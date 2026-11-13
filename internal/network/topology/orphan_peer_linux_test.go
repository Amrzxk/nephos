package topology

import (
	"net"
	"testing"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

func TestOrphanRoutesAcceptOnlyAddressDerivedKernelRoutes(t *testing.T) {
	address, err := netlink.ParseAddr("fe80::1234/64")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, dst string
		want      bool
	}{
		{"kernel automatic IPv6 prefix", "fe80::/64", true},
		{"foreign kernel-labeled route", "fd00::/64", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, dst, err := net.ParseCIDR(tc.dst)
			if err != nil {
				t.Fatal(err)
			}
			route := netlink.Route{Dst: dst, Table: 254, Protocol: unix.RTPROT_KERNEL, Type: unix.RTN_UNICAST}
			if got := addressDerivedKernelRoute(route, []netlink.Addr{*address}); got != tc.want {
				t.Fatalf("accepted=%v want=%v", got, tc.want)
			}
		})
	}
	_, dst, err := net.ParseCIDR("fe80::/64")
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range []netlink.Route{
		{Dst: dst, Table: 254, Protocol: unix.RTPROT_KERNEL, Type: unix.RTN_UNICAST, MTU: 1200},
		{Dst: dst, Table: 254, Protocol: unix.RTPROT_KERNEL, Type: unix.RTN_UNICAST, Realm: 42},
		{Dst: dst, Table: 254, Protocol: unix.RTPROT_KERNEL, Type: unix.RTN_UNICAST, Priority: 42},
	} {
		if addressDerivedKernelRoute(route, []netlink.Addr{*address}) {
			t.Fatalf("foreign route behavior accepted: %#v", route)
		}
	}
}
