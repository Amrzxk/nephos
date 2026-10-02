package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/Amrzxk/nephos/internal/compute"
	"github.com/Amrzxk/nephos/internal/model"
	"github.com/Amrzxk/nephos/internal/network/netns"
	"github.com/Amrzxk/nephos/internal/store"
)

// instanceNetwork reads observed prerequisites from SQLite and delegates only
// owned ENI teardown to the network engine.
type instanceNetwork struct {
	store  *store.Store
	engine networkEngine
}

func (n instanceNetwork) Ready(ctx context.Context, instance model.Instance) (bool, error) {
	subnet, err := n.store.GetSubnet(ctx, instance.WorkspaceID, instance.SubnetID)
	if err != nil {
		return false, err
	}
	vpc, err := n.store.GetVPC(ctx, instance.WorkspaceID, subnet.VPCID)
	if err != nil {
		return false, err
	}
	return !subnet.DeletionRequested && !vpc.DeletionRequested &&
		subnet.State == model.StateAvailable && vpc.State == model.StateAvailable &&
		subnet.ObservedGeneration == subnet.Generation && vpc.ObservedGeneration == vpc.Generation, nil
}

func (n instanceNetwork) DeleteENI(ctx context.Context, vpc model.VPC, eni model.ENI) error {
	return n.engine.DeleteENI(ctx, vpc, eni)
}

func (n instanceNetwork) EnsureRunning(ctx context.Context, instance model.Instance, ref compute.Reference, status compute.Status) error {
	if !status.Running || ref.Identity != (compute.Identity{WorkspaceID: instance.WorkspaceID, InstanceID: instance.ID}) || string(ref.ID) != instance.RuntimeID {
		return fmt.Errorf("instance %s running identity is not verified", instance.ID)
	}
	subnet, err := n.store.GetSubnet(ctx, instance.WorkspaceID, instance.SubnetID)
	if err != nil {
		return err
	}
	vpc, err := n.store.GetVPC(ctx, instance.WorkspaceID, subnet.VPCID)
	if err != nil {
		return err
	}
	if subnet.DeletionRequested || vpc.DeletionRequested {
		return fmt.Errorf("instance %s network is deleting", instance.ID)
	}
	target, err := netns.OpenInstance(ctx, status.PID, string(ref.ID))
	if err != nil {
		return fmt.Errorf("pin running instance %s: %w", instance.ID, err)
	}
	err = n.engine.EnsureENI(ctx, vpc, subnet, instance.ENI, target)
	return errors.Join(err, target.Close())
}
