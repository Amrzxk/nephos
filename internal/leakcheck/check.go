package leakcheck

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/Amrzxk/nephos/internal/compute"
	"github.com/Amrzxk/nephos/internal/lifecycle"
	"github.com/Amrzxk/nephos/internal/network/topology"
	"github.com/Amrzxk/nephos/internal/store"
)

// DesiredInventory releases a single consistent SQL snapshot before effects.
type DesiredInventory interface {
	DesiredSnapshot(context.Context, string) (store.DesiredState, error)
}

// RuntimeInventory includes every marked container, including ambiguous ones.
type RuntimeInventory interface {
	Inventory(context.Context) ([]compute.ObservedContainer, error)
}

// NetworkInventory reports marked kernel objects and required inspection errors.
type NetworkInventory interface {
	Inventory(context.Context) (topology.Inventory, error)
}

// ProcessInventory independently corroborates previously observed identities.
type ProcessInventory interface {
	Observe(context.Context, []lifecycle.Reference) ([]Finding, error)
}

// Checker never mutates anything. Its success decision requires a caller which
// actually owns the exclusive barrier; a diagnostic bit cannot grant authority.
type Checker struct {
	Store     DesiredInventory
	Runtime   RuntimeInventory
	Network   NetworkInventory
	Registry  *lifecycle.Registry
	Gate      *lifecycle.Gate
	Processes ProcessInventory
}

// Check rejects partial observations, outstanding references and cleanup
// uncertainty. Foreign unmarked objects and control-plane listeners are exempt.
func (c *Checker) Check(ctx context.Context, workspace string) (Result, error) {
	result := Result{Findings: []Finding{}, InspectionErrors: []string{}}
	if workspace != "default" {
		return result, fmt.Errorf("unsupported leak-check workspace %q", workspace)
	}
	var errs []error
	failure := func(area string, err error) {
		wrapped := fmt.Errorf("%s: %w", area, err)
		errs = append(errs, wrapped)
		result.InspectionErrors = append(result.InspectionErrors, wrapped.Error())
	}
	if c.Store == nil || c.Runtime == nil || c.Network == nil || c.Registry == nil || c.Gate == nil || c.Processes == nil {
		failure("leak checker", errors.New("required observation adapter missing"))
		return result, errors.Join(errs...)
	}
	desired, err := c.Store.DesiredSnapshot(ctx, workspace)
	if err != nil {
		failure("desired resources and leases", err)
	} else {
		for _, count := range []struct {
			kind string
			n    int
		}{{"vpc", len(desired.VPCs)}, {"subnet", len(desired.Subnets)}, {"instance", len(desired.Instances)}, {"eni-lease", len(desired.ENIs)}} {
			if count.n != 0 {
				result.Findings = append(result.Findings, Finding{Kind: count.kind, Identity: workspace, Reason: fmt.Sprintf("%d desired resources remain", count.n)})
			}
		}
	}
	containers, err := c.Runtime.Inventory(ctx)
	if err != nil {
		failure("runtime", err)
	} else {
		for _, container := range containers {
			reason := "marked runtime root remains"
			if container.OwnershipError != "" {
				reason = container.OwnershipError
			}
			result.Findings = append(result.Findings, Finding{Kind: "runtime", Identity: string(container.Reference.ID), Reason: reason})
		}
	}
	network, err := c.Network.Inventory(ctx)
	if err != nil {
		failure("network/nftables/netlink", err)
	} else {
		for _, ns := range network.Namespaces {
			if ns.Owned || ns.OwnershipError != "" {
				reason := "owned named namespace remains"
				if ns.OwnershipError != "" {
					reason = ns.OwnershipError
				}
				result.Findings = append(result.Findings, Finding{Kind: "namespace", Identity: ns.Name, Reason: reason})
			}
		}
		for _, finding := range network.Findings {
			result.Findings = append(result.Findings, Finding{Kind: finding.Kind, Identity: finding.Identity, Reason: finding.Reason})
		}
	}
	processes, err := c.Processes.Observe(ctx, c.Registry.Known(workspace))
	if err != nil {
		failure("process/cgroup/namespace corroboration", err)
	} else {
		result.Findings = append(result.Findings, processes...)
	}
	for _, ref := range c.Registry.Outstanding(workspace) {
		result.Findings = append(result.Findings, Finding{Kind: ref.Kind, Identity: ref.Identity, Reason: "product reference has not completed actual cleanup"})
	}
	for _, err := range c.Registry.Errors(workspace) {
		failure("registered cleanup", err)
	}
	if count := c.Gate.Outstanding(workspace); count != 0 {
		result.Findings = append(result.Findings, Finding{Kind: "effect", Identity: workspace, Reason: fmt.Sprintf("%d effects remain", count)})
	}
	if !c.Gate.HoldsExclusive(ctx, workspace) {
		result.Findings = append(result.Findings, Finding{Kind: "barrier", Identity: workspace, Reason: "caller must hold exclusive drained admission"})
	}
	if err := ctx.Err(); err != nil {
		failure("check context", err)
	}
	sort.Slice(result.Findings, func(i, j int) bool {
		a, b := result.Findings[i], result.Findings[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Identity != b.Identity {
			return a.Identity < b.Identity
		}
		return a.Reason < b.Reason
	})
	result.Complete = len(errs) == 0
	result.Clean = result.Complete && len(result.Findings) == 0
	return result, errors.Join(errs...)
}
