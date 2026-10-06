package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Amrzxk/nephos/internal/compute"
	"github.com/Amrzxk/nephos/internal/lifecycle"
	"github.com/Amrzxk/nephos/internal/model"
	"github.com/Amrzxk/nephos/internal/store"
)

const daemonShutdownBudget = 25 * time.Second
const nestedStopBudget = 15 * time.Second

type pendingStarts struct {
	compute.Runtime
	gate    *lifecycle.Gate
	mu      sync.Mutex
	parents map[compute.Reference]*lifecycle.Lease
	failure chan<- error
	failed  atomic.Bool
}

func newOwnedRuntime(runtime compute.Runtime, gate *lifecycle.Gate, failure chan<- error) *pendingStarts {
	return &pendingStarts{Runtime: runtime, gate: gate, parents: make(map[compute.Reference]*lifecycle.Lease), failure: failure}
}

func (p *pendingStarts) begin(ctx context.Context) (*lifecycle.Lease, error) {
	if parent := lifecycle.ContextLease(ctx); parent != nil {
		return p.gate.EnterChild(ctx, parent)
	}
	return nil, fmt.Errorf("runtime mutation has no admitted effect lease")
}

func (p *pendingStarts) finish(ctx context.Context, lease *lifecycle.Lease, err error) {
	p.observe(ctx, err)
	if errors.Is(err, compute.ErrUncertainEffect) {
		// Never erase an unknown external mutation just because its HTTP waiter
		// returned. Retain the lease, fail the daemon closed, and preserve roots
		// for a fresh whole-appliance startup/authoritative recovery.
		p.reportFailure(err)
		return
	}
	_ = lease.Close()
}

func (p *pendingStarts) observe(ctx context.Context, err error) {
	if errors.Is(err, compute.ErrUncertainEffect) || ctx.Err() == nil && errors.Is(err, compute.ErrInspectionUnavailable) {
		p.reportFailure(err)
	}
}

func (p *pendingStarts) EnsureImage(ctx context.Context, image string) error {
	err := p.Runtime.EnsureImage(ctx, image)
	p.observe(ctx, err)
	return err
}

func (p *pendingStarts) reportFailure(err error) {
	p.failed.Store(true)
	p.gate.Seal("default")
	if p.failure != nil {
		select {
		case p.failure <- err:
		default:
		}
	}
}

func (p *pendingStarts) Lookup(ctx context.Context, identity compute.Identity) (compute.Reference, error) {
	ref, err := p.Runtime.Lookup(ctx, identity)
	p.observe(ctx, err)
	return ref, err
}

func (p *pendingStarts) Inspect(ctx context.Context, ref compute.Reference) (compute.Status, error) {
	status, err := p.Runtime.Inspect(ctx, ref)
	p.observe(ctx, err)
	return status, err
}

func (p *pendingStarts) Create(ctx context.Context, instance model.Instance) (created compute.CreateResult, err error) {
	lease, err := p.begin(ctx)
	if err != nil {
		return created, err
	}
	defer func() { p.finish(ctx, lease, err) }()
	return p.Runtime.Create(ctx, instance)
}

func (p *pendingStarts) Delete(ctx context.Context, ref compute.Reference) (err error) {
	lease, err := p.begin(ctx)
	if err != nil {
		return err
	}
	defer func() { p.finish(ctx, lease, err) }()
	return p.Runtime.Delete(ctx, ref)
}

func (p *pendingStarts) Exec(ctx context.Context, ref compute.Reference, request compute.ExecRequest) (session compute.ExecSession, err error) {
	lease, err := p.begin(ctx)
	if err != nil {
		return nil, err
	}
	session, err = p.Runtime.Exec(ctx, ref, request)
	if err != nil {
		p.finish(ctx, lease, err)
		return nil, err
	}
	return &ownedExecSession{ExecSession: session, owner: p, lease: lease, parent: ctx}, nil
}

type ownedExecSession struct {
	compute.ExecSession
	owner  *pendingStarts
	lease  *lifecycle.Lease
	parent context.Context
	once   sync.Once
	err    error
}

func (s *ownedExecSession) Close() error {
	s.once.Do(func() {
		s.err = s.ExecSession.Close()
		if s.err != nil {
			s.err = errors.Join(s.err, compute.ErrUncertainEffect)
		}
		s.owner.finish(s.parent, s.lease, s.err)
	})
	return s.err
}

func (p *pendingStarts) Start(ctx context.Context, ref compute.Reference) (result error) {
	parent := lifecycle.ContextLease(ctx)
	if parent == nil {
		return fmt.Errorf("runtime Start has no admitted effect lease")
	}
	// Keep a dedicated Start lease until both the HTTP operation and any
	// verified hook children have finished. Register before calling Podman.
	lease, err := p.gate.EnterChild(ctx, parent)
	if err != nil {
		return err
	}
	defer func() { p.finish(ctx, lease, result) }()
	p.mu.Lock()
	if _, exists := p.parents[ref]; exists {
		p.mu.Unlock()
		return fmt.Errorf("runtime Start already pending for %s", ref.Identity.InstanceID)
	}
	p.parents[ref] = lease
	p.mu.Unlock()
	defer func() {
		if !errors.Is(result, compute.ErrUncertainEffect) {
			p.mu.Lock()
			delete(p.parents, ref)
			p.mu.Unlock()
		}
	}()
	return p.Runtime.Start(ctx, ref)
}

