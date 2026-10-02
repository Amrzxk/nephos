package topology

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/vishvananda/netlink"

	"github.com/Amrzxk/nephos/internal/model"
	"github.com/Amrzxk/nephos/internal/network/netns"
)

// observeENI is read-only after reciprocal ownership/attachment verification.
// Only detected drift enters the quiesce/configure/activate repair path.
func observeENI(ctx context.Context, h *netlink.Handle, router netlink.Link, subnet model.Subnet, eni model.ENI, target *netns.InstanceTarget, alias string) (bool, error) {
	if router.Attrs().Alias != alias || router.Attrs().Flags&net.FlagUp == 0 {
		return false, nil
	}
	for key, want := range map[string]string{"proxy_arp": "1", "send_redirects": "0", "rp_filter": "0"} {
		got, err := os.ReadFile("/proc/sys/net/ipv4/conf/" + router.Attrs().Name + "/" + key)
		if err != nil {
			return false, fmt.Errorf("observe ENI sysctl %s: %w", key, err)
		}
		if strings.TrimSpace(string(got)) != want {
			return false, nil
		}
	}
	routes, err := h.RouteList(router, netlink.FAMILY_V4)
	if err != nil {
		return false, fmt.Errorf("observe router ENI routes: %w", err)
	}
	if !hasENIRoute(routes, eni.PrivateIP.String()+"/32", "", netlink.SCOPE_LINK) {
		return false, nil
	}
	peerReady := false
	err = netns.WithInstanceHandle(ctx, target, func(peerHandle *netlink.Handle) error {
		peer, err := peerHandle.LinkByName("eth0")
		if err != nil {
			return err
		}
		if peer.Attrs().Alias != alias || peer.Attrs().Flags&net.FlagUp == 0 || peer.Attrs().HardwareAddr.String() != eni.MACAddress {
			return nil
		}
		addresses, err := peerHandle.AddrList(peer, netlink.FAMILY_V4)
		if err != nil {
			return err
		}
		addressReady := false
		for _, addr := range addresses {
			if addr.IPNet.String() == fmt.Sprintf("%s/%d", eni.PrivateIP, subnet.CIDRBlock.Bits()) {
				addressReady = true
			}
		}
		if !addressReady {
			return nil
		}
		lo, err := peerHandle.LinkByName("lo")
		if err != nil {
			return err
		}
		if lo.Attrs().Flags&net.FlagUp == 0 {
			return nil
		}
		routes, err := peerHandle.RouteList(peer, netlink.FAMILY_V4)
		if err != nil {
			return err
		}
		gateway := subnet.CIDRBlock.Addr().Next().String()
		peerReady = hasENIRoute(routes, gateway+"/32", "", netlink.SCOPE_LINK) && hasENIRoute(routes, "", gateway, netlink.SCOPE_UNIVERSE)
		return nil
	})
	if err != nil || !peerReady {
		return false, err
	}
	records, err := eniSourceRecords(h)
	if err != nil {
		return false, err
	}
	output, exists, err := inspectENISourcePolicy(ctx)
	if err != nil || !exists {
		return false, err
	}
	return eniPolicyMatches(records, output), nil
}

func hasENIRoute(routes []netlink.Route, destination, gateway string, scope netlink.Scope) bool {
	for i := range routes {
		route := &routes[i]
		dst := ""
		if route.Dst != nil && route.Dst.String() != "0.0.0.0/0" {
			dst = route.Dst.String()
		}
		gw := ""
		if route.Gw != nil && !route.Gw.IsUnspecified() {
			gw = route.Gw.String()
		}
		if dst == destination && gw == gateway && route.Scope == scope && route.Table == mainRouteTable {
			return true
		}
	}
	return false
}
