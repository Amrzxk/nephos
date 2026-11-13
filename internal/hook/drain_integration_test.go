//go:build linux && integration

package hook

import (
	"context"
	"errors"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Amrzxk/nephos/internal/compute"
	"github.com/Amrzxk/nephos/internal/lifecycle"
)

func TestVerifiedHookHTTPChildCompletesDuringDrain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	gate := lifecycle.NewGate()
	parent, err := gate.Enter(ctx, "default")
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	reached := make(chan compute.Reference, 1)
	allow := make(chan struct{})
	var expected atomic.Value
	f := hookWorld(t, false, func(ctx context.Context, ref compute.Reference) (func() error, error) {
		// NewHandler invokes us only after real peer, store/runtime and pinned
		// namespace validation. Hold here until exclusivity is requested.
		reached <- ref
		select {
		case <-allow:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if ref != expected.Load().(compute.Reference) {
			return nil, errors.New("wrong verified Start binding")
		}
		child, err := gate.EnterChild(ctx, parent)
		if err != nil {
			return nil, err
		}
		return child.Close, nil
	})
	ref := compute.Reference{Identity: compute.Identity{WorkspaceID: f.instance.WorkspaceID, InstanceID: f.instance.ID}, ID: compute.RuntimeID(f.instance.RuntimeID)}
	expected.Store(ref)
	started := make(chan error, 1)
	go func() { started <- f.runtime.Start(ctx, ref) }()
	select {
	case got := <-reached:
		if got != ref {
			t.Fatal("hook reached admission with wrong identity")
		}
	case <-ctx.Done():
		t.Fatal("verified hook never reached child admission")
	}
	done := make(chan error, 1)
	go func() { done <- gate.Exclusive(ctx, "default", func(context.Context) error { return nil }) }()
	for {
		lease, err := gate.Enter(ctx, "default")
		if errors.Is(err, lifecycle.ErrDraining) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		_ = lease.Close()
		runtime.Gosched()
	}
	close(allow)
	select {
	case err := <-started:
		if err != nil {
			t.Fatalf("verified hook failed during drain: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("Start deadlocked with verified hook drain")
	}
	_ = parent.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !f.plumber.beforePID1.Load() {
		t.Fatal("verified child did not finish ENI plumbing")
	}
}
