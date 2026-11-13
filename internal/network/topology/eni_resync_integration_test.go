//go:build linux && integration

package topology

import (
	"encoding/json"
	"fmt"
	"net"
	"slices"
	"strings"
	"testing"

	"github.com/vishvananda/netlink"

	"github.com/Amrzxk/nephos/internal/network/netns"
)

func TestENIHealthyResyncIsNonDisruptive(t *testing.T) {
	ctx, engine, vpc, subnets := eniTestWorld(t)
	first := eniTestContainer(t, subnets[0], "10.0.1.4")
	second := eniTestContainer(t, subnets[1], "10.0.2.4")
	ensureTestENI(ctx, t, engine, vpc, subnets[0], first)
	ensureTestENI(ctx, t, engine, vpc, subnets[1], second)
	eniMustCommand(t, "podman", "exec", first.runtimeID, "ip", "addr", "add", "10.0.1.5/32", "dev", "eth0")
	if _, err := eniCommand(t, "podman", "exec", first.runtimeID, "ping", "-I", "10.0.1.5", "-c", "1", "-W", "1", "10.0.2.4"); err == nil {
		t.Fatal("spoof control unexpectedly passed")
	}
	namespace, _ := netns.Name(vpc.ShortIndex)
	policy := func() (uint64, []uint64) {
		t.Helper()
		var output []byte
		if err := netns.WithHandle(ctx, namespace, func(*netlink.Handle) error {
			var err error
			output, err = eniCommand(t, "nft", "-j", "list", "table", "inet", "nephos")
			return err
		}); err != nil {
			t.Fatal(err)
		}
		var observed struct {
			Nftables []struct {
				Table *struct{ Handle uint64 }
				Rule  *struct {
					Handle uint64
					Expr   []struct{ Counter *struct{ Packets uint64 } }
				}
			}
		}
		if err := json.Unmarshal(output, &observed); err != nil {
			t.Fatal(err)
		}
		var packets uint64
		var handles []uint64
		for _, entry := range observed.Nftables {
			if entry.Table != nil {
				handles = append(handles, entry.Table.Handle)
			}
			if entry.Rule != nil {
				handles = append(handles, entry.Rule.Handle)
				for _, expr := range entry.Rule.Expr {
					if expr.Counter != nil {
						packets += expr.Counter.Packets
					}
				}
			}
		}
		if packets < 1 {
			t.Fatalf("missing accumulated anti-spoof counter: %s", output)
		}
		return packets, handles
	}
	beforePackets, beforeHandles := policy()
	beforeCarrier := string(eniMustCommand(t, "podman", "exec", first.runtimeID, "cat", "/sys/class/net/eth0/carrier_changes"))
	for range 3 {
		ensureTestENI(ctx, t, engine, vpc, subnets[0], first)
		ensureTestENI(ctx, t, engine, vpc, subnets[1], second)
	}
	afterCarrier := string(eniMustCommand(t, "podman", "exec", first.runtimeID, "cat", "/sys/class/net/eth0/carrier_changes"))
	if afterCarrier != beforeCarrier {
		t.Errorf("healthy reconciliation flapped carrier: %s -> %s", strings.TrimSpace(beforeCarrier), strings.TrimSpace(afterCarrier))
	}
	if packets, handles := policy(); packets < beforePackets || !slices.Equal(handles, beforeHandles) {
		t.Errorf("healthy reconciliation reset policy/counters: %d/%v -> %d/%v", beforePackets, beforeHandles, packets, handles)
	}
	eniMustCommand(t, "podman", "exec", first.runtimeID, "ping", "-I", "10.0.1.4", "-c", "1", "-W", "2", "10.0.2.4")
}

func TestENIResyncRepairsPolicyDrift(t *testing.T) {
	ctx, engine, vpc, subnets := eniTestWorld(t)
	instance := eniTestContainer(t, subnets[0], "10.0.1.4")
	ensureTestENI(ctx, t, engine, vpc, subnets[0], instance)
	namespace, _ := netns.Name(vpc.ShortIndex)
	if err := netns.WithHandle(ctx, namespace, func(*netlink.Handle) error {
		_, err := eniCommand(t, "nft", "flush", "chain", "inet", "nephos", "source_check")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	ensureTestENI(ctx, t, engine, vpc, subnets[0], instance)
	if err := netns.WithHandle(ctx, namespace, func(*netlink.Handle) error {
		out, err := eniCommand(t, "nft", "-j", "list", "chain", "inet", "nephos", "source_check")
		if err != nil {
			return err
		}
		if !strings.Contains(string(out), "nephos:anti-spoof") {
			return fmt.Errorf("healthy-looking pair hid policy drift: %s", out)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestENIResyncRefusesForeignPolicy(t *testing.T) {
	ctx, engine, vpc, subnets := eniTestWorld(t)
	instance := eniTestContainer(t, subnets[0], "10.0.1.4")
	ensureTestENI(ctx, t, engine, vpc, subnets[0], instance)
	namespace, _ := netns.Name(vpc.ShortIndex)
	name, _ := eniLinkName(instance.eni.ShortIndex)
	if err := netns.WithHandle(ctx, namespace, func(*netlink.Handle) error {
		if _, err := eniCommand(t, "nft", "delete", "table", "inet", "nephos"); err != nil {
			return err
		}
		_, err := eniCommand(t, "nft", "add", "table", "inet", "nephos", "{ comment \"foreign\"; }")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := engine.EnsureENI(ctx, vpc, subnets[0], instance.eni, instance.target); err == nil {
		t.Fatal("foreign policy accepted")
	}
	if err := netns.WithHandle(ctx, namespace, func(h *netlink.Handle) error {
		link, err := h.LinkByName(name)
		if err != nil {
			return err
		}
		if link.Attrs().Flags&net.FlagUp != 0 {
			return fmt.Errorf("unverifiable policy left owned pair active")
		}
		out, err := eniCommand(t, "nft", "-j", "list", "table", "inet", "nephos")
		if err != nil || !strings.Contains(string(out), "foreign") {
			return fmt.Errorf("foreign policy mutated: %s: %w", out, err)
		}
		// Restore only this fixture's replaced table before normal owned cleanup.
		_, err = eniCommand(t, "nft", "delete", "table", "inet", "nephos")
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
