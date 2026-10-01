package main

import (
	"context"

	"github.com/Amrzxk/nephos/internal/model"
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
