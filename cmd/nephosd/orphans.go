package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/Amrzxk/nephos/internal/compute"
	"github.com/Amrzxk/nephos/internal/lifecycle"
	"github.com/Amrzxk/nephos/internal/reconcile"
	"github.com/Amrzxk/nephos/internal/store"
)

// orphanMaintenanceCycle runs fn under exclusive effect admission. Startup
// (skipBusy=false) waits up to wait for every admitted effect to finish.
// Periodic maintenance (skipBusy=true) runs only if the workspace is idle at
// that moment: it never queues as an exclusive waiter, so an open console is
// neither canceled nor causes new consoles and reconciles to be refused while
// maintenance waits. Failure after admission is systemic and seals the gate.
func orphanMaintenanceCycle(ctx context.Context, gate *lifecycle.Gate, wait time.Duration, skipBusy bool, fn func(context.Context) error) (bool, error) {
	work := func(admitted context.Context) error {
		// Waiting and effect budgets are separate. Preserve the live exclusive
		// token, but bind work cancellation to the daemon, not the expired wait.
		workCtx, stop := context.WithTimeout(context.WithoutCancel(admitted), 60*time.Second)
		defer stop()
		stopPropagation := context.AfterFunc(ctx, stop)
		defer stopPropagation()
		if err := fn(workCtx); err != nil {
			gate.Seal("default") // Do not reopen effects after uncertain cleanup.
			return err
		}
		return nil
	}
	if skipBusy {
		return gate.TryExclusive(ctx, "default", work)
	}
	waitCtx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	ran := false
	err := gate.Exclusive(waitCtx, "default", func(admitted context.Context) error {
		ran = true
		return work(admitted)
	})
	return ran, err
}

func runOrphanMaintenance(ctx context.Context, gate *lifecycle.Gate, s *store.Store, runtime compute.Runtime, network reconcile.OrphanNetwork, ready chan<- struct{}) error {
	collect := func(ctx context.Context) error { return reconcile.CollectOrphans(ctx, s, runtime, network) }
	if _, err := orphanMaintenanceCycle(ctx, gate, 60*time.Second, false, collect); err != nil {
		return err
	}
	close(ready)
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			ran, err := orphanMaintenanceCycle(ctx, gate, 0, true, collect)
			if err != nil {
				return err
			}
			if !ran {
				slog.Debug("orphan maintenance deferred for active effects", slog.String("workspace", "default"))
			}
		}
	}
}
