package reconcile

import (
	"context"
	"fmt"

	"github.com/Amrzxk/nephos/internal/model"
	"github.com/Amrzxk/nephos/internal/network/netns"
)

// Sweep converges persisted VPCs. Strict orphan collection is separate trusted
// maintenance under exclusivity, never name-only deletion inside this lease.
// Individual engine failures are recorded and retried, not
// returned as startup-fatal errors.
func (c *Controller) Sweep(ctx context.Context) error {
	ctx, done, admitted, err := enterEffect(ctx, c.gate)
	if err != nil {
		return err
	}
	if !admitted {
		return nil
	}
	defer done()
	c.workMu.Lock()
	defer c.workMu.Unlock()
	vpcs := make([]model.VPC, 0)
	after := ""
	for {
		page, err := c.store.ListVPCPage(ctx, "default", after, 100)
		if err != nil {
			return err
		}
		if len(page) == 0 {
			break
		}
		for i := range page {
			vpc := &page[i]
			_, err := netns.Name(vpc.ShortIndex)
			if err != nil {
				return fmt.Errorf("VPC %s namespace name: %w", vpc.ID, err)
			}
			vpcs = append(vpcs, *vpc)
		}
		after = page[len(page)-1].ID
	}
	for i := range vpcs {
		vpc := &vpcs[i]
		if err := c.reconcileLocked(ctx, vpc.ID); err != nil {
			return err
		}
	}
	return nil
}
