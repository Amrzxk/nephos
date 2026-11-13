package topology

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os/exec"
	"strings"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netlink/nl"
	"golang.org/x/sys/unix"
)

// validateOrphanRouter refuses foreign residue before deleting a parent. It
// removes known kernel children while the namespace is pinned, never by unlink
// alone: an external FD may retain the namespace after its mount disappears.
func validateOrphanRouter(ctx context.Context, h *netlink.Handle, remove bool) error {
	links, err := h.LinkList()
	if err != nil {
		return err
	}
	var router netlink.Link
	addressesByIndex := make(map[int][]netlink.Addr)
	for _, link := range links {
		switch {
		case link.Attrs().Name == "lo" && link.Type() == "device":
		case link.Attrs().Name == routerLinkName && link.Type() == "dummy":
			router = link
		case canonicalENILink(link.Attrs().Name) && strings.HasPrefix(link.Attrs().Alias, "nephos:eni:"):
			if remove {
				return fmt.Errorf("orphan ENI remains before parent cleanup")
			}
		default:
			return fmt.Errorf("refusing namespace cleanup with foreign link %s", link.Attrs().Name)
		}
		addresses, err := h.AddrList(link, netlink.FAMILY_ALL)
		if err != nil {
			return err
		}
		addressesByIndex[link.Attrs().Index] = addresses
		for _, addr := range addresses {
			if link.Attrs().Name == "lo" {
				if addr.IPNet == nil || (addr.IPNet.String() != "127.0.0.1/8" && addr.IPNet.String() != "::1/128") {
					return fmt.Errorf("foreign loopback address prevents namespace cleanup")
				}
			} else if addr.IP.To4() == nil && !automaticLinkLocal(addr, link.Attrs().HardwareAddr) {
				return fmt.Errorf("foreign IPv6 address on %s", link.Attrs().Name)
			}
		}
	}
	if router != nil {
		addresses, err := h.AddrList(router, netlink.FAMILY_V4)
		if err != nil {
			return err
		}
		for _, address := range addresses {
			bits, size := address.Mask.Size()
			if address.Label != gatewayLabel || bits != 32 || size != 32 {
				return fmt.Errorf("refusing foreign router address %s", address.String())
			}
		}
	}
	rules, err := listKernelPolicyRules()
	if err != nil {
		return err
	}
	seen := make(map[int]bool)
	for _, rule := range rules {
		if seen[rule.priority] {
			return fmt.Errorf("ambiguous duplicate rule priority %d", rule.priority)
		}
		seen[rule.priority] = true
		switch rule.priority {
		case 0, 32766, 32767: // Kernel's initial local/main/default lookup rules.
			wantTable := map[int]int{0: 255, 32766: 254, 32767: 253}[rule.priority]
			if rule.table != wantTable || rule.action != unix.RTN_UNICAST || rule.dst != nil || !baselineRuleMetadata(rule.extra) {
				return fmt.Errorf("foreign initial policy rule")
			}
		case localPriority:
			if rule.table != mainRouteTable || rule.action != unix.RTN_UNICAST || rule.dst == nil || len(rule.extra) != 0 {
				return fmt.Errorf("ambiguous local VPC rule")
			}
			bits, size := rule.dst.Mask.Size()
			if size != 32 || bits < 16 || bits > 28 || !rule.dst.IP.Equal(rule.dst.IP.Mask(rule.dst.Mask)) {
				return fmt.Errorf("invalid orphan VPC rule CIDR")
			}
		case dropPriority:
			if rule.table != mainRouteTable || rule.action != unix.RTN_BLACKHOLE || rule.dst != nil || len(rule.extra) != 0 {
				return fmt.Errorf("ambiguous VPC drop rule")
			}
		default:
			return fmt.Errorf("refusing foreign policy rule priority %d", rule.priority)
		}
	}
	IPv6Rules, err := listKernelPolicyRulesFor(netlink.FAMILY_V6)
	if err != nil {
		return fmt.Errorf("inspect IPv6 policy rules: %w", err)
	}
	seenIPv6 := make(map[int]bool)
	for _, rule := range IPv6Rules {
		wantTable, allowed := map[int]int{0: 255, 32766: 254, 32767: 253}[rule.priority]
		if !allowed || seenIPv6[rule.priority] || rule.table != wantTable || rule.action != unix.RTN_UNICAST || rule.dst != nil || !baselineRuleMetadata(rule.extra) {
			return fmt.Errorf("foreign IPv6 policy rule priority %d", rule.priority)
		}
		seenIPv6[rule.priority] = true
	}
	// Routes without an owned link, or non-kernel routes after ENI cleanup,
	// cannot be erased by unmounting their namespace.
	routes, err := h.RouteListFiltered(netlink.FAMILY_ALL, &netlink.Route{Table: unix.RT_TABLE_UNSPEC}, 0)
	if err != nil {
		return err
	}
	for i := range routes {
		route := &routes[i]
		if !remove && route.Protocol != unix.RTPROT_KERNEL {
			owned := false
			for _, link := range links {
				if link.Attrs().Index == route.LinkIndex && canonicalENILink(link.Attrs().Name) && strings.HasPrefix(link.Attrs().Alias, "nephos:eni:") {
					owned = true
				}
			}
			if owned {
				continue
			} // Full dependent-route proof is done separately.
		}
		if !addressDerivedKernelRoute(*route, addressesByIndex[route.LinkIndex]) {
			return fmt.Errorf("foreign route remains in orphan namespace: %s", route.String())
		}
	}
	output, exists, err := inspectENISourcePolicy(ctx)
	if err != nil {
		return err
	}
	var doc struct {
		Nftables []struct {
			Table *struct{ Family, Name string }
		}
	}
	if err := json.Unmarshal(output, &doc); err != nil {
		return err
	}
	for _, entry := range doc.Nftables {
		if entry.Table != nil && (entry.Table.Family != "inet" || entry.Table.Name != "nephos") {
			return fmt.Errorf("foreign nftables table prevents namespace cleanup")
		}
	}
	if !remove {
		if exists && !ownedENIPolicyShape(output) {
			return fmt.Errorf("ambiguous orphan source policy")
		}
		return nil
	}
	if exists {
		if !ownedENIPolicyShape(output) {
			return fmt.Errorf("ambiguous residual owned policy")
		}
		if out, err := exec.CommandContext(ctx, "nft", "delete", "table", "inet", "nephos").CombinedOutput(); err != nil {
			return fmt.Errorf("remove owned policy: %w: %s", err, out)
		}
	}
	for _, rule := range rules {
		if rule.priority != localPriority && rule.priority != dropPriority {
			continue
		}
		want := netlink.NewRule()
		want.Family = netlink.FAMILY_V4
		want.Priority = rule.priority
		want.Table = rule.table
		want.Type = rule.action
		want.Dst = rule.dst
		if err := h.RuleDel(want); err != nil {
			return fmt.Errorf("remove owned policy rule: %w", err)
		}
	}
	if router != nil {
		if err := h.LinkDel(router); err != nil {
			return fmt.Errorf("remove owned gateway link: %w", err)
		}
	}
	remaining, err := h.LinkList()
	if err != nil {
		return err
	}
	if len(remaining) != 1 || remaining[0].Attrs().Name != "lo" {
		return fmt.Errorf("kernel children remain before namespace unlink")
	}
	// Repeat the complete shape inspection while still pinned, after removals.
	return validateOrphanRouter(ctx, h, false)
}

func baselineRuleMetadata(extra []string) bool {
	// Kernel protocol provenance is metadata, never a packet selector.
	return len(extra) == 0 || (len(extra) == 1 && extra[0] == fmt.Sprintf("%d:%x", nl.FRA_PROTOCOL, []byte{unix.RTPROT_KERNEL}))
}

func automaticLinkLocal(address netlink.Addr, mac net.HardwareAddr) bool {
	if len(mac) != 6 || address.IPNet == nil || address.IP.To4() != nil {
		return false
	}
	bits, size := address.Mask.Size()
	if bits != 64 || size != 128 {
		return false
	}
	want := net.IP{0xfe, 0x80, 0, 0, 0, 0, 0, 0, mac[0] ^ 2, mac[1], mac[2], 0xff, 0xfe, mac[3], mac[4], mac[5]}
	return address.IP.Equal(want)
}
