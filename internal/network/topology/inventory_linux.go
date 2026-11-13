package topology

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/vishvananda/netlink"

	"github.com/Amrzxk/nephos/internal/model"
	"github.com/Amrzxk/nephos/internal/network/netns"
)

// Finding describes an observed marked object, including ambiguous residue.
type Finding struct{ Kind, Identity, Reason string }

// NamespaceObservation is a broad, non-destructive namespace observation.
type NamespaceObservation = netns.Observation

// Inventory is a complete observed candidate set, not deletion authority.
type Inventory struct {
	Namespaces []NamespaceObservation
	Findings   []Finding
}

// Inventory is broad observation, not destructive authority. Malformed marked
// objects stay visible; inability to inspect a valid namespace is an error.
func (e *Engine) Inventory(ctx context.Context) (Inventory, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return inventory(ctx)
}

func inventory(ctx context.Context) (Inventory, error) {
	namespaces, err := netns.Inventory(ctx)
	if err != nil {
		return Inventory{}, err
	}
	result := Inventory{Namespaces: namespaces}
	if err := netns.InspectAppliance(ctx, func(h *netlink.Handle) error { return observeNamespace(ctx, h, "appliance-root", &result) }); err != nil {
		return Inventory{}, fmt.Errorf("inspect appliance plane: %w", err)
	}
	for i := range result.Namespaces {
		ns := &result.Namespaces[i]
		if !ns.Owned && ns.OwnershipError != "" {
			result.Findings = append(result.Findings, Finding{"namespace", ns.Name, ns.OwnershipError})
			continue
		}
		before := len(result.Findings)
		err := netns.WithInspectionHandle(ctx, ns.Name, ns.Inode, func(handle *netlink.Handle) error {
			return observeNamespace(ctx, handle, ns.Name, &result)
		})
		if err != nil {
			return Inventory{}, fmt.Errorf("inventory namespace %s: %w", ns.Name, err)
		}
		if !ns.Owned && len(result.Findings) != before {
			ns.OwnershipError = "Nephos markers found under foreign namespace identity"
		}
	}
	return result, nil
}

func observeNamespace(ctx context.Context, h *netlink.Handle, name string, result *Inventory) error {
	links, err := h.LinkList()
	if err != nil {
		return fmt.Errorf("inventory links in %s: %w", name, err)
	}
	for _, link := range links {
		identity := name + "/" + link.Attrs().Name
		if strings.HasPrefix(link.Attrs().Alias, "nephos:") {
			reason := "marked ENI remains"
			if _, err := parseENIAlias(link.Attrs().Alias); err != nil {
				reason = err.Error()
			}
			result.Findings = append(result.Findings, Finding{"eni", identity, reason})
		}
		addresses, err := h.AddrList(link, netlink.FAMILY_ALL)
		if err != nil {
			return fmt.Errorf("inventory addresses on %s: %w", identity, err)
		}
		for _, addr := range addresses {
			if addr.Label == gatewayLabel {
				result.Findings = append(result.Findings, Finding{"gateway", identity + "/" + addr.String(), "marked gateway remains"})
			}
		}
	}
	if _, err := h.RouteListFiltered(netlink.FAMILY_ALL, &netlink.Route{Table: 0}, 0); err != nil {
		return fmt.Errorf("inventory routes: %w", err)
	}
	for _, family := range []int{netlink.FAMILY_V4, netlink.FAMILY_V6} {
		if _, err := h.RuleList(family); err != nil {
			return fmt.Errorf("inventory rules: %w", err)
		}
	}
	_, exists, err := inspectENISourcePolicy(ctx)
	if err != nil {
		return err
	}
	if exists {
		result.Findings = append(result.Findings, Finding{"ruleset", name + "/inet nephos", "marked policy remains"})
	}
	return nil
}

var errWrongENIScope = errors.New("desired ENI belongs to another namespace")

type eniOwner struct {
	namespace string
	eni       model.ENI
}

func desiredENIOwners(vpcs []model.VPC, subnets []model.Subnet, enis []model.ENI) (map[string]eniOwner, error) {
	vpcByID := make(map[string]model.VPC)
	for i := range vpcs {
		vpc := &vpcs[i]
		if _, duplicate := vpcByID[vpc.ID]; duplicate || vpc.WorkspaceID != "default" {
			return nil, fmt.Errorf("ambiguous desired VPC %s", vpc.ID)
		}
		vpcByID[vpc.ID] = *vpc
	}
	subnetByID := make(map[string]model.Subnet)
	for i := range subnets {
		subnet := &subnets[i]
		vpc, ok := vpcByID[subnet.VPCID]
		if !ok || subnet.WorkspaceID != vpc.WorkspaceID {
			return nil, fmt.Errorf("subnet %s has no matching VPC owner", subnet.ID)
		}
		if _, duplicate := subnetByID[subnet.ID]; duplicate {
			return nil, fmt.Errorf("duplicate desired subnet %s", subnet.ID)
		}
		subnetByID[subnet.ID] = *subnet
	}
	owners := make(map[string]eniOwner)
	for i := range enis {
		eni := &enis[i]
		subnet, ok := subnetByID[eni.SubnetID]
		if !ok || eni.WorkspaceID != subnet.WorkspaceID || !subnet.CIDRBlock.Contains(eni.PrivateIP) {
			return nil, fmt.Errorf("ENI %s has no matching subnet owner", eni.ID)
		}
		if _, duplicate := owners[eni.ID]; duplicate {
			return nil, fmt.Errorf("duplicate desired ENI %s", eni.ID)
		}
		name, err := netns.Name(vpcByID[subnet.VPCID].ShortIndex)
		if err != nil {
			return nil, err
		}
		owners[eni.ID] = eniOwner{namespace: name, eni: *eni}
	}
	return owners, nil
}
