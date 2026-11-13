package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/Amrzxk/nephos/internal/reconcile"
)

const (
	startupSweepBackoffBase = 100 * time.Millisecond
	startupSweepBackoffMax  = 5 * time.Second
)

// sweepUntilComplete runs one startup sweep to completion before readiness.
// A sweep that was not admitted (the gate is draining) or could not read its
// desired set is retried with backoff: readiness must mean that every
// persisted resource was actually visited, not that a sweep was skipped.
// Only cancellation and invariant violations end the wait with an error.
func sweepUntilComplete(ctx context.Context, name string, sweep func(context.Context) error) error {
	delay := startupSweepBackoffBase
	for {
		err := sweep(ctx)
		if err == nil {
			return nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if !errors.Is(err, reconcile.ErrNotAdmitted) && reconcile.IsFatal(err) {
			return fmt.Errorf("initial %s sweep: %w", name, err)
		}
		slog.Warn("initial reconcile sweep incomplete; retrying before ready",
			slog.String("sweep", name), slog.Duration("retry_in", delay), slog.Any("error", err))
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		delay = min(delay*2, startupSweepBackoffMax)
	}
}
