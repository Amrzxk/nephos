//go:build linux && integration

package topology

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/vishvananda/netlink"

	"github.com/Amrzxk/nephos/internal/model"
	"github.com/Amrzxk/nephos/internal/network/netns"
)

func TestENIConvergence(t *testing.T) {
	ctx, engine, vpc, subnets := eniTestWorld(t)
	first := eniTestContainer(t, subnets[0], "10.0.1.4")
	second := eniTestContainer(t, subnets[1], "10.0.2.4")
	var wg sync.WaitGroup
	errs := make(chan error, 3)
	for _, instance := range []eniTestInstance{first, second} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			subnet := subnets[0]
			if instance.eni.SubnetID == subnets[1].ID {
				subnet = subnets[1]
			}
			errs <- engine.EnsureENI(ctx, vpc, subnet, instance.eni, instance.target)
		}()
	}
	wg.Add(1)
	go func() { defer wg.Done(); errs <- engine.EnsureVPC(ctx, vpc, subnets) }()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	ensureTestENI(ctx, t, engine, vpc, subnets[0], first)
	ensureTestENI(ctx, t, engine, vpc, subnets[1], second)
	var rootPeerIndex int
	nsName, _ := netns.Name(vpc.ShortIndex)
	linkName, _ := eniLinkName(first.eni.ShortIndex)
	if err := netns.WithHandle(ctx, nsName, func(h *netlink.Handle) error {
		link, err := h.LinkByName(linkName)
		if err != nil {
			return err
		}
		rootPeerIndex = link.Attrs().Index
		if link.Type() != "veth" || link.Attrs().Flags&net.FlagUp == 0 {
			return fmt.Errorf("invalid router link: %+v", link.Attrs())
		}
		for key, want := range map[string]string{"proxy_arp": "1", "send_redirects": "0", "rp_filter": "0"} {
			got, err := os.ReadFile("/proc/sys/net/ipv4/conf/" + linkName + "/" + key)
			if err != nil {
				return err
			}
			if strings.TrimSpace(string(got)) != want {
				return fmt.Errorf("sysctl %s=%q", key, got)
			}
		}
		routes, err := h.RouteList(link, netlink.FAMILY_V4)
		if err != nil {
			return err
		}
		count := 0
		for _, route := range routes {
			if route.Dst != nil && route.Dst.String() == "10.0.1.4/32" {
				count++
			}
		}
		if count != 1 {
			return fmt.Errorf("ENI host route count=%d", count)
		}
		raw, err := eniCommand(t, "nft", "-j", "list", "set", "inet", "nephos", "eni_sources")
		if err != nil {
			return fmt.Errorf("nft: %w %s", err, raw)
		}
		if !strings.Contains(string(raw), "10.0.1.4") || !strings.Contains(string(raw), "10.0.2.4") {
			return fmt.Errorf("concurrent hooks lost source rules: %s", raw)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := netns.WithInstanceHandle(ctx, first.target, func(h *netlink.Handle) error {
		peer, err := h.LinkByName("eth0")
		if err != nil {
			return err
		}
		if peer.Attrs().ParentIndex != rootPeerIndex || peer.Attrs().HardwareAddr.String() != first.eni.MACAddress {
			return fmt.Errorf("instance MAC/peer mismatch")
		}
		addresses, err := h.AddrList(peer, netlink.FAMILY_V4)
		if err != nil {
			return err
		}
		count := 0
		for _, address := range addresses {
			if address.IPNet.String() == "10.0.1.4/24" {
				count++
			}
		}
		if count != 1 {
			return fmt.Errorf("instance address count=%d", count)
		}
		routes, err := h.RouteList(peer, netlink.FAMILY_V4)
		if err != nil {
			return err
		}
		defaultOK, onLinkOK := false, false
		for _, route := range routes {
			if (route.Dst == nil || route.Dst.String() == "0.0.0.0/0") && route.Gw.Equal(net.ParseIP("10.0.1.1")) {
				defaultOK = true
			}
			if route.Dst != nil && route.Dst.String() == "10.0.1.1/32" && route.Scope == netlink.SCOPE_LINK {
				onLinkOK = true
			}
		}
		if !defaultOK || !onLinkOK {
			return fmt.Errorf("gateway routes missing: %+v", routes)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	eniMustCommand(t, "podman", "exec", first.runtimeID, "ping", "-c", "1", "-W", "2", "10.0.2.4")
	// ICMP is available to ordinary instance users without adding NET_RAW
	// to Podman's capability policy or carrying ping's file capability.
	if got := eniMustCommand(t, "podman", "exec", first.runtimeID, "getcap", "/usr/bin/ping"); len(got) != 0 {
		t.Fatalf("ping retains a file capability: %s", got)
	}
	if got := strings.Join(strings.Fields(string(eniMustCommand(t, "podman", "exec", first.runtimeID, "cat", "/proc/sys/net/ipv4/ping_group_range"))), " "); got != "0 65535" {
		t.Fatalf("instance ICMP group range: %q", got)
	}
	eniMustCommand(t, "podman", "exec", "--user=1000:1000", first.runtimeID, "ping", "-c", "1", "-W", "2", "10.0.2.4")
	if err := engine.DeleteENI(ctx, vpc, first.eni); err != nil {
		t.Fatal(err)
	}
	if err := engine.DeleteENI(ctx, vpc, first.eni); err != nil {
		t.Fatalf("repeat delete: %v", err)
	}
	if err := netns.WithHandle(ctx, nsName, func(h *netlink.Handle) error {
		if _, err := h.LinkByName(linkName); err == nil {
			return fmt.Errorf("deleted ENI remains")
		}
		other, _ := eniLinkName(second.eni.ShortIndex)
		if _, err := h.LinkByName(other); err != nil {
			return fmt.Errorf("unrelated ENI deleted: %w", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestENIForeignLink(t *testing.T) {
	ctx, engine, vpc, subnets := eniTestWorld(t)
	instance := eniTestContainer(t, subnets[0], "10.0.1.4")
	nsName, _ := netns.Name(vpc.ShortIndex)
	linkName, _ := eniLinkName(instance.eni.ShortIndex)
	if err := netns.WithHandle(ctx, nsName, func(h *netlink.Handle) error {
		if err := h.LinkAdd(&netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: linkName, Alias: "foreign-test-link"}}); err != nil {
			return err
		}
		link, err := h.LinkByName(linkName)
		if err != nil {
			return err
		}
		if err := h.LinkSetAlias(link, "foreign-test-link"); err != nil {
			return err
		}
		link, err = h.LinkByName(linkName)
		if err != nil {
			return err
		}
		if link.Attrs().Alias != "foreign-test-link" {
			return fmt.Errorf("LinkSetAlias did not persist: %q", link.Attrs().Alias)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := engine.EnsureENI(ctx, vpc, subnets[0], instance.eni, instance.target); err == nil {
		t.Fatal("foreign link silently adopted")
	}
	if err := engine.DeleteENI(ctx, vpc, instance.eni); err == nil {
		t.Fatal("foreign link silently deleted")
	}
	if err := netns.WithHandle(ctx, nsName, func(h *netlink.Handle) error {
		link, err := h.LinkByName(linkName)
		if err != nil {
			return err
		}
		if link.Type() != "dummy" || link.Attrs().Alias != "foreign-test-link" {
			return fmt.Errorf("foreign link changed: type=%q alias=%q", link.Type(), link.Attrs().Alias)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestENISourceCheck(t *testing.T) {
	ctx, engine, vpc, subnets := eniTestWorld(t)
	first := eniTestContainer(t, subnets[0], "10.0.1.4")
	second := eniTestContainer(t, subnets[1], "10.0.2.4")
	ensureTestENI(ctx, t, engine, vpc, subnets[0], first)
	ensureTestENI(ctx, t, engine, vpc, subnets[1], second)
	witnessStart(t, second)
	eniMustCommand(t, "podman", "exec", first.runtimeID, "ping", "-c", "1", "-W", "2", "10.0.2.4")
	before := witnessPackets(t, second)
	if before < 1 {
		t.Fatal("positive control did not hit destination witness")
	}
	eniMustCommand(t, "podman", "exec", first.runtimeID, "ip", "addr", "add", "10.0.1.5/32", "dev", "eth0")
	if out, err := eniCommand(t, "podman", "exec", first.runtimeID, "ping", "-I", "10.0.1.5", "-c", "2", "-W", "1", "10.0.2.4"); err == nil {
		t.Fatalf("forged source was accepted: %s", out)
	}
	if after := witnessPackets(t, second); after != before {
		t.Fatalf("forged packet reached destination: before=%d after=%d", before, after)
	}
	name, _ := netns.Name(vpc.ShortIndex)
	if err := netns.WithHandle(ctx, name, func(*netlink.Handle) error {
		raw, err := eniCommand(t, "nft", "-j", "list", "chain", "inet", "nephos", "source_check")
		if err != nil {
			return fmt.Errorf("nft: %w %s", err, raw)
		}
		var doc struct {
			Nftables []struct {
				Rule *struct {
					Expr []struct{ Counter *struct{ Packets uint64 } }
				}
			}
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			return err
		}
		var drops uint64
		for _, entry := range doc.Nftables {
			if entry.Rule != nil {
				for _, expr := range entry.Rule.Expr {
					if expr.Counter != nil {
						drops += expr.Counter.Packets
					}
				}
			}
		}
		if drops < 2 {
			return fmt.Errorf("spoofed packets did not hit anti-spoof counter: %d", drops)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	eniMustCommand(t, "podman", "exec", first.runtimeID, "ping", "-I", "10.0.1.4", "-c", "1", "-W", "2", "10.0.2.4")
	if after := witnessPackets(t, second); after != before+1 {
		t.Fatalf("valid source positive control failed: %d", after)
	}
}

func TestENIIsolation(t *testing.T) {
	ctx, engine, vpc, subnets := eniTestWorld(t)
	first := eniTestContainer(t, subnets[0], "10.0.1.4")
	second := eniTestContainer(t, subnets[1], "10.0.2.4")
	ensureTestENI(ctx, t, engine, vpc, subnets[0], first)
	ensureTestENI(ctx, t, engine, vpc, subnets[1], second)
	other := model.VPC{ID: fmt.Sprintf("vpc-%017x", unusedIndex(t)), WorkspaceID: "default", ShortIndex: unusedIndex(t), CIDRBlock: vpc.CIDRBlock}
	otherSubnet := subnets[0]
	otherSubnet.VPCID = other.ID
	if err := engine.EnsureVPC(ctx, other, []model.Subnet{otherSubnet}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := engine.DeleteVPC(ctx, other); err != nil {
			t.Error(err)
		}
	})
	otherSource := eniTestContainer(t, otherSubnet, "10.0.1.4")
	remoteOnly := eniTestContainer(t, otherSubnet, "10.0.1.5")
	ensureTestENI(ctx, t, engine, other, otherSubnet, otherSource)
	ensureTestENI(ctx, t, engine, other, otherSubnet, remoteOnly)
	witnessStart(t, remoteOnly)
	eniMustCommand(t, "podman", "exec", otherSource.runtimeID, "ping", "-c", "1", "-W", "2", "10.0.1.5")
	before := witnessPackets(t, remoteOnly)
	if before < 1 {
		t.Fatal("remote-only target positive control failed")
	}
	eniMustCommand(t, "podman", "exec", first.runtimeID, "ping", "-c", "1", "-W", "2", "10.0.2.4")
	if out, err := eniCommand(t, "podman", "exec", first.runtimeID, "ping", "-c", "2", "-W", "1", "10.0.1.5"); err == nil {
		t.Fatalf("overlapping VPC leaked to remote-only destination: %s", out)
	}
	if after := witnessPackets(t, remoteOnly); after != before {
		t.Fatalf("overlapping VPC delivered packets to other VPC: %d -> %d", before, after)
	}
	eniMustCommand(t, "podman", "exec", otherSource.runtimeID, "ping", "-c", "1", "-W", "2", "10.0.1.5")
	if after := witnessPackets(t, remoteOnly); after != before+1 {
		t.Fatal("target stopped during isolation probe")
	}
}

func TestENIPolicyFailureLeavesNoLivePair(t *testing.T) {
	ctx, engine, vpc, subnets := eniTestWorld(t)
	instance := eniTestContainer(t, subnets[0], "10.0.1.4")
	name, _ := netns.Name(vpc.ShortIndex)
	linkName, _ := eniLinkName(instance.eni.ShortIndex)
	if err := netns.WithHandle(ctx, name, func(*netlink.Handle) error {
		_, err := eniCommand(t, "nft", "add", "table", "inet", "nephos", "{ comment \"foreign\"; }")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := engine.EnsureENI(ctx, vpc, subnets[0], instance.eni, instance.target); err == nil {
		t.Fatal("foreign policy table accepted")
	}
	if err := netns.WithHandle(ctx, name, func(h *netlink.Handle) error {
		if _, err := h.LinkByName(linkName); err == nil {
			return fmt.Errorf("failed ENI left router pair")
		}
		raw, err := eniCommand(t, "nft", "-j", "list", "table", "inet", "nephos")
		if err != nil || !strings.Contains(string(raw), "foreign") {
			return fmt.Errorf("foreign policy changed: %s", raw)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := netns.WithInstanceHandle(ctx, instance.target, func(h *netlink.Handle) error {
		if _, err := h.LinkByName("eth0"); err == nil {
			return fmt.Errorf("failed ENI left instance interface")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
