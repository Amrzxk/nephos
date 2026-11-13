// Command nephosd is the Nephos control plane. It runs inside the appliance
// container and owns the API server, the store, the reconcilers, and the
// network and compute engines.
//
// M1 serves persisted VPC/subnet resources and converges their topology before
// the appliance reports ready.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Amrzxk/nephos/internal/apiserver"
	"github.com/Amrzxk/nephos/internal/compute"
	"github.com/Amrzxk/nephos/internal/compute/podman"
	"github.com/Amrzxk/nephos/internal/credentials"
	"github.com/Amrzxk/nephos/internal/hook"
	"github.com/Amrzxk/nephos/internal/leakcheck"
	"github.com/Amrzxk/nephos/internal/lifecycle"
	"github.com/Amrzxk/nephos/internal/model"
	"github.com/Amrzxk/nephos/internal/network/topology"
	"github.com/Amrzxk/nephos/internal/reconcile"
	"github.com/Amrzxk/nephos/internal/service"
	"github.com/Amrzxk/nephos/internal/store"
	"github.com/Amrzxk/nephos/internal/version"
)

func main() {
	// os.Exit skips deferred calls, so the whole process body lives in
	// realMain and main does nothing but translate its result.
	os.Exit(realMain())
}

func realMain() int {
	logger := newLogger(os.Getenv("NEPHOS_LOG_LEVEL"))
	slog.SetDefault(logger)

	// context.Background() belongs only in main and tests (AGENTS.md).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, logger); err != nil {
		logger.Error("nephosd exited with an error", slog.Any("error", err))
		return 1
	}
	return 0
}

func run(ctx context.Context, logger *slog.Logger) error {
	info := version.Get()
	logger.Info("nephosd starting",
		slog.String("version", info.Version),
		slog.String("commit", info.Commit),
		slog.String("go_version", info.GoVersion),
		slog.String("platform", info.Platform),
	)

	var listenConfig net.ListenConfig
	ln, err := listenConfig.Listen(ctx, "tcp", ":7788")
	if err != nil {
		return fmt.Errorf("listening for the API: %w", err)
	}
	defer ln.Close()
	if err := serve(ctx, ln, "/var/lib/nephos/secrets/api-token", "/var/lib/nephos/state/nephos.db", hook.SocketPath, info, topology.New(), podman.New("/run/podman/podman.sock")); err != nil {
		return err
	}
	logger.Info("nephosd shutting down")
	return nil
}

type networkEngine interface {
	reconcile.NetworkEngine
	reconcile.OrphanNetwork
	hook.ENIPlumber
	DeleteENI(context.Context, model.VPC, model.ENI) error
}

type daemonOptions struct{ shutdownBudget time.Duration }

