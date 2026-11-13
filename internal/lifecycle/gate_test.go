package lifecycle

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"
)

func waitExclusivePending(ctx context.Context, t *testing.T, gate *Gate) {
	t.Helper()
	for {
		lease, err := gate.Enter(ctx, "default")
		if errors.Is(err, ErrDraining) {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		_ = lease.Close()
		if ctx.Err() != nil {
			t.Fatal("exclusive admission never closed")
		}
		runtime.Gosched()
	}
}

func TestEffectBarrierWaitsForCompletion(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	gate := NewGate()
	lease, err := gate.Enter(ctx, "default")
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- gate.Exclusive(ctx, "default", func(context.Context) error { close(entered); return nil })
	}()
	waitExclusivePending(ctx, t, gate)
	// Publishing an operation result is not the same as closing its lease:
	// deferred handle/worker cleanup still owns participation in the barrier.
	select {
	case <-entered:
		t.Fatal("exclusive entered before effect cleanup")
	default:
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := gate.Enter(ctx, "default"); err != nil {
		t.Fatal(err)
	}
}

func TestStartHookChildCompletesDuringDrain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	gate := NewGate()
	parent, err := gate.Enter(ctx, "default")
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- gate.Exclusive(ctx, "default", func(context.Context) error { close(entered); return nil })
	}()
	waitExclusivePending(ctx, t, gate)
	if _, err := gate.EnterChild(ctx, &Lease{}); err == nil {
		t.Fatal("forged parent accepted")
	}
	other, _ := NewGate().Enter(ctx, "default")
	if _, err := gate.EnterChild(ctx, other); err == nil {
		t.Fatal("unrelated gate parent accepted")
	}
	child, err := gate.EnterChild(ctx, parent)
	if err != nil {
		t.Fatalf("verified child of admitted start refused: %v", err)
	}
	_ = parent.Close()
	if _, err := gate.EnterChild(ctx, parent); err == nil {
		t.Fatal("closed parent granted new work")
	}
	select {
	case <-entered:
		t.Fatal("parent completion erased live hook child")
	default:
	}
	_ = child.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestEffectBarrierCanceledWaitRestoresAdmission(t *testing.T) {
	gate := NewGate()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	lease, _ := gate.Enter(ctx, "default")
	waitCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		done <- gate.Exclusive(waitCtx, "default", func(context.Context) error { t.Error("canceled maintenance ran"); return nil })
	}()
	waitExclusivePending(ctx, t, gate)
	stop()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	second, err := gate.Enter(ctx, "default")
	if err != nil {
		t.Fatal(err)
	}
	_ = second.Close()
	_ = lease.Close()
}
