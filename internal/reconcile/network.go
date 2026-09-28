package reconcile

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/Amrzxk/nephos/internal/model"
	"github.com/Amrzxk/nephos/internal/network/netns"
)

// Sweep removes only orphaned Nephos VPC namespaces and then converges every
// persisted VPC. Individual engine failures are recorded and retried, not
// returned as startup-fatal errors.
func (c *Controller) Sweep(ctx context.Context) error {
	c.workMu.Lock()
	defer c.workMu.Unlock()
	vpcs := make([]model.VPC, 0)
	wanted := make(map[string]struct{})
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
			name, err := netns.Name(vpc.ShortIndex)
			if err != nil {
				return fmt.Errorf("VPC %s namespace name: %w", vpc.ID, err)
			}
			wanted[name] = struct{}{}
			vpcs = append(vpcs, *vpc)
		}
		after = page[len(page)-1].ID
	}
	names, err := c.network.ListVPCNames(ctx)
	if err != nil {
		return fmt.Errorf("list VPC namespace orphans: %w", err)
	}
	for _, name := range names {
		if _, desired := wanted[name]; desired {
			continue
		}
		index, valid := ownedIndex(name)
		if !valid {
			continue
		}
		if err := c.network.DeleteVPC(ctx, model.VPC{ID: name, ShortIndex: index}); err != nil {
			return fmt.Errorf("remove orphan VPC namespace %s: %w", name, err)
		}
	}
	for i := range vpcs {
		vpc := &vpcs[i]
		if err := c.reconcileLocked(ctx, vpc.ID); err != nil {
			return err
		}
	}
	return nil
}

func ownedIndex(name string) (int64, bool) {
	part, ok := strings.CutPrefix(name, "nx-vpc-")
	if !ok {
		return 0, false
	}
	index, err := strconv.ParseInt(part, 10, 64)
	if err != nil {
		return 0, false
	}
	canonical, err := netns.Name(index)
	return index, err == nil && canonical == name
}
