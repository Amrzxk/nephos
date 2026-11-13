package lifecycle

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRegistryDrainJoinsEveryReference(t *testing.T) {
	r := NewRegistry()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	a, stopA := context.WithCancel(ctx)
	b, stopB := context.WithCancel(ctx)
	releaseA := r.Track("default", "console", "same-instance", stopA)
	releaseB := r.Track("default", "console", "same-instance", stopB)
	otherRelease := r.Track("other", "socket", "other", nil)
	defer otherRelease()
	done := make(chan error, 1)
	go func() { done <- r.Drain(ctx, "default") }()
	<-a.Done()
	<-b.Done()
	if len(r.Outstanding("default")) != 2 {
		t.Fatal("duplicate instance sessions not tracked independently")
	}
	releaseA()
	releaseA()
	select {
	case <-done:
		t.Fatal("drain returned before second session cleanup")
	default:
	}
	releaseB()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(r.Outstanding("other")) != 1 {
		t.Fatal("drain touched other workspace")
	}
	late, stopLate := context.WithCancel(ctx)
	lateRelease := r.Track("default", "socket", "late", stopLate)
	defer lateRelease()
	select {
	case <-late.Done():
	default:
		t.Fatal("late registration escaped shutdown cancellation")
	}
}

func TestRegistryDrainKeepsTimedOutReferences(t *testing.T) {
	r := NewRegistry()
	release := r.Track("default", "fd", "held", nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := r.Drain(ctx, "default"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if len(r.Outstanding("default")) != 1 {
		t.Fatal("cancellation erased a live reference")
	}
	r.ReportError("default", errors.New("reap failed"))
	release()
	if err := r.Drain(context.Background(), "default"); err == nil {
		t.Fatal("cleanup error hidden")
	}
}
