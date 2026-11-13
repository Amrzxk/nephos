package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Amrzxk/nephos/internal/compute"
	"github.com/Amrzxk/nephos/internal/lifecycle"
	"github.com/Amrzxk/nephos/internal/store"
)

type rowlessStopRuntime struct {
	compute.Runtime
	items   []compute.ObservedContainer
	stopped []compute.Reference
}

func TestOrphanMaintenanceCarriesExclusiveAuthority(t *testing.T) {
	gate := lifecycle.NewGate()
	ran, err := orphanMaintenanceCycle(context.Background(), gate, time.Second, false, func(ctx context.Context) error {
		if !gate.HoldsExclusive(ctx, "default") {
			t.Error("maintenance callback lost its exclusive lease token")
		}
		return nil
	})
	if err != nil || !ran {
		t.Fatalf("maintenance did not run: %v %v", ran, err)
	}
}

func TestPeriodicOrphanMaintenanceDoesNotCancelBusyConsole(t *testing.T) {
	ctx := context.Background()
	gate := lifecycle.NewGate()
	lease, err := gate.Enter(ctx, "default")
	if err != nil {
		t.Fatal(err)
	}
	called := false
	ran, err := orphanMaintenanceCycle(ctx, gate, 20*time.Millisecond, true, func(context.Context) error { called = true; return nil })
	if err != nil || ran || called {
		t.Fatalf("busy maintenance ran=%v called=%v err=%v", ran, called, err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	ran, err = orphanMaintenanceCycle(ctx, gate, time.Second, true, func(context.Context) error { called = true; return nil })
	if err != nil || !ran || !called {
		t.Fatalf("idle maintenance did not run: %v %v", ran, err)
	}
}

func (r *rowlessStopRuntime) Inventory(context.Context) ([]compute.ObservedContainer, error) {
	return r.items, nil
}
func (r *rowlessStopRuntime) Stop(_ context.Context, ref compute.Reference, _ time.Duration) error {
	r.stopped = append(r.stopped, ref)
	return nil
}

func TestNestedStopIncludesRowlessOwnedRoots(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ref := compute.Reference{Identity: compute.Identity{WorkspaceID: "default", InstanceID: "i-00000000000000001"}, ID: compute.RuntimeID(strings.Repeat("a", 64))}
	runtime := &rowlessStopRuntime{items: []compute.ObservedContainer{{Reference: ref, Name: ref.Identity.InstanceID, Managed: true}}}
	if err := stopNested(ctx, s, runtime); err != nil {
		t.Fatal(err)
	}
	if len(runtime.stopped) != 1 || runtime.stopped[0] != ref {
		t.Fatalf("rowless root escaped graceful shutdown: %+v", runtime.stopped)
	}
	runtime.stopped = nil
	runtime.items[0].Managed = false
	if err := stopNested(ctx, s, runtime); err == nil || len(runtime.stopped) != 0 {
		t.Fatalf("ambiguous root was stopped: %+v err=%v", runtime.stopped, err)
	}
}
