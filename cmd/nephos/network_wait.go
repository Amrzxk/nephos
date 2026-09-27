package main

import (
	"context"
	"fmt"
	"time"

	"github.com/Amrzxk/nephos/pkg/client"
)

func pollPause(ctx context.Context, interval time.Duration) error {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func waitError(parent, waitCtx context.Context, id string, timeout time.Duration) error {
	if parent.Err() != nil {
		return fmt.Errorf("waiting for %s: %w", id, parent.Err())
	}
	if waitCtx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("waiting for %s timed out after %s", id, timeout)
	}
	return fmt.Errorf("waiting for %s: %w", id, waitCtx.Err())
}

func waitVPC(ctx context.Context, api *client.ClientWithResponses, id string, deleting bool, timeout, interval time.Duration) (client.Vpc, error) {
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		if waitCtx.Err() != nil {
			return client.Vpc{}, waitError(ctx, waitCtx, id, timeout)
		}
		response, err := api.GetVpcWithResponse(waitCtx, "default", id)
		if err == nil {
			if deleting && response.StatusCode() == 404 {
				return client.Vpc{}, nil
			}
			if response.JSON200 == nil {
				return client.Vpc{}, responseError("wait for VPC", response)
			}
			vpc := *response.JSON200
			if vpc.State == client.VpcStateFailed {
				return client.Vpc{}, fmt.Errorf("VPC %s failed: %s", id, vpc.StateReason)
			}
			if !deleting && vpc.State == client.VpcStateAvailable {
				return vpc, nil
			}
		}
		if err := pollPause(waitCtx, interval); err != nil {
			return client.Vpc{}, waitError(ctx, waitCtx, id, timeout)
		}
	}
}

func waitSubnet(ctx context.Context, api *client.ClientWithResponses, id string, deleting bool, timeout, interval time.Duration) (client.Subnet, error) {
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		if waitCtx.Err() != nil {
			return client.Subnet{}, waitError(ctx, waitCtx, id, timeout)
		}
		response, err := api.GetSubnetWithResponse(waitCtx, "default", id)
		if err == nil {
			if deleting && response.StatusCode() == 404 {
				return client.Subnet{}, nil
			}
			if response.JSON200 == nil {
				return client.Subnet{}, responseError("wait for subnet", response)
			}
			subnet := *response.JSON200
			if subnet.State == client.SubnetStateFailed {
				return client.Subnet{}, fmt.Errorf("subnet %s failed: %s", id, subnet.StateReason)
			}
			if !deleting && subnet.State == client.SubnetStateAvailable {
				return subnet, nil
			}
		}
		if err := pollPause(waitCtx, interval); err != nil {
			return client.Subnet{}, waitError(ctx, waitCtx, id, timeout)
		}
	}
}
