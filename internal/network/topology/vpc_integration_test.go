//go:build linux && integration

package topology

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"slices"
	"sort"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/vishvananda/netlink"

	"github.com/Amrzxk/nephos/internal/model"
	"github.com/Amrzxk/nephos/internal/network/netns"
)

func unusedIndex(t *testing.T) int64 {
	t.Helper()
	for range 10 {
		n, err := rand.Int(rand.Reader, big.NewInt(90000000))
		if err != nil {
			t.Fatal(err)
		}
		index := n.Int64() + 10000000
		name, err := netns.Name(index)
		if err != nil {
			t.Fatal(err)
		}
		_, err = os.Lstat("/run/netns/" + name)
		if os.IsNotExist(err) {
			return index
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Fatal("could not allocate an unused VPC index")
	return 0
}

func gatewayAddresses(t *testing.T, name string) []string {
	t.Helper()
	var got []string
	err := netns.Do(context.Background(), name, func() error {
		link, err := netlink.LinkByName("nxr0")
		if err != nil {
			return err
		}
		addrs, err := netlink.AddrList(link, netlink.FAMILY_V4)
		if err != nil {
			return err
		}
		for _, addr := range addrs {
			if addr.Label != "nxr0:gw" {
				return fmt.Errorf("unexpected unmanaged router address %s with label %q", addr.String(), addr.Label)
			}
			got = append(got, addr.IPNet.String())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	return got
}

func TestOverlappingVPCsHaveIsolatedConvergentGateways(t *testing.T) {
	ctx := context.Background()
	rootBefore := applianceRootNetworkState(t)
	engine := New()
	first := model.VPC{
		ID: "vpc-first", CIDRBlock: netip.MustParsePrefix("10.0.0.0/16"),
		ShortIndex: unusedIndex(t),
	}
	second := model.VPC{
		ID: "vpc-second", CIDRBlock: first.CIDRBlock,
		ShortIndex: unusedIndex(t),
	}
	if second.ShortIndex == first.ShortIndex {
		t.Fatal("random test indexes collided")
	}
	names := make([]string, 0, 2)
	for _, vpc := range []model.VPC{first, second} {
		name, err := netns.Name(vpc.ShortIndex)
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
		t.Cleanup(func() {
			if err := engine.DeleteVPC(context.Background(), vpc); err != nil {
				t.Error(err)
			}
		})
	}
	subnet1 := model.Subnet{
		VPCID: first.ID, CIDRBlock: netip.MustParsePrefix("10.0.1.0/24"),
	}
	subnet2 := model.Subnet{
		VPCID: first.ID, CIDRBlock: netip.MustParsePrefix("10.0.2.0/24"),
	}
	if err := engine.EnsureVPC(ctx, first, []model.Subnet{subnet1, subnet2}); err != nil {
		t.Fatal(err)
	}
	if err := engine.EnsureVPC(ctx, second, []model.Subnet{
		{VPCID: second.ID, CIDRBlock: subnet1.CIDRBlock},
	}); err != nil {
		t.Fatal(err)
	}
	wantFirst := []string{"10.0.1.1/32", "10.0.2.1/32"}
	if got := gatewayAddresses(t, names[0]); !slices.Equal(got, wantFirst) {
		t.Fatalf("first VPC gateways=%v, want %v", got, wantFirst)
	}
	if got := gatewayAddresses(t, names[1]); !slices.Equal(got, []string{"10.0.1.1/32"}) {
		t.Fatalf("second VPC gateways=%v", got)
	}
	if err := engine.EnsureVPC(ctx, first, []model.Subnet{subnet1}); err != nil {
		t.Fatal(err)
	}
	if err := engine.EnsureVPC(ctx, first, []model.Subnet{subnet1}); err != nil {
		t.Fatalf("idempotent EnsureVPC: %v", err)
	}
	if got := gatewayAddresses(t, names[0]); !slices.Equal(got, []string{"10.0.1.1/32"}) {
		t.Fatalf("stale gateway survived: %v", got)
	}
	if got := gatewayAddresses(t, names[1]); !slices.Equal(got, []string{"10.0.1.1/32"}) {
		t.Fatalf("second VPC changed with first: %v", got)
	}
	live, err := engine.ListVPCNames(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if !slices.Contains(live, name) {
			t.Fatalf("%s missing from %v", name, live)
		}
	}
	rules, err := exec.Command("ip", "-n", names[0], "-4", "rule", "show").CombinedOutput()
	if err != nil {
		t.Fatalf("inspect VPC policy rules: %v\n%s", err, rules)
	}
	var localLine, dropLine string
	for _, line := range strings.Split(string(rules), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "100:":
			localLine = line
		case "32000:":
			dropLine = line
		}
	}
	if strings.Count(string(rules), "100:") != 1 ||
		strings.Count(string(rules), "32000:") != 1 ||
		!strings.Contains(localLine, "to 10.0.0.0/16") ||
		!strings.Contains(localLine, "lookup main") ||
		!strings.Contains(dropLine, "blackhole") {
		t.Fatalf("VPC policy rules are not the local route plus one catch-all blackhole:\n%s", rules)
	}
	if names[0] == names[1] {
		t.Fatal(fmt.Errorf("overlapping VPCs share namespace %s", names[0]))
	}
	for _, vpc := range []model.VPC{first, second} {
		if err := engine.DeleteVPC(ctx, vpc); err != nil {
			t.Fatal(err)
		}
	}
	live, err = engine.ListVPCNames(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if slices.Contains(live, name) {
			t.Fatalf("deleted VPC namespace %s survived", name)
		}
	}
	if rootAfter := applianceRootNetworkState(t); rootAfter != rootBefore {
		t.Fatalf("appliance root network changed:\nbefore: %s\nafter: %s", rootBefore, rootAfter)
	}
}

func TestUnexpectedRouterAddressFailsClosed(t *testing.T) {
	ctx := context.Background()
	engine := New()
	vpc := model.VPC{
		ID: "vpc-unexpected", CIDRBlock: netip.MustParsePrefix("10.0.0.0/16"),
		ShortIndex: unusedIndex(t),
	}
	name, err := netns.Name(vpc.ShortIndex)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := engine.DeleteVPC(context.Background(), vpc); err != nil {
			t.Error(err)
		}
	})
	if err := engine.EnsureVPC(ctx, vpc, nil); err != nil {
		t.Fatal(err)
	}
	err = netns.WithHandle(ctx, name, func(handle *netlink.Handle) error {
		link, err := handle.LinkByName("nxr0")
		if err != nil {
			return err
		}
		_, ipNet, err := net.ParseCIDR("192.0.2.9/32")
		if err != nil {
			return err
		}
		ipNet.IP = net.ParseIP("192.0.2.9")
		return handle.AddrAdd(link, &netlink.Addr{IPNet: ipNet, Label: "nxr0:other"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.EnsureVPC(ctx, vpc, nil); err == nil ||
		!strings.Contains(err.Error(), "unmanaged router address") {
		t.Fatalf("unexpected router address was silently accepted: %v", err)
	}
}

func applianceRootNetworkState(t *testing.T) string {
	t.Helper()
	var stat unix.Stat_t
	if err := unix.Stat("/proc/thread-self/ns/net", &stat); err != nil {
		t.Fatal(err)
	}
	links, err := netlink.LinkList()
	if err != nil {
		t.Fatal(err)
	}
	linkNames := make([]string, 0, len(links))
	for i := range links {
		linkNames = append(linkNames, links[i].Attrs().Name)
	}
	sort.Strings(linkNames)
	routes, err := netlink.RouteList(nil, netlink.FAMILY_V4)
	if err != nil {
		t.Fatal(err)
	}
	routeNames := make([]string, 0, len(routes))
	for i := range routes {
		routeNames = append(routeNames, routes[i].String())
	}
	sort.Strings(routeNames)
	forward, err := os.ReadFile("/proc/sys/net/ipv4/ip_forward")
	if err != nil {
		t.Fatal(err)
	}
	redirects, err := os.ReadFile("/proc/sys/net/ipv4/conf/default/send_redirects")
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("inode=%d links=%v routes=%v forward=%s redirects=%s",
		stat.Ino, linkNames, routeNames, strings.TrimSpace(string(forward)), strings.TrimSpace(string(redirects)))
}
