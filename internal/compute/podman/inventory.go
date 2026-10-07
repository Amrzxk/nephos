package podman

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/Amrzxk/nephos/internal/compute"
)

// Inventory observes every container, including stopped roots and candidates
// with incomplete or contradictory Nephos metadata. It never grants deletion
// permission by a list filter; mutations re-inspect the full reference.
func (c *Client) Inventory(ctx context.Context) ([]compute.ObservedContainer, error) {
	var listed []struct {
		ID     string `json:"Id"`
		Names  []string
		Labels map[string]string
	}
	if err := c.request(ctx, http.MethodGet, "/containers/json?all=true", nil, &listed); err != nil {
		return nil, fmt.Errorf("list runtime inventory: %w", inspectionFailure(err))
	}
	if listed == nil {
		return nil, fmt.Errorf("missing runtime inventory array: %w", compute.ErrInspectionUnavailable)
	}
	seen := make(map[string]bool, len(listed))
	for _, item := range listed {
		if !runtimeIDPattern.MatchString(item.ID) || seen[item.ID] {
			return nil, fmt.Errorf("invalid or duplicate runtime inventory ID %q: %w", item.ID, compute.ErrInspectionUnavailable)
		}
		seen[item.ID] = true
	}
	observed := make([]compute.ObservedContainer, 0)
	for _, item := range listed {
		var doc containerInspect
		if err := c.request(ctx, http.MethodGet, "/containers/"+item.ID+"/json", nil, &doc); err != nil {
			return nil, fmt.Errorf("inspect inventory container %s: %w", item.ID, inspectionFailure(err))
		}
		if doc.ID != item.ID {
			return nil, fmt.Errorf("inventory container identity changed for %s: %w", item.ID, compute.ErrInspectionUnavailable)
		}
		listMarked := false
		for _, name := range item.Names {
			listMarked = listMarked || strings.HasPrefix(name, "i-")
		}
		for key := range item.Labels {
			listMarked = listMarked || strings.HasPrefix(key, "io.nephos.")
		}
		if !listMarked && !markedContainer(doc) {
			continue
		}
		candidate := compute.ObservedContainer{
			Name: doc.Name,
			Reference: compute.Reference{ID: compute.RuntimeID(doc.ID), Identity: compute.Identity{
				WorkspaceID: doc.Config.Labels["io.nephos.workspace-id"], InstanceID: doc.Config.Labels[instanceLabel],
			}},
		}
		if err := owned(doc); err != nil {
			candidate.OwnershipError = err.Error()
		} else {
			candidate.Managed = true
		}
		observed = append(observed, candidate)
	}
	slices.SortFunc(observed, func(a, b compute.ObservedContainer) int {
		return strings.Compare(string(a.Reference.ID), string(b.Reference.ID))
	})
	return observed, nil
}

func inspectionFailure(err error) error {
	return fmt.Errorf("%w: %w", compute.ErrInspectionUnavailable, err)
}

func markedContainer(doc containerInspect) bool {
	if strings.HasPrefix(doc.Name, "i-") {
		return true
	}
	for _, markers := range []map[string]string{doc.Config.Labels, doc.Config.Annotations} {
		for key := range markers {
			if strings.HasPrefix(key, "io.nephos.") {
				return true
			}
		}
	}
	return false
}
