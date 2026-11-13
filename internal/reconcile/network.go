package reconcile

import (
	"context"
	"fmt"

	"github.com/Amrzxk/nephos/internal/model"
	"github.com/Amrzxk/nephos/internal/network/netns"
)

// Sweep converges persisted VPCs. Strict orphan collection is separate trusted
// maintenance under exclusivity, never name-only deletion inside this lease.
// Individual VPC failures are recorded and retried, not returned. It returns
// ErrNotAdmitted when the gate is draining, a listing error when the desired
// set could not be read, and only invariant violations as fatal errors.
func (c *Controller) Sweep(ctx context.Context) error {
	ctx, done, admitted, err := enterEffect(ctx, c.gate)
	if err != nil {
		return err
	}
	if !admitted {
		return ErrNotAdmitted
	}
	defer done()
	c.workMu.Lock()
	defer c.workMu.Unlock()
	vpcs := make([]model.VPC, 0)
	after := ""
	for {
		page, err := c.store.ListVPCPage(ctx, "default", after, 100)
		if err != nil {
			return fmt.Errorf("list desired VPCs: %w", err)
		}
		if len(page) == 0 {
			break
		}
		for i := range page {
			vpc := &page[i]
			_, err := netns.Name(vpc.ShortIndex)
			if err != nil {
				return fmt.Errorf("%w: VPC %s namespace name: %w", errInvariant, vpc.ID, err)
			}
			vpcs = append(vpcs, *vpc)
		}
		after = page[len(page)-1].ID
	}
	for i := range vpcs {
		vpc := &vpcs[i]
		// One VPC's transient failure is retried on its own; it must not stop
		// the sweep from converging every other VPC.
		if err := c.handle(ctx, vpc.ID, c.reconcileLocked(ctx, vpc.ID)); err != nil {
			return err
		}
	}
	return nil
}
