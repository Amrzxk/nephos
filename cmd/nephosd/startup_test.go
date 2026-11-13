package main

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Amrzxk/nephos/internal/reconcile"
)

func TestSweepUntilCompleteRetriesUntilAdmitted(t *testing.T) {
	calls := 0
	err := sweepUntilComplete(context.Background(), "test", func(context.Context) error {
		calls++
		switch calls {
		case 1:
			return reconcile.ErrNotAdmitted
		case 2:
			return fmt.Errorf("list desired VPCs: %w", errors.New("database is locked"))
		default:
			return nil
		}
	})
	if err != nil || calls != 3 {
		t.Fatalf("err=%v calls=%d, want success on third attempt", err, calls)
	}
}

func TestSweepUntilCompleteStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := sweepUntilComplete(ctx, "test", func(context.Context) error { return reconcile.ErrNotAdmitted })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v, want the context error", err)
	}
}
