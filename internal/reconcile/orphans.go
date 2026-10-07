package reconcile

import (
	"context"
	"fmt"

	"github.com/Amrzxk/nephos/internal/compute"
	"github.com/Amrzxk/nephos/internal/model"
)

// collectRuntimeOrphans is trusted maintenance: its caller must hold the
// exclusive effect gate and supply a consistent desired-state snapshot.
func collectRuntimeOrphans(ctx context.Context, desired []model.Instance, runtime compute.Runtime) error {
	items, err := runtime.Inventory(ctx)
	if err != nil {
		return fmt.Errorf("observe runtime orphans: %w", err)
	}
	wanted := make(map[compute.Identity]bool, len(desired))
	for i := range desired {
		instance := &desired[i]
		wanted[compute.Identity{WorkspaceID: instance.WorkspaceID, InstanceID: instance.ID}] = true
	}
	seen := make(map[compute.Identity]bool, len(items))
	ids := make(map[compute.RuntimeID]bool, len(items))
	// Prevalidate the entire candidate set before deleting even one root.
	for _, item := range items {
		ref := item.Reference
		if !item.Managed || item.OwnershipError != "" || ref.Identity.WorkspaceID != "default" ||
			item.Name != ref.Identity.InstanceID || ref.ID == "" || seen[ref.Identity] || ids[ref.ID] {
			return fmt.Errorf("ambiguous runtime candidate %s (%q): %s", ref.ID, item.Name, item.OwnershipError)
		}
		seen[ref.Identity], ids[ref.ID] = true, true
	}
	for _, item := range items {
		if wanted[item.Reference.Identity] {
			continue // Pending and stale-cache owners are still desired owners.
		}
		// The adapter re-inspects the immutable ID and every ownership field
		// immediately before removal; observation alone is not mutation proof.
		if err := runtime.Delete(ctx, item.Reference); err != nil {
			return fmt.Errorf("remove runtime orphan %s: %w", item.Reference.ID, err)
		}
	}
	return nil
}
