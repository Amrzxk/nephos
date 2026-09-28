// Package topology converges VPC router namespaces from SQLite snapshots.
package topology

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	"github.com/Amrzxk/nephos/internal/model"
	"github.com/Amrzxk/nephos/internal/network/netns"
)

const (
	routerLinkName = "nxr0"
	gatewayLabel   = "nxr0:gw"
	localPriority  = 100
	dropPriority   = 32000
	mainRouteTable = 254
)

// Engine owns only VPC namespaces and subnet gateway addresses in this slice.
type Engine struct{ mu sync.Mutex }

// New returns the Linux VPC topology engine.
func New() *Engine { return &Engine{} }

// EnsureVPC preserves an existing namespace and converges its router state.
func (e *Engine) EnsureVPC(ctx context.Context, vpc model.VPC, subnets []model.Subnet) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	name, err := netns.Name(vpc.ShortIndex)
	if err != nil {
		return err
	}
	gateways, err := desiredGateways(vpc, subnets)
	if err != nil {
		return err
	}
	if err := netns.Ensure(ctx, name); err != nil {
		return fmt.Errorf("ensure VPC %s namespace %s: %w", vpc.ID, name, err)
	}
	return netns.WithHandle(ctx, name, func(handle *netlink.Handle) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := ensureRouterLinks(handle); err != nil {
			return fmt.Errorf("VPC %s router links: %w", vpc.ID, err)
		}
		if err := setNamespaceSysctl("net/ipv4/ip_forward", "1"); err != nil {
			return err
		}
		if err := setNamespaceSysctl("net/ipv4/conf/default/send_redirects", "0"); err != nil {
			return err
		}
		if err := setNamespaceSysctl("net/ipv4/conf/nxr0/send_redirects", "0"); err != nil {
			return err
		}
		if err := reconcileGateways(handle, gateways); err != nil {
			return fmt.Errorf("VPC %s gateways: %w", vpc.ID, err)
		}
		if err := ensurePolicyRules(handle, vpc.CIDRBlock); err != nil {
			return fmt.Errorf("VPC %s policy rules: %w", vpc.ID, err)
		}
		return ctx.Err()
	})
}

// DeleteVPC removes only the namespace named by this VPC's short index.
func (e *Engine) DeleteVPC(ctx context.Context, vpc model.VPC) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	name, err := netns.Name(vpc.ShortIndex)
	if err != nil {
		return err
	}
	if err := netns.Delete(ctx, name); err != nil {
		return fmt.Errorf("delete VPC %s namespace %s: %w", vpc.ID, name, err)
	}
	return nil
}

// ListVPCNames supports conservative orphan detection in the reconciler.
func (e *Engine) ListVPCNames(ctx context.Context) ([]string, error) {
	return netns.List(ctx)
}

func desiredGateways(vpc model.VPC, subnets []model.Subnet) ([]netip.Addr, error) {
	prefix := vpc.CIDRBlock
	if !prefix.IsValid() || !prefix.Addr().Is4() || prefix.Masked() != prefix ||
		prefix.Bits() < 16 || prefix.Bits() > 28 {
		return nil, fmt.Errorf("VPC %s has invalid IPv4 CIDR %s", vpc.ID, prefix)
	}
	gateways := make([]netip.Addr, 0, len(subnets))
	seen := make([]netip.Prefix, 0, len(subnets))
	for i := range subnets {
		cidr := subnets[i].CIDRBlock
		if subnets[i].VPCID != vpc.ID || !cidr.IsValid() || !cidr.Addr().Is4() ||
			cidr.Masked() != cidr || cidr.Bits() < prefix.Bits() || cidr.Bits() > 28 ||
			!prefix.Contains(cidr.Addr()) {
			return nil, fmt.Errorf("subnet %s is not a valid range inside VPC %s", subnets[i].ID, vpc.ID)
		}
		for _, prior := range seen {
			if cidr.Overlaps(prior) {
				return nil, fmt.Errorf("subnet %s overlaps %s in VPC %s", subnets[i].ID, prior, vpc.ID)
			}
		}
		seen = append(seen, cidr)
		gateways = append(gateways, cidr.Addr().Next())
	}
	slices.SortFunc(gateways, func(a, b netip.Addr) int { return a.Compare(b) })
	return gateways, nil
}

func ensureRouterLinks(handle *netlink.Handle) error {
	lo, err := handle.LinkByName("lo")
	if err != nil {
		return fmt.Errorf("find loopback: %w", err)
	}
	if err := handle.LinkSetUp(lo); err != nil {
		return fmt.Errorf("bring up loopback: %w", err)
	}
	link, err := handle.LinkByName(routerLinkName)
	var missing netlink.LinkNotFoundError
	if errors.As(err, &missing) {
		dummy := &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: routerLinkName}}
		if err := handle.LinkAdd(dummy); err != nil {
			return fmt.Errorf("create router dummy: %w", err)
		}
		link, err = handle.LinkByName(routerLinkName)
	}
	if err != nil {
		return fmt.Errorf("find router dummy: %w", err)
	}
	if link.Type() != "dummy" {
		return fmt.Errorf("refusing non-dummy router link %s (%s)", routerLinkName, link.Type())
	}
	if err := handle.LinkSetUp(link); err != nil {
		return fmt.Errorf("bring up router dummy: %w", err)
	}
	return nil
}

