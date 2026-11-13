package topology

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"reflect"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	"github.com/Amrzxk/nephos/internal/network/netns"
)

// withProvenOrphan holds the actual peer namespace pin through destructive
// work. Recovery's deliberately relaxed alias-rebinding proof is NOT reused.
func withProvenOrphan(ctx context.Context, routerHandle *netlink.Handle, router netlink.Link, record eniIdentity, fn func() error) error {
	if router.Type() != "veth" || !canonicalENILink(router.Attrs().Name) {
		return fmt.Errorf("ambiguous marked router link %s", router.Attrs().Name)
	}
	return netns.WithObservedHandle(ctx, record.Namespace, func(fd int, peerHandle *netlink.Handle) error {
		nsID, err := routerHandle.GetNetNsIdByFd(fd)
		if err != nil {
			return fmt.Errorf("inspect orphan peer namespace: %w", err)
		}
		if nsID < 0 || router.Attrs().NetNsID != nsID {
			return fmt.Errorf("orphan ENI peer does not match pinned namespace")
		}
		peer, err := peerHandle.LinkByIndex(router.Attrs().ParentIndex)
		if err != nil {
			return fmt.Errorf("inspect orphan ENI peer: %w", err)
		}
		identity, err := parseENIAlias(peer.Attrs().Alias)
		if err != nil {
			return err
		}
		if peer.Type() != "veth" || peer.Attrs().ParentIndex != router.Attrs().Index || identity != record || peer.Attrs().HardwareAddr.String() != record.MAC || (peer.Attrs().Name != "eth0" && peer.Attrs().Name != "vp"+router.Attrs().Name[2:]) {
			return fmt.Errorf("orphan ENI reciprocal identity, alias or actual MAC mismatch")
		}
		addresses, err := peerHandle.AddrList(peer, netlink.FAMILY_V4)
		if err != nil {
			return err
		}
		if len(addresses) != 1 || !addresses[0].IP.Equal(net.IP(record.IP.AsSlice())) {
			return fmt.Errorf("orphan ENI actual IPv4 address mismatch")
		}
		bits, size := addresses[0].Mask.Size()
		if size != 32 || bits < 16 || bits > 28 {
			return fmt.Errorf("orphan ENI has invalid subnet prefix")
		}
		gateway := netip.PrefixFrom(record.IP, bits).Masked().Addr().Next()
		if err := verifyDeletionRoutes(routerHandle, router, record.IP, netip.Addr{}, false); err != nil {
			return err
		}
		if err := verifyDeletionRoutes(peerHandle, peer, record.IP, gateway, true); err != nil {
			return err
		}
		return fn()
	})
}

// LinkDel implicitly removes routes on both ends. Do not destroy foreign
// dependent routes just because a link's own marker is valid.
func verifyDeletionRoutes(h *netlink.Handle, link netlink.Link, ip, gateway netip.Addr, peer bool) error {
	routes, err := h.RouteListFiltered(netlink.FAMILY_ALL, &netlink.Route{LinkIndex: link.Attrs().Index, Table: unix.RT_TABLE_UNSPEC}, netlink.RT_FILTER_OIF)
	if err != nil {
		return fmt.Errorf("inspect dependent routes on %s: %w", link.Attrs().Name, err)
	}
	addresses, err := h.AddrList(link, netlink.FAMILY_ALL)
	if err != nil {
		return fmt.Errorf("inspect route address provenance: %w", err)
	}
	for _, address := range addresses {
		if address.IP.To4() == nil {
			if !automaticLinkLocal(address, link.Attrs().HardwareAddr) {
				return fmt.Errorf("foreign IPv6 address on orphan link %s", link.Attrs().Name)
			}
		} else if !peer {
			return fmt.Errorf("foreign IPv4 address on router ENI %s", link.Attrs().Name)
		}
	}
	for i := range routes {
		route := &routes[i]
		if addressDerivedKernelRoute(*route, addresses) {
			continue
		}
		if route.Table == mainRouteTable && route.Type == unix.RTN_UNICAST && routeDefaultAncillary(*route) && route.Priority == 0 && len(route.Src) == 0 && (route.Protocol == unix.RTPROT_BOOT || route.Protocol == 0) {
			if !peer && route.Dst != nil && route.Dst.String() == ip.String()+"/32" && route.Gw == nil && route.Scope == netlink.SCOPE_LINK {
				continue
			}
			if peer && (route.Dst == nil || route.Dst.String() == "0.0.0.0/0") && route.Gw.Equal(net.IP(gateway.AsSlice())) {
				continue
			}
			if peer && route.Dst != nil && route.Dst.String() == gateway.String()+"/32" && route.Gw == nil && route.Scope == netlink.SCOPE_LINK {
				continue
			}
		}
		return fmt.Errorf("refusing orphan link %s with foreign dependent route %s", link.Attrs().Name, route.String())
	}
	return nil
}

func addressDerivedKernelRoute(route netlink.Route, addresses []netlink.Addr) bool {
	if route.Protocol != unix.RTPROT_KERNEL || route.Dst == nil || len(route.Gw) != 0 || !routeDefaultAncillary(route) {
		return false
	}
	for _, address := range addresses {
		if address.IPNet == nil {
			continue
		}
		prefix := &net.IPNet{IP: address.IP.Mask(address.Mask), Mask: address.Mask}
		priorityOK := route.Priority == 0 || (address.IP.To4() == nil && route.Priority == 256)
		if !priorityOK || (len(route.Src) != 0 && !route.Src.Equal(address.IP)) {
			continue
		}
		if route.Table == mainRouteTable && route.Type == unix.RTN_UNICAST && route.Dst.String() == prefix.String() {
			return true
		}
		bits, size := route.Dst.Mask.Size()
		if route.Table == unix.RT_TABLE_LOCAL && bits == size {
			if route.Type == unix.RTN_LOCAL && route.Dst.IP.Equal(address.IP) {
				return true
			}
			if route.Type == unix.RTN_BROADCAST && (route.Dst.IP.Equal(address.Broadcast) || route.Dst.IP.Equal(prefix.IP)) {
				return true
			}
		}
	}
	return false
}

// Keep the accepted shape explicit. Future netlink route attributes remain
// non-default here and therefore fail proof until intentionally supported.
func routeDefaultAncillary(route netlink.Route) bool {
	route.LinkIndex = 0
	route.Scope = 0
	route.Dst = nil
	route.Src = nil
	route.Gw = nil
	route.Protocol = 0
	route.Priority = 0
	route.Family = 0
	route.Table = 0
	route.Type = 0
	return reflect.DeepEqual(route, netlink.Route{})
}
