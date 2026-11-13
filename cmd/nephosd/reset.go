package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/Amrzxk/nephos/internal/compute"
	"github.com/Amrzxk/nephos/internal/leakcheck"
	"github.com/Amrzxk/nephos/internal/lifecycle"
	"github.com/Amrzxk/nephos/internal/model"
	"github.com/Amrzxk/nephos/internal/reconcile"
	"github.com/Amrzxk/nephos/internal/store"
)

// resetVerifier is the soft reset's last step: under exclusive effect
// admission it collects owned orphans, then runs the leak checker.
type resetVerifier func(ctx context.Context, workspace string) (model.LeakResult, error)

func (v resetVerifier) Verify(ctx context.Context, workspace string) (model.LeakResult, error) {
	return v(ctx, workspace)
}

// newResetVerifier binds cleanup work to the daemon lifetime, not to the
// request: a caller's timeout may stop the wait for exclusive admission, but
// it never cancels orphan collection halfway. Like periodic maintenance, a
// collection failure after admission seals the gate and stops the daemon.
func newResetVerifier(lifetime context.Context, gate *lifecycle.Gate, s *store.Store, runtime compute.Runtime, network reconcile.OrphanNetwork,
	checker *leakcheck.Checker, failure chan<- error) resetVerifier {
	return func(ctx context.Context, workspace string) (model.LeakResult, error) {
		wait := 60 * time.Second
		if deadline, ok := ctx.Deadline(); ok {
			wait = time.Until(deadline)
		}
		if wait <= 0 {
			return model.LeakResult{}, context.DeadlineExceeded
		}
		var result model.LeakResult
		ran, err := orphanMaintenanceCycle(lifetime, gate, wait, false, func(admitted context.Context) error {
			if err := reconcile.CollectOrphans(admitted, s, runtime, network); err != nil {
				return fmt.Errorf("reset orphan collection: %w", err)
			}
			var checkErr error
			result, checkErr = checker.Check(admitted, workspace)
			if checkErr != nil {
				slog.Warn("reset leak check was incomplete", slog.String("workspace", workspace), slog.Any("error", checkErr))
			}
			return nil
		})
		if err != nil && ran {
			select {
			case failure <- err:
			default:
			}
		}
		if err != nil {
			return model.LeakResult{}, err
		}
		return result, nil
	}
}
