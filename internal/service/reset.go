package service

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/Amrzxk/nephos/internal/model"
	"github.com/Amrzxk/nephos/internal/store"
)

// ResetGuard refuses creates while a soft reset runs. It lives in memory and
// only for the duration of the reset request, so it is never a second source
// of truth (ADR-0007). If the daemon restarts mid-reset the guard is gone and
// rerunning the reset is the recovery path.
type ResetGuard struct{ active atomic.Bool }

func (g *ResetGuard) begin() bool { return g.active.CompareAndSwap(false, true) }
func (g *ResetGuard) end()        { g.active.Store(false) }

// check is nil-safe so services built without a guard (tests) never refuse.
func (g *ResetGuard) check() error {
	if g != nil && g.active.Load() {
		return resetInProgress()
	}
	return nil
}

// ResetVerifier removes owned orphans and runs the leak check under exclusive
// effect admission. It returns the observation even when it is incomplete.
type ResetVerifier interface {
	Verify(ctx context.Context, workspace string) (model.LeakResult, error)
}

// ResetDeps is the store view the coordinator needs; *store.Store satisfies it.
type ResetDeps interface {
	DesiredSnapshot(ctx context.Context, workspace string) (store.DesiredState, error)
}

// Reset empties the default workspace through the ordinary delete paths, so
// the reconcilers remain the only code that touches the kernel and Podman.
type Reset struct {
	store     ResetDeps
	network   *Network
	instances *Instances
	guard     *ResetGuard
	verifier  ResetVerifier
	poll      time.Duration
}

// NewReset wires the coordinator. network and instances must share guard.
func NewReset(s ResetDeps, network *Network, instances *Instances, guard *ResetGuard, verifier ResetVerifier) *Reset {
	return &Reset{store: s, network: network, instances: instances, guard: guard, verifier: verifier, poll: 250 * time.Millisecond}
}

// ResetResult is the verified outcome of a successful soft reset.
type ResetResult struct {
	Workspace string
	LeakCheck model.LeakResult
}

// LeakError reports a reset whose teardown finished but whose leak check
// found residue or could not observe everything.
type LeakError struct{ Result model.LeakResult }

func (e *LeakError) Error() string {
	if !e.Result.Complete {
		return fmt.Sprintf("LeakDetected: leak check was incomplete (%d inspection errors)", len(e.Result.InspectionErrors))
	}
	return fmt.Sprintf("LeakDetected: %d Nephos-owned objects remain after reset", len(e.Result.Findings))
}

// Run deletes instances, then subnets, then VPCs, waiting for each tier to
// disappear, and succeeds only when the leak check is clean. ctx bounds the
// whole reset; on expiry resources stay marked for deletion.
func (r *Reset) Run(ctx context.Context) (ResetResult, error) {
	if !r.guard.begin() {
		return ResetResult{}, resetInProgress()
	}
	defer r.guard.end()
	var last store.DesiredState // reported on timeout
	for {
		state, err := r.store.DesiredSnapshot(ctx, defaultWorkspace)
		if err != nil {
			if ctx.Err() != nil {
				return ResetResult{}, resetTimeout(last)
			}
			return ResetResult{}, fmt.Errorf("observe workspace for reset: %w", err)
		}
		last = state
		// A create admitted just before the guard may commit after a previous
		// snapshot; looping until a snapshot is empty catches it.
		if len(state.Instances)+len(state.Subnets)+len(state.VPCs) == 0 {
			break
		}
		if err := r.requestDeletion(ctx, state); err != nil {
			if ctx.Err() != nil {
				return ResetResult{}, resetTimeout(state)
			}
			return ResetResult{}, err
		}
		select {
		case <-ctx.Done():
			return ResetResult{}, resetTimeout(state)
		case <-time.After(r.poll):
		}
	}
	result, err := r.verifier.Verify(ctx, defaultWorkspace)
	if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
		return ResetResult{}, resetTimeout(store.DesiredState{})
	}
	if err != nil {
		return ResetResult{}, fmt.Errorf("verify reset: %w", err)
	}
	if !result.Clean {
		return ResetResult{}, &LeakError{Result: result}
	}
	return ResetResult{Workspace: defaultWorkspace, LeakCheck: result}, nil
}

// requestDeletion marks only the innermost remaining tier, because subnets
// and VPCs refuse deletion while they still have children.
func (r *Reset) requestDeletion(ctx context.Context, state store.DesiredState) error {
	var pending []string
	var remove func(context.Context, string) error
	switch {
	case len(state.Instances) != 0:
		for i := range state.Instances {
			if !state.Instances[i].DeletionRequested {
				pending = append(pending, state.Instances[i].ID)
			}
		}
		remove = func(ctx context.Context, id string) error {
			_, err := r.instances.TerminateSnapshot(ctx, id)
			return err
		}
	case len(state.Subnets) != 0:
		for i := range state.Subnets {
			if !state.Subnets[i].DeletionRequested {
				pending = append(pending, state.Subnets[i].ID)
			}
		}
		remove = func(ctx context.Context, id string) error { _, err := r.network.DeleteSubnet(ctx, id); return err }
	default:
		for i := range state.VPCs {
			if !state.VPCs[i].DeletionRequested {
				pending = append(pending, state.VPCs[i].ID)
			}
		}
		remove = func(ctx context.Context, id string) error { _, err := r.network.DeleteVPC(ctx, id); return err }
	}
	for _, id := range pending {
		if err := ignorableResetRace(remove(ctx, id)); err != nil {
			return fmt.Errorf("remove %s for reset: %w", id, err)
		}
	}
	return nil
}

// ignorableResetRace drops errors the next snapshot resolves: a row the
// reconciler already removed, or a child that appeared after the snapshot.
func ignorableResetRace(err error) error {
	var domain *Error
	if errors.As(err, &domain) && (domain.Status == 404 || domain.Code == "DependencyViolation") {
		return nil
	}
	return err
}

func resetInProgress() error {
	return &Error{Code: "ResetInProgress", Message: "a workspace reset is in progress; retry when it finishes", Status: 409}
}

func resetTimeout(state store.DesiredState) error {
	message := "reset timed out before the workspace was verified empty"
	if n := len(state.Instances) + len(state.Subnets) + len(state.VPCs); n != 0 {
		message = fmt.Sprintf("reset timed out with %d instances, %d subnets and %d VPCs still being removed",
			len(state.Instances), len(state.Subnets), len(state.VPCs))
	}
	return &Error{Code: "ResetTimeout", Message: message + "; rerun the reset to continue", Status: 504}
}
