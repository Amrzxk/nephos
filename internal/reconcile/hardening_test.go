package reconcile

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Amrzxk/nephos/internal/lifecycle"
)

func TestRetryDelayBacksOffToCap(t *testing.T) {
	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{1, retryBase},
		{2, 2 * retryBase},
		{3, 4 * retryBase},
		{20, retryMax},
		{100, retryMax},
	}
	for _, tt := range tests {
		if got := retryDelay(tt.attempt); got != tt.want {
			t.Errorf("retryDelay(%d) = %s, want %s", tt.attempt, got, tt.want)
		}
	}
}

func TestIsFatalOnlyForInvariants(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"store contention", errors.New("database is locked"), false},
		{"wrapped store error", fmt.Errorf("load VPC: %w", errors.New("sql: database is closed")), false},
		{"not admitted", ErrNotAdmitted, false},
		{"invariant", fmt.Errorf("%w: deleting VPC still has subnets", errInvariant), true},
		{"wrapped invariant", fmt.Errorf("sweep: %w", fmt.Errorf("%w: bad index", errInvariant)), true},
	}
	for _, tt := range tests {
		if got := IsFatal(tt.err); got != tt.want {
			t.Errorf("%s: IsFatal = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestDegradationTracksRetryingResources(t *testing.T) {
	var d degradation
	if d.Degraded() {
		t.Fatal("new tracker is degraded")
	}
	d.mark("vpc-1")
	d.mark("vpc-2")
	d.clear("vpc-1")
	if !d.Degraded() {
		t.Fatal("one resource still retrying, want degraded")
	}
	d.clear("vpc-2")
	if d.Degraded() {
		t.Fatal("all resources recovered, want healthy")
	}
}

func TestSweepReportsNotAdmittedWhileDraining(t *testing.T) {
	s, _, f, _ := setup(t)
	gate := lifecycle.NewGate()
	gate.Seal("default")
	vpcs := New(s, f, time.Hour, gate)
	if err := vpcs.Sweep(context.Background()); !errors.Is(err, ErrNotAdmitted) {
		t.Fatalf("VPC sweep on a draining gate = %v, want ErrNotAdmitted", err)
	}
	instances := NewInstances(s, &instanceTestNetwork{}, &instanceTestRuntime{}, time.Hour, gate)
	if err := instances.Sweep(context.Background()); !errors.Is(err, ErrNotAdmitted) {
		t.Fatalf("instance sweep on a draining gate = %v, want ErrNotAdmitted", err)
	}
}

func TestRunSurvivesTransientStoreFailure(t *testing.T) {
	s, svc, f, c := setup(t)
	vpc, _ := create(t, svc)
	// A closed store fails every query, standing in for SQLite contention or
	// I/O errors. The controller must retry and report degraded, not exit.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	c.Enqueue(vpc.ID)
	deadline := time.Now().Add(5 * time.Second)
	for !c.Degraded() {
		select {
		case err := <-done:
			t.Fatalf("controller exited on a transient store error: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("controller never reported degraded")
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case err := <-done:
		t.Fatalf("controller exited after degrading: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run after cancel = %v, want context.Canceled", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ensures != 0 {
		t.Fatalf("effects ran without readable desired state: %d", f.ensures)
	}
}

func TestVPCConvergenceWakesWaitingInstances(t *testing.T) {
	s, svc, f, c := setup(t)
	create(t, svc)
	instances := NewInstances(s, &instanceTestNetwork{}, &instanceTestRuntime{}, time.Hour)
	instances.setWaiting("i-waiting", true)
	c.OnConverged(instances.WakeWaiting)
	if err := c.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := instances.pop(); got != "i-waiting" {
		t.Fatalf("waiting instance not re-enqueued after VPC converged: %q", got)
	}
	if got := instances.pop(); got != "" {
		t.Fatalf("unexpected extra enqueue %q", got)
	}
	// Woken instances leave the waiting set; a second wake is a no-op.
	instances.WakeWaiting()
	if got := instances.pop(); got != "" {
		t.Fatalf("instance woken twice: %q", got)
	}
	f.failEnsure = 1
	instances.setWaiting("i-other", true)
	if err := c.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := instances.pop(); got != "" {
		t.Fatalf("failed VPC convergence woke instances: %q", got)
	}
}

func TestUnreadyInstanceIsTrackedAsWaiting(t *testing.T) {
	ctx, s, subnet := instanceTestWorld(t)
	i := testRunInstance(ctx, t, s, subnet, "waiter", "")
	network := &instanceTestNetwork{}
	runtime := &instanceTestRuntime{}
	c := NewInstances(s, network, runtime, time.Hour)
	if err := c.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	c.WakeWaiting()
	if got := c.pop(); got != i.ID {
		t.Fatalf("unready instance not woken: %q", got)
	}
	network.mu.Lock()
	network.ready = true
	network.mu.Unlock()
	if err := c.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	c.WakeWaiting()
	if got := c.pop(); got != "" {
		t.Fatalf("running instance still waiting: %q", got)
	}
}
