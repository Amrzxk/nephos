package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Amrzxk/nephos/internal/compute"
	"github.com/Amrzxk/nephos/internal/leakcheck"
	"github.com/Amrzxk/nephos/internal/lifecycle"
	"github.com/Amrzxk/nephos/internal/model"
	"github.com/Amrzxk/nephos/internal/network/topology"
	"github.com/Amrzxk/nephos/internal/store"
)

type failingInventory struct{ compute.Runtime }

func (failingInventory) Inventory(context.Context) ([]compute.ObservedContainer, error) {
	return nil, errors.New("podman socket closed")
}

type emptyNetwork struct{}

func (emptyNetwork) Inventory(context.Context) (topology.Inventory, error) {
	return topology.Inventory{}, nil
}
func (emptyNetwork) CollectOrphans(context.Context, []model.VPC, []model.Subnet, []model.ENI) error {
	return nil
}

func verifierFixture(t *testing.T, runtime compute.Runtime) (*lifecycle.Gate, resetVerifier, chan error) {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	gate := lifecycle.NewGate()
	failure := make(chan error, 1)
	checker := &leakcheck.Checker{Store: s, Runtime: runtime, Network: emptyNetwork{}, Registry: lifecycle.NewRegistry(), Gate: gate, Processes: leakcheck.LinuxProcesses{}}
	return gate, newResetVerifier(context.Background(), gate, s, runtime, emptyNetwork{}, checker, failure), failure
}

func TestResetVerifierTimeoutWhileBusyNeverSeals(t *testing.T) {
	gate, verify, failure := verifierFixture(t, failingInventory{})
	lease, err := gate.Enter(context.Background(), "default") // a live console
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := verify.Verify(ctx, "default"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v, want deadline", err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if next, err := gate.Enter(context.Background(), "default"); err != nil {
		t.Fatalf("gate sealed by an expired wait: %v", err)
	} else {
		_ = next.Close()
	}
	select {
	case err := <-failure:
		t.Fatalf("daemon failure signaled for a timeout: %v", err)
	default:
	}
}

func TestResetVerifierCollectionFailureSealsAndStopsDaemon(t *testing.T) {
	gate, verify, failure := verifierFixture(t, failingInventory{})
	if _, err := verify.Verify(context.Background(), "default"); err == nil {
		t.Fatal("collection failure reported as verified")
	}
	if _, err := gate.Enter(context.Background(), "default"); err == nil {
		t.Fatal("gate stayed open after uncertain cleanup")
	}
	select {
	case <-failure:
	default:
		t.Fatal("daemon was not told to stop")
	}
}
