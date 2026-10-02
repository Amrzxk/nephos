package main

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/Amrzxk/nephos/pkg/client"
)

func resolveInstance(ctx context.Context, api *client.ClientWithResponses, reference string) (string, error) {
	if looksLikeID(reference, "i-") {
		response, err := api.GetInstanceWithResponse(ctx, "default", reference)
		if err != nil {
			return "", fmt.Errorf("resolve instance ID: %w", err)
		}
		if response.JSON200 != nil {
			return reference, nil
		}
		if response.StatusCode() != http.StatusNotFound {
			return "", responseError("resolve instance ID", response)
		}
	}
	var token *string
	seen := make(map[string]struct{})
	match := ""
	for {
		limit := 100
		response, err := api.ListInstancesWithResponse(ctx, "default", &client.ListInstancesParams{Limit: &limit, PageToken: token})
		if err != nil {
			return "", fmt.Errorf("resolve instance name: %w", err)
		}
		if response.JSON200 == nil {
			return "", responseError("resolve instance name", response)
		}
		for i := range response.JSON200.Items {
			item := &response.JSON200.Items[i]
			if item.Name == reference {
				if match != "" {
					return "", fmt.Errorf("instance name %q is ambiguous", reference)
				}
				match = item.Id
			}
		}
		token = response.JSON200.NextPageToken
		if token == nil || *token == "" {
			break
		}
		if _, repeated := seen[*token]; repeated {
			return "", fmt.Errorf("instance list repeated a page token")
		}
		seen[*token] = struct{}{}
	}
	if match == "" {
		return "", fmt.Errorf("instance name %q not found", reference)
	}
	return match, nil
}

func waitInstance(ctx context.Context, api *client.ClientWithResponses, id string, deleting bool, timeout, interval time.Duration) (client.Instance, error) {
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		if waitCtx.Err() != nil {
			return client.Instance{}, waitError(ctx, waitCtx, id, timeout)
		}
		response, err := api.GetInstanceWithResponse(waitCtx, "default", id)
		if err == nil {
			if deleting && response.StatusCode() == http.StatusNotFound {
				return client.Instance{}, nil
			}
			if response.JSON200 == nil {
				return client.Instance{}, responseError("wait for instance", response)
			}
			instance := *response.JSON200
			if instance.State == client.InstanceStateFailed {
				return client.Instance{}, fmt.Errorf("instance %s failed: %s", id, instance.StateReason)
			}
			if !deleting && instance.State == client.InstanceStateRunning {
				return instance, nil
			}
		}
		if err := pollPause(waitCtx, interval); err != nil {
			return client.Instance{}, waitError(ctx, waitCtx, id, timeout)
		}
	}
}
