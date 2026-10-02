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
	hook.ENIPlumber
	DeleteENI(context.Context, model.VPC, model.ENI) error
}

func serve(ctx context.Context, ln net.Listener, tokenPath, dbPath, hookPath string, build version.Info, network networkEngine, runtime compute.Runtime) error {
	token, err := credentials.LoadOrCreate(tokenPath)
	if err != nil {
		return fmt.Errorf("loading API token: %w", err)
	}
	s, err := store.Open(ctx, dbPath)
	if err != nil {
		return fmt.Errorf("opening appliance state: %w", err)
	}
	defer s.Close()
	hookListener, err := hook.Listen(ctx, hookPath)
	if err != nil {
		return fmt.Errorf("open private hook listener: %w", err)
	}
	defer hookListener.Close()
	hookServer := &http.Server{Handler: hook.NewHandler(s, network), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }}
	hookDone := make(chan error, 1)
	go func() { hookDone <- hookServer.Serve(hookListener) }()
	controller := reconcile.New(s, network, 60*time.Second)
	instances := reconcile.NewInstances(s, instanceNetwork{store: s, engine: network}, runtime, 60*time.Second)
	resources := service.NewNetwork(s, controller.Enqueue, nil)
	instanceService := service.NewInstances(s, instances.Enqueue, nil)
	var ready atomic.Bool
	h := apiserver.New(token, build, ready.Load, resources, instanceService, s, runtime)
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second}
	serverDone := make(chan error, 1)
	go func() { serverDone <- srv.Serve(ln) }()
	shutdown := func() {
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		_ = hookServer.Shutdown(shutdownCtx)
	}
	if err := controller.Sweep(ctx); err != nil {
		shutdown()
		<-serverDone
		<-hookDone
		return fmt.Errorf("initial VPC reconcile sweep: %w", err)
	}
	if err := instances.Sweep(ctx); err != nil {
		shutdown()
		<-serverDone
		<-hookDone
		return fmt.Errorf("initial instance reconcile sweep: %w", err)
	}
	ready.Store(true)
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	controllerDone := make(chan error, 1)
	go func() { controllerDone <- controller.Run(runCtx) }()
	instanceDone := make(chan error, 1)
	go func() { instanceDone <- instances.Run(runCtx) }()
	var serveErr, controllerErr, instanceErr, hookErr error
	serverExited, controllerExited, instanceExited, hookExited := false, false, false, false
	select {
	case <-ctx.Done():
	case serveErr = <-serverDone:
		serverExited = true
	case controllerErr = <-controllerDone:
		controllerExited = true
	case instanceErr = <-instanceDone:
		instanceExited = true
	case hookErr = <-hookDone:
		hookExited = true
	}
	ready.Store(false)
	stop()
	shutdown()
	if !serverExited {
		serveErr = <-serverDone
	}
	if !controllerExited {
		controllerErr = <-controllerDone
	}
	if !instanceExited {
		instanceErr = <-instanceDone
	}
	if !hookExited {
		hookErr = <-hookDone
	}
	if hookErr != nil && !errors.Is(hookErr, http.ErrServerClosed) {
		return fmt.Errorf("serve private hook: %w", hookErr)
	}
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		return fmt.Errorf("serving the API: %w", serveErr)
	}
	if controllerErr != nil && !errors.Is(controllerErr, context.Canceled) {
		return fmt.Errorf("running VPC reconciler: %w", controllerErr)
	}
	if instanceErr != nil && !errors.Is(instanceErr, context.Canceled) {
		return fmt.Errorf("running instance reconciler: %w", instanceErr)
	}
	return nil
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