func setNamespaceSysctl(key, value string) error {
	if strings.HasPrefix(key, "net/ipv4/conf/all/") || strings.Contains(key, "..") {
		return fmt.Errorf("refusing non-local sysctl %q", key)
	}
	switch key {
	case "net/ipv4/ip_forward", "net/ipv4/conf/default/send_redirects",
		"net/ipv4/conf/nxr0/send_redirects":
	default:
		return fmt.Errorf("sysctl %q is not permitted by the VPC engine", key)
	}
	path := "/proc/sys/" + key
	if err := os.WriteFile(path, []byte(value), 0o644); err != nil {
		return fmt.Errorf("set VPC sysctl %s=%s: %w", key, value, err)
	}
	return nil
}

func reconcileGateways(handle *netlink.Handle, desired []netip.Addr) error {
	link, err := handle.LinkByName(routerLinkName)
	if err != nil {
		return fmt.Errorf("find router dummy: %w", err)
	}
	existing, err := handle.AddrList(link, netlink.FAMILY_V4)
	if err != nil {
		return fmt.Errorf("list router addresses: %w", err)
	}
	wanted := make(map[string]bool, len(desired))
	for _, addr := range desired {
		wanted[addr.String()] = false
	}
	for i := range existing {
		if existing[i].IPNet == nil {
			continue
		}
		addr, ok := netip.AddrFromSlice(existing[i].IP)
		if !ok {
			return fmt.Errorf("router has unparsable IPv4 address %s", existing[i].String())
		}
		key := addr.Unmap().String()
		if _, needed := wanted[key]; needed && existing[i].Label != gatewayLabel {
			return fmt.Errorf("gateway address %s is owned by label %q", key, existing[i].Label)
		}
		if existing[i].Label != gatewayLabel {
			return fmt.Errorf("unmanaged router address %s with label %q", key, existing[i].Label)
		}
		if _, needed := wanted[key]; needed {
			if ones, bits := existing[i].Mask.Size(); ones != 32 || bits != 32 {
				return fmt.Errorf("gateway %s has unexpected prefix length %d", key, ones)
			}
			wanted[key] = true
			continue
		}
		if err := handle.AddrDel(link, &existing[i]); err != nil {
			return fmt.Errorf("remove stale gateway %s: %w", key, err)
		}
	}
	for _, addr := range desired {
		if wanted[addr.String()] {
			continue
		}
		ipNet := &net.IPNet{IP: net.IP(addr.AsSlice()), Mask: net.CIDRMask(32, 32)}
		if err := handle.AddrAdd(link, &netlink.Addr{IPNet: ipNet, Label: gatewayLabel}); err != nil {
			return fmt.Errorf("add gateway %s: %w", addr, err)
		}
	}
	return nil
}

func ensurePolicyRules(handle *netlink.Handle, cidr netip.Prefix) error {
	_, destination, err := net.ParseCIDR(cidr.String())
	if err != nil {
		return fmt.Errorf("parse VPC policy CIDR %s: %w", cidr, err)
	}
	local := netlink.NewRule()
	local.Family = netlink.FAMILY_V4
	local.Priority = localPriority
	local.Dst = destination
	local.Table = mainRouteTable
	local.Type = unix.RTN_UNICAST
	if err := ensureRule(handle, local); err != nil {
		return fmt.Errorf("local VPC rule: %w", err)
	}
	drop := netlink.NewRule()
	drop.Family = netlink.FAMILY_V4
	drop.Priority = dropPriority
	drop.Table = mainRouteTable
	drop.Type = unix.RTN_BLACKHOLE
	if err := ensureRule(handle, drop); err != nil {
		return fmt.Errorf("unmatched blackhole rule: %w", err)
	}
	return nil
}

func ensureRule(handle *netlink.Handle, want *netlink.Rule) error {
	rules, err := listKernelPolicyRules()
	if err != nil {
		return err
	}
	found := false
	for i := range rules {
		if rules[i].priority != want.Priority {
			continue
		}
		if found || rules[i].table != want.Table || rules[i].action != want.Type ||
			len(rules[i].extra) != 0 || !sameDestination(rules[i].dst, want.Dst) {
			return fmt.Errorf(
				"priority %d has unexpected or duplicate policy rule: table=%d action=%d destination=%v extra=%v; want table=%d action=%d destination=%v",
				want.Priority, rules[i].table, rules[i].action, rules[i].dst, rules[i].extra,
				want.Table, want.Type, want.Dst,
			)
		}
		found = true
	}
	if found {
		return nil
	}
	if err := handle.RuleAdd(want); err != nil {
		return fmt.Errorf("add priority %d policy rule: %w", want.Priority, err)
	}
	return nil
}

func sameDestination(a, b *net.IPNet) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.String() == b.String()
}
