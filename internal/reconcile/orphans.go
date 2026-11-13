package reconcile

import (
	"context"
	"fmt"

	"github.com/Amrzxk/nephos/internal/compute"
	"github.com/Amrzxk/nephos/internal/model"
	"github.com/Amrzxk/nephos/internal/network/topology"
	"github.com/Amrzxk/nephos/internal/store"
)

// OrphanNetwork keeps ownership and destructive scope inside the engine.
type OrphanNetwork interface {
	Inventory(context.Context) (topology.Inventory, error)
	CollectOrphans(context.Context, []model.VPC, []model.Subnet, []model.ENI) error
}

// CollectOrphans requires the caller's exclusive effect lease. Take one
// SQLite snapshot under that lease, release SQL, prevalidate both engines,
// then clean network children before their retained runtime parents.
func CollectOrphans(ctx context.Context, s *store.Store, runtime compute.Runtime, network OrphanNetwork) error {
	desired, err := s.DesiredSnapshot(ctx, "default")
	if err != nil {
		return err
	}
	items, err := runtime.Inventory(ctx)
	if err != nil {
		return fmt.Errorf("observe runtime orphans: %w", err)
	}
	if err := validateRuntimeCandidates(items); err != nil {
		return err
	}
	observed, err := network.Inventory(ctx)
	if err != nil {
		return fmt.Errorf("observe network orphans: %w", err)
	}
	for _, ns := range observed.Namespaces {
		if ns.OwnershipError != "" {
			return fmt.Errorf("ambiguous namespace %s: %s", ns.Name, ns.OwnershipError)
		}
	}
	if err := network.CollectOrphans(ctx, desired.VPCs, desired.Subnets, desired.ENIs); err != nil {
		return fmt.Errorf("collect network orphans: %w", err)
	}
	return collectRuntimeOrphans(ctx, desired.Instances, runtime)
}

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
	if err := validateRuntimeCandidates(items); err != nil {
		return err
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

func validateRuntimeCandidates(items []compute.ObservedContainer) error {
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
	return nil
}
