package lifecycle

import (
	"context"
	"errors"
	"testing"
)

func TestTryExclusiveRunsOnlyWhenIdle(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name    string
		prepare func(*Gate) func()
		wantRan bool
	}{
		{"idle", func(*Gate) func() { return func() {} }, true},
		{"admitted effect", func(g *Gate) func() {
			lease, err := g.Enter(ctx, "default")
			if err != nil {
				t.Fatal(err)
			}
			return func() { _ = lease.Close() }
		}, false},
		{"sealed", func(g *Gate) func() { g.Seal("default"); return func() {} }, false},
		{"other workspace busy", func(g *Gate) func() {
			lease, err := g.Enter(ctx, "other")
			if err != nil {
				t.Fatal(err)
			}
			return func() { _ = lease.Close() }
		}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewGate()
			done := tt.prepare(g)
			defer done()
			called := false
			ran, err := g.TryExclusive(ctx, "default", func(admitted context.Context) error {
				called = true
				if !g.HoldsExclusive(admitted, "default") {
					t.Error("TryExclusive callback lacks exclusive authority")
				}
				if _, err := g.Enter(ctx, "default"); !errors.Is(err, ErrDraining) {
					t.Errorf("admission open during exclusive maintenance: %v", err)
				}
				return nil
			})
			if err != nil || ran != tt.wantRan || called != tt.wantRan {
				t.Fatalf("ran=%v called=%v err=%v, want ran=%v", ran, called, err, tt.wantRan)
			}
		})
	}
}

func TestSkippedTryExclusiveNeverBlocksAdmission(t *testing.T) {
	ctx := context.Background()
	g := NewGate()
	console, err := g.Enter(ctx, "default")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = console.Close() }()
	if ran, err := g.TryExclusive(ctx, "default", func(context.Context) error { return nil }); ran || err != nil {
		t.Fatalf("busy TryExclusive ran=%v err=%v", ran, err)
	}
	// Unlike a pending Exclusive waiter, a skipped TryExclusive leaves
	// admission open: new consoles and reconciles still enter.
	second, err := g.Enter(ctx, "default")
	if err != nil {
		t.Fatalf("admission closed after skipped maintenance: %v", err)
	}
	_ = second.Close()
}

func TestTryExclusiveReturnsCallbackErrorAndReleases(t *testing.T) {
	ctx := context.Background()
	g := NewGate()
	boom := errors.New("cleanup failed")
	ran, err := g.TryExclusive(ctx, "default", func(context.Context) error { return boom })
	if !ran || !errors.Is(err, boom) {
		t.Fatalf("ran=%v err=%v", ran, err)
	}
	lease, err := g.Enter(ctx, "default")
	if err != nil {
		t.Fatalf("exclusivity not released after callback: %v", err)
	}
	_ = lease.Close()
}