func (p *pendingStarts) admitHook(ctx context.Context, ref compute.Reference) (func() error, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	parent := p.parents[ref]
	if parent == nil {
		return nil, fmt.Errorf("no admitted runtime Start for verified hook identity")
	}
	child, err := p.gate.EnterChild(ctx, parent)
	if err != nil {
		return nil, err
	}
	return child.Close, nil
}

func consoleAdmission(gate *lifecycle.Gate) func(context.Context, model.Instance) (context.Context, func() error, error) {
	return func(ctx context.Context, instance model.Instance) (context.Context, func() error, error) {
		lease, err := gate.Enter(ctx, instance.WorkspaceID)
		if err != nil {
			return ctx, nil, err
		}
		return lifecycle.WithLease(ctx, lease), lease.Close, nil
	}
}

type daemonWorker struct {
	name   string
	done   <-chan error
	result error
	joined bool
}

func (w *daemonWorker) join(ctx context.Context) error {
	if !w.joined {
		select {
		case w.result = <-w.done:
			w.joined = true
		case <-ctx.Done():
			return fmt.Errorf("join %s: %w", w.name, ctx.Err())
		}
	}
	if w.result != nil && !errors.Is(w.result, context.Canceled) && !errors.Is(w.result, http.ErrServerClosed) {
		return fmt.Errorf("%s: %w", w.name, w.result)
	}
	return nil
}

// stopNested uses one shared deadline, not a fresh timeout per instance. It
// never removes roots or treats an unavailable runtime as an empty inventory.
func stopNested(ctx context.Context, s *store.Store, runtime compute.Runtime) error {
	stopCtx, cancel := context.WithTimeout(ctx, nestedStopBudget)
	defer cancel()
	var errs []error
	after := ""
	for {
		page, err := s.ListInstancePage(stopCtx, "default", after, 100)
		if err != nil {
			return errors.Join(append(errs, fmt.Errorf("inventory nested instances: %w", err))...)
		}
		if len(page) == 0 {
			return errors.Join(errs...)
		}
		for i := range page {
			instance := &page[i]
			ref, err := runtime.Lookup(stopCtx, compute.Identity{WorkspaceID: instance.WorkspaceID, InstanceID: instance.ID})
			if errors.Is(err, compute.ErrNotFound) {
				continue
			}
			if err != nil {
				errs = append(errs, fmt.Errorf("inspect shutdown instance %s: %w", instance.ID, err))
				continue
			}
			remaining := nestedStopBudget
			if deadline, ok := stopCtx.Deadline(); ok {
				remaining = time.Until(deadline)
			}
			if err := runtime.Stop(stopCtx, ref, remaining); err != nil {
				errs = append(errs, fmt.Errorf("stop nested instance %s: %w", instance.ID, err))
			}
		}
		after = page[len(page)-1].ID
	}
}

func shutdownDaemon(ctx context.Context, gate *lifecycle.Gate, refs *lifecycle.Registry,
	s *store.Store, runtime compute.Runtime, servers []*http.Server, workers []*daemonWorker, budget time.Duration) (bool, error) {
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), budget)
	defer cancel()
	var errs []error
	safe := true
	// HTTP Shutdown does not own hijacked WebSockets. Drain their registry in
	// parallel with both servers so blocked streams cannot delay cancellation.
	serverStops := make(chan error, len(servers))
	for _, server := range servers {
		go func() {
			err := server.Shutdown(shutdownCtx)
			if err != nil {
				err = errors.Join(err, server.Close())
			}
			serverStops <- err
		}()
	}
	drainDone := make(chan error, 1)
	go func() { drainDone <- refs.Drain(shutdownCtx, "default") }()
	for range servers {
		select {
		case err := <-serverStops:
			if err != nil {
				safe = false
				errs = append(errs, fmt.Errorf("shutdown HTTP server: %w", err))
			}
		case <-shutdownCtx.Done():
			safe = false
			errs = append(errs, shutdownCtx.Err())
		}
	}
	for _, worker := range workers {
		if err := worker.join(shutdownCtx); err != nil {
			errs = append(errs, err)
		}
		safe = safe && worker.joined
	}
	select {
	case err := <-drainDone:
		if err != nil {
			errs = append(errs, fmt.Errorf("drain owned references: %w", err))
		}
	case <-shutdownCtx.Done():
		safe = false
		errs = append(errs, shutdownCtx.Err())
	}
	// The exclusive lease remains held through all nested stops. If any live
	// effect survives the shared budget, stop is not authorized and roots stay.
	if err := gate.Exclusive(shutdownCtx, "default", func(ctx context.Context) error { return stopNested(ctx, s, runtime) }); err != nil {
		errs = append(errs, err)
	}
	// Store closure needs joined in-process users. Unknown remote mutation
	// participation still blocks Stop/clean success but cannot access SQLite
	// after controllers, hook handlers and console workers have all joined.
	return safe && len(refs.Outstanding("default")) == 0, errors.Join(errs...)
}