func serve(ctx context.Context, ln net.Listener, tokenPath, dbPath, hookPath string, build version.Info, network networkEngine, runtime compute.Runtime, options ...daemonOptions) (result error) {
	budget := daemonShutdownBudget
	if len(options) > 0 && options[0].shutdownBudget > 0 {
		budget = options[0].shutdownBudget
	}
	token, err := credentials.LoadOrCreate(tokenPath)
	if err != nil {
		return fmt.Errorf("loading API token: %w", err)
	}
	s, err := store.Open(ctx, dbPath)
	if err != nil {
		return fmt.Errorf("opening appliance state: %w", err)
	}
	canCloseStore := true
	defer func() {
		if canCloseStore {
			result = errors.Join(result, s.Close())
		} else {
			slog.Error("store retained until process exit because owners did not fully join")
		}
	}()
	lifetime, stop := context.WithCancel(ctx)
	defer stop()
	gate := lifecycle.NewGate()
	refs := lifecycle.NewRegistry()
	lifetime = lifecycle.WithRegistry(lifetime, refs)
	mutationFailure := make(chan error, 1)
	starts := newOwnedRuntime(runtime, gate, mutationFailure)
	hookListener, err := hook.Listen(ctx, hookPath)
	if err != nil {
		return fmt.Errorf("open private hook listener: %w", err)
	}
	defer hookListener.Close()
	hookServer := &http.Server{Handler: hook.NewHandler(s, network, starts.admitHook), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, BaseContext: func(net.Listener) context.Context { return lifetime }}
	hookDone := make(chan error, 1)
	go func() { hookDone <- hookServer.Serve(hookListener) }()
	controller := reconcile.New(s, network, 60*time.Second, gate)
	instances := reconcile.NewInstances(s, instanceNetwork{store: s, engine: network}, starts, 60*time.Second, gate)
	controller.OnConverged(instances.WakeWaiting)
	resources := service.NewNetwork(s, controller.Enqueue, nil)
	instanceService := service.NewInstances(s, instances.Enqueue, nil)
	resetGuard := &service.ResetGuard{}
	resources.GuardResets(resetGuard)
	instanceService.GuardResets(resetGuard)
	checker := &leakcheck.Checker{Store: s, Runtime: runtime, Network: network, Registry: refs, Gate: gate, Processes: leakcheck.LinuxProcesses{}}
	reset := service.NewReset(s, resources, instanceService, resetGuard,
		newResetVerifier(lifetime, gate, s, runtime, network, checker, mutationFailure))
	var ready atomic.Bool
	h := apiserver.New(token, build, func() bool { return ready.Load() && !starts.failed.Load() }, resources, instanceService, s, starts, apiserver.WithConsoleLifecycle(consoleAdmission(gate), refs),
		apiserver.WithDegraded(func() bool { return controller.Degraded() || instances.Degraded() }), apiserver.WithReset(reset))
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second, BaseContext: func(net.Listener) context.Context { return lifetime }}
	serverDone := make(chan error, 1)
	go func() { serverDone <- srv.Serve(ln) }()
	orphansReady := make(chan struct{})
	orphansDone := make(chan error, 1)
	go func() { orphansDone <- runOrphanMaintenance(lifetime, gate, s, runtime, network, orphansReady) }()
	networkReady := make(chan struct{})
	controllerDone := make(chan error, 1)
	go func() {
		select {
		case <-orphansReady:
		case <-lifetime.Done():
			controllerDone <- lifetime.Err()
			return
		}
		if err := sweepUntilComplete(lifetime, "VPC", controller.Sweep); err != nil {
			controllerDone <- err
			return
		}
		close(networkReady)
		controllerDone <- controller.Run(lifetime)
	}()
	instanceDone := make(chan error, 1)
	go func() {
		select {
		case <-networkReady:
		case <-lifetime.Done():
			instanceDone <- lifetime.Err()
			return
		}
		if err := sweepUntilComplete(lifetime, "instance", instances.Sweep); err != nil {
			instanceDone <- err
			return
		}
		if lifetime.Err() == nil {
			ready.Store(true)
		}
		instanceDone <- instances.Run(lifetime)
	}()
	workers := []*daemonWorker{{name: "serving the API", done: serverDone}, {name: "serve private hook", done: hookDone},
		{name: "running VPC reconciler", done: controllerDone}, {name: "running instance reconciler", done: instanceDone}, {name: "collecting owned orphans", done: orphansDone}}
	canCloseStore = false
	var exited *daemonWorker
	var failure error
	select {
	case <-ctx.Done():
	case err := <-mutationFailure:
		failure = fmt.Errorf("runtime lifecycle failure: %w", err)
	case err := <-serverDone:
		exited = workers[0]
		exited.result = err
	case err := <-hookDone:
		exited = workers[1]
		exited.result = err
	case err := <-controllerDone:
		exited = workers[2]
		exited.result = err
	case err := <-instanceDone:
		exited = workers[3]
		exited.result = err
	case err := <-orphansDone:
		exited = workers[4]
		exited.result = err
	}
	if exited != nil {
		exited.joined = true
		if ctx.Err() == nil && (exited.result == nil || errors.Is(exited.result, http.ErrServerClosed) || errors.Is(exited.result, context.Canceled)) {
			exited.result = fmt.Errorf("unexpected worker exit without daemon cancellation")
		}
	}
	ready.Store(false)
	gate.Seal("default")
	stop()
	canCloseStore, result = shutdownDaemon(lifetime, gate, refs, s, runtime, []*http.Server{srv, hookServer}, workers, budget)
	return errors.Join(failure, result)
}

// newLogger builds the structured JSON logger every Nephos component uses.
// Text output is deliberately not offered: the CLI pretty-prints the JSON
// stream (`nephos logs`), so there is one wire format.
func newLogger(levelName string) *slog.Logger {
	level := slog.LevelInfo
	switch levelName {
	case "debug", "DEBUG":
		level = slog.LevelDebug
	case "warn", "WARN":
		level = slog.LevelWarn
	case "error", "ERROR":
		level = slog.LevelError
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
}
