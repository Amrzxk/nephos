package topology

import (
	"context"
	"fmt"
	"strings"

	"github.com/vishvananda/netlink"

	"github.com/Amrzxk/nephos/internal/model"
	"github.com/Amrzxk/nephos/internal/network/netns"
)

// CollectOrphans is trusted maintenance. Its caller must hold the exclusive
// effect barrier and supply one complete desired snapshot, including ENIs.
func (e *Engine) CollectOrphans(ctx context.Context, desired []model.VPC, subnets []model.Subnet, enis []model.ENI) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	owners, err := desiredENIOwners(desired, subnets, enis)
	if err != nil {
		return err
	}
	wanted := make(map[string]bool)
	for i := range desired {
		vpc := &desired[i]
		name, err := netns.Name(vpc.ShortIndex)
		if err != nil {
			return err
		}
		if wanted[name] {
			return fmt.Errorf("duplicate desired VPC namespace %s", name)
		}
		wanted[name] = true
	}
	observed, err := inventory(ctx)
	if err != nil {
		return err
	}
	for _, finding := range observed.Findings {
		if strings.HasPrefix(finding.Identity, "appliance-root/") {
			return fmt.Errorf("ambiguous root marker %s: %s", finding.Identity, finding.Reason)
		}
	}
	// Validate every candidate before any deletion, including peer proof. An
	// unrelated malformed marked object cannot turn a partial scan into clean.
	for _, ns := range observed.Namespaces {
		if !ns.Owned && ns.OwnershipError == "" {
			continue
		}
		if !ns.Owned {
			return fmt.Errorf("ambiguous namespace %s: %s", ns.Name, ns.OwnershipError)
		}
		if err := netns.WithIdentityHandle(ctx, ns.Name, ns.Inode, func(h *netlink.Handle) error {
			_, err := orphanLinks(ctx, ns.Name, h, owners, false)
			if err != nil {
				return err
			}
			if !wanted[ns.Name] {
				return validateOrphanRouter(ctx, h, false)
			}
			return nil
		}); err != nil {
			return fmt.Errorf("validate orphan namespace %s: %w", ns.Name, err)
		}
	}
	for _, ns := range observed.Namespaces {
		if !ns.Owned && ns.OwnershipError == "" {
			continue
		}
		if err := netns.WithIdentityHandle(ctx, ns.Name, ns.Inode, func(h *netlink.Handle) error {
			changed, err := orphanLinks(ctx, ns.Name, h, owners, true)
			if err != nil {
				return err
			}
			if changed {
				if err := applyENISourceChecks(ctx, h); err != nil {
					return err
				}
			}
			if !wanted[ns.Name] {
				return validateOrphanRouter(ctx, h, true)
			}
			return nil
		}); err != nil {
			return fmt.Errorf("collect namespace %s children: %w", ns.Name, err)
		}
		if !wanted[ns.Name] {
			if err := netns.DeleteIdentity(ctx, ns.Name, ns.Inode); err != nil {
				return err
			}
		}
	}
	return nil
}

func orphanLinks(ctx context.Context, namespace string, h *netlink.Handle, owners map[string]eniOwner, remove bool) (bool, error) {
	links, err := h.LinkList()
	if err != nil {
		return false, fmt.Errorf("inspect ENI candidates: %w", err)
	}
	seen := make(map[string]bool)
	records := make(map[string]eniIdentity)
	var candidates []netlink.Link
	for _, link := range links {
		if !strings.HasPrefix(link.Attrs().Alias, "nephos:") {
			continue
		}
		record, err := parseENIAlias(link.Attrs().Alias)
		if err != nil {
			return false, err
		}
		if !canonicalENILink(link.Attrs().Name) || link.Type() != "veth" || seen[record.ID] {
			return false, fmt.Errorf("ambiguous marked link %s", link.Attrs().Name)
		}
		seen[record.ID] = true
		records[link.Attrs().Name] = record
		if owner, ok := owners[record.ID]; ok {
			name, err := eniLinkName(owner.eni.ShortIndex)
			if err != nil {
				return false, err
			}
			if owner.namespace != namespace || name != link.Attrs().Name || owner.eni.PrivateIP != record.IP || owner.eni.MACAddress != record.MAC {
				return false, fmt.Errorf("desired ENI %s scope/IP/MAC/index mismatch", record.ID)
			}
			continue // Recovery may rebind a desired ENI's obsolete inode marker.
		}
		candidates = append(candidates, link)
	}
	policy, exists, err := inspectENISourcePolicy(ctx)
	if err != nil {
		return false, err
	}
	if len(candidates) > 0 && exists && !ownedENIPolicyShape(policy) {
		return false, fmt.Errorf("ambiguous owned source-check table contents")
	}
	for _, link := range candidates {
		record := records[link.Attrs().Name]
		if err := withProvenOrphan(ctx, h, link, record, func() error {
			if !remove {
				return nil
			}
			return h.LinkDel(link)
		}); err != nil {
			return false, fmt.Errorf("prove orphan ENI %s: %w", link.Attrs().Name, err)
		}
	}
	return remove && len(candidates) > 0, nil
}
