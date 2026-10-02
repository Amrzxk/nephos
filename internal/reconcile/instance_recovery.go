package reconcile

import (
	"context"
	"errors"
	"fmt"

	"github.com/Amrzxk/nephos/internal/compute"
	"github.com/Amrzxk/nephos/internal/model"
)

func (c *InstanceController) lookupOrCreate(ctx context.Context, instance model.Instance) (compute.CreateResult, error) {
	ref, err := c.runtime.Lookup(ctx, runtimeReference(instance).Identity)
	if err == nil {
		return compute.CreateResult{Reference: ref}, nil
	}
	if !errors.Is(err, compute.ErrNotFound) {
		return compute.CreateResult{}, fmt.Errorf("discover retained root %s: %w", instance.ID, err)
	}
	if instance.Provisioned {
		return compute.CreateResult{}, fmt.Errorf("retained root for provisioned instance %s is missing: %w", instance.ID, err)
	}
	// Before first observed running, an authoritative absence permits retry.
	// A nonempty legacy cache still reaches discovery first, never ID mutation.
	if err := c.runtime.EnsureImage(ctx, "nephos-ubuntu:dev"); err != nil {
		return compute.CreateResult{}, err
	}
	return c.runtime.Create(ctx, instance)
}
