//go:build linux && integration

package topology

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	"github.com/Amrzxk/nephos/internal/model"
	"github.com/Amrzxk/nephos/internal/network/netns"
)

func TestENIOrphanCollectionReal(t *testing.T) {
	ctx, engine, vpc, subnets := eniTestWorld(t)
	desired := eniTestContainer(t, subnets[0], "10.0.1.4")
	orphan := eniTestContainer(t, subnets[1], "10.0.2.4")
	ensureTestENI(ctx, t, engine, vpc, subnets[0], desired)
	ensureTestENI(ctx, t, engine, vpc, subnets[1], orphan)
	name, err := netns.Name(vpc.ShortIndex)
	if err != nil {
		t.Fatal(err)
	}
	if err := netns.WithHandle(ctx, name, func(h *netlink.Handle) error {
		linkName, _ := eniLinkName(orphan.eni.ShortIndex)
		link, err := h.LinkByName(linkName)
		if err != nil {
			return err
		}
		routes, err := h.RouteListFiltered(netlink.FAMILY_ALL, &netlink.Route{LinkIndex: link.Attrs().Index}, netlink.RT_FILTER_OIF)
		for _, route := range routes {
			t.Logf("independent route witness: dst=%v protocol=%d scope=%d table=%d type=%d", route.Dst, route.Protocol, route.Scope, route.Table, route.Type)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := netns.WithHandle(ctx, name, func(h *netlink.Handle) error {
		return h.LinkAdd(&netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: "ve999"}})
	}); err != nil {
		t.Fatal(err)
	}
	if err := engine.CollectOrphans(ctx, []model.VPC{vpc}, subnets, []model.ENI{desired.eni}); err != nil {
		t.Fatal(err)
	}
	desiredName, _ := eniLinkName(desired.eni.ShortIndex)
	orphanName, _ := eniLinkName(orphan.eni.ShortIndex)
	if err := netns.WithHandle(ctx, name, func(h *netlink.Handle) error {
		for _, live := range []string{desiredName, "ve999"} {
			if _, err := h.LinkByName(live); err != nil {
				return err
			}
		}
		if _, err := h.LinkByName(orphanName); err == nil {
			t.Error("rowless ENI survived")
		}
		foreign, err := h.LinkByName("ve999")
		if err != nil {
			return err
		}
		return h.LinkDel(foreign) // exact test-created neighbor, never product GC
	}); err != nil {
		t.Fatal(err)
	}
}

func TestOrphanVPCRefusesForeignPolicySelectorsReal(t *testing.T) {
	for _, ipv6 := range []bool{false, true} {
		t.Run(fmt.Sprint("IPv6=", ipv6), func(t *testing.T) {
			ctx, engine, vpc, _ := eniTestWorld(t)
			name, _ := netns.Name(vpc.ShortIndex)
			if err := netns.WithHandle(ctx, name, func(*netlink.Handle) error {
				if ipv6 {
					_, err := eniCommand(t, "ip", "-6", "rule", "add", "priority", "1234", "lookup", "123")
					return err
				}
				if _, err := eniCommand(t, "ip", "rule", "del", "priority", "0", "lookup", "local"); err != nil {
					return err
				}
				_, err := eniCommand(t, "ip", "rule", "add", "priority", "0", "from", "192.0.2.0/24", "lookup", "local")
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if err := engine.CollectOrphans(ctx, nil, nil, nil); err == nil {
				t.Fatal("foreign policy selector granted namespace unlink")
			}
			if err := netns.WithHandle(ctx, name, func(*netlink.Handle) error {
				if ipv6 {
					_, err := eniCommand(t, "ip", "-6", "rule", "del", "priority", "1234", "lookup", "123")
					return err
				}
				if _, err := eniCommand(t, "ip", "rule", "del", "priority", "0", "from", "192.0.2.0/24", "lookup", "local"); err != nil {
					return err
				}
				_, err := eniCommand(t, "ip", "rule", "add", "priority", "0", "lookup", "local")
				return err
			}); err != nil {
				t.Fatalf("foreign rule/namespace was mutated: %v", err)
			}
		})
	}
}

func TestInventoryFindsMarkersUnderForeignNamespaceNameReal(t *testing.T) {
	ctx, engine, vpc, _ := eniTestWorld(t)
	name, _ := netns.Name(vpc.ShortIndex)
	const namespaceRoot = "/run/netns"
	foreign := filepath.Join(namespaceRoot, "foreign-"+name)
	f, err := os.OpenFile(foreign, os.O_CREATE|os.O_EXCL|os.O_RDONLY, 0o444)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mount(filepath.Join(namespaceRoot, name), foreign, "none", unix.MS_BIND, ""); err != nil {
		_ = os.Remove(foreign)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := unix.Unmount(foreign, 0); err != nil {
			t.Error(err)
		}
		if err := os.Remove(foreign); err != nil {
			t.Error(err)
		}
	})
	if err := netns.Delete(ctx, name); err != nil {
		t.Fatal(err)
	}
	observed, err := engine.Inventory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(observed.Findings) == 0 {
		t.Fatal("Nephos markers under a foreign namespace name disappeared from inventory")
	}
	if err := engine.CollectOrphans(ctx, nil, nil, nil); err == nil {
		t.Fatal("foreign-named marked namespace granted destructive cleanup")
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Fatal("ambiguous namespace was removed:", err)
	}
}

func TestOrphanVPCRefusesForeignRouteAndAddressesReal(t *testing.T) {
	for _, kind := range []string{"kernel route", "loopback address", "router IPv6 address"} {
		t.Run(kind, func(t *testing.T) {
			ctx, engine, vpc, _ := eniTestWorld(t)
			name, _ := netns.Name(vpc.ShortIndex)
			if err := netns.WithHandle(ctx, name, func(h *netlink.Handle) error {
				linkName := "lo"
				if kind == "router IPv6 address" {
					linkName = "nxr0"
				}
				link, err := h.LinkByName(linkName)
				if err != nil {
					return err
				}
				if kind == "kernel route" {
					_, dst, err := net.ParseCIDR("203.0.113.0/24")
					if err != nil {
						return err
					}
					return h.RouteAdd(&netlink.Route{LinkIndex: link.Attrs().Index, Dst: dst, Protocol: unix.RTPROT_KERNEL, Scope: netlink.SCOPE_LINK})
				}
				cidr := "10.6.0.1/32"
				if kind == "router IPv6 address" {
					cidr = "fd00::1/64"
				}
				addr, err := netlink.ParseAddr(cidr)
				if err != nil {
					return err
				}
				return h.AddrAdd(link, addr)
			}); err != nil {
				t.Fatal(err)
			}
			if err := engine.CollectOrphans(ctx, nil, nil, nil); err == nil {
				t.Fatal("foreign residue granted namespace unlink")
			}
			if err := netns.WithHandle(ctx, name, func(h *netlink.Handle) error {
				// Independent exact cleanup proves the product preserved the namespace
				// and seeded neighbor; broader test-fixture teardown follows later.
				linkName := "lo"
				if kind == "router IPv6 address" {
					linkName = "nxr0"
				}
				link, err := h.LinkByName(linkName)
				if err != nil {
					return err
				}
				if kind == "kernel route" {
					_, dst, err := net.ParseCIDR("203.0.113.0/24")
					if err != nil {
						return err
					}
					return h.RouteDel(&netlink.Route{LinkIndex: link.Attrs().Index, Dst: dst, Protocol: unix.RTPROT_KERNEL, Scope: netlink.SCOPE_LINK})
				}
				cidr := "10.6.0.1/32"
				if kind == "router IPv6 address" {
					cidr = "fd00::1/64"
				}
				addr, err := netlink.ParseAddr(cidr)
				if err != nil {
					return err
				}
				return h.AddrDel(link, addr)
			}); err != nil {
				t.Fatalf("foreign residue or parent was mutated: %v", err)
			}
		})
	}
}

func TestOrphanCollectionDoesNotBlockDesiredPolicyRecoveryReal(t *testing.T) {
	ctx, engine, vpc, subnets := eniTestWorld(t)
	instance := eniTestContainer(t, subnets[0], "10.0.1.4")
	ensureTestENI(ctx, t, engine, vpc, subnets[0], instance)
	name, _ := netns.Name(vpc.ShortIndex)
	if err := netns.WithHandle(ctx, name, func(*netlink.Handle) error {
		_, err := eniCommand(t, "nft", "flush", "chain", "inet", "nephos", "source_check")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := engine.CollectOrphans(ctx, []model.VPC{vpc}, subnets, []model.ENI{instance.eni}); err != nil {
		t.Fatalf("GC prevented desired-owner policy repair: %v", err)
	}
	ensureTestENI(ctx, t, engine, vpc, subnets[0], instance)
}

func TestAmbiguousOwnershipFailsWithoutRemovingENIReal(t *testing.T) {
	ctx, engine, vpc, subnets := eniTestWorld(t)
	orphan := eniTestContainer(t, subnets[0], "10.0.1.4")
	ensureTestENI(ctx, t, engine, vpc, subnets[0], orphan)
	name, _ := netns.Name(vpc.ShortIndex)
	linkName, _ := eniLinkName(orphan.eni.ShortIndex)
	if err := netns.WithHandle(ctx, name, func(h *netlink.Handle) error {
		link, err := h.LinkByName(linkName)
		if err != nil {
			return err
		}
		return h.LinkSetAlias(link, "nephos:eni:malformed")
	}); err != nil {
		t.Fatal(err)
	}
	if err := engine.CollectOrphans(ctx, []model.VPC{vpc}, subnets, nil); err == nil {
		t.Fatal("malformed marker granted deletion authority")
	}
	if err := netns.WithHandle(ctx, name, func(h *netlink.Handle) error { _, err := h.LinkByName(linkName); return err }); err != nil {
		t.Fatal("ambiguous link was removed:", err)
	}
	if err := netns.WithHandle(ctx, name, func(h *netlink.Handle) error {
		link, err := h.LinkByName(linkName)
		if err != nil {
			return err
		}
		return h.LinkSetAlias(link, eniAlias(orphan.eni, orphan.target.Inode())) // restore this test's own corruption
	}); err != nil {
		t.Fatal(err)
	}
}

func TestOrphanVPCChildrenRemovedBeforeUnlinkReal(t *testing.T) {
	ctx, engine, vpc, _ := eniTestWorld(t)
	name, _ := netns.Name(vpc.ShortIndex)
	if err := netns.WithHandle(ctx, name, func(h *netlink.Handle) error {
		return h.LinkAdd(&netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: "foreign0"}})
	}); err != nil {
		t.Fatal(err)
	}
	if err := engine.CollectOrphans(ctx, nil, nil, nil); err == nil {
		t.Fatal("foreign child allowed whole namespace removal")
	}
	if err := netns.WithHandle(ctx, name, func(h *netlink.Handle) error {
		foreign, err := h.LinkByName("foreign0")
		if err != nil {
			return err
		}
		return h.LinkDel(foreign)
	}); err != nil {
		t.Fatal(err)
	}
	if err := engine.CollectOrphans(ctx, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	observed, err := engine.Inventory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(observed.Namespaces) != 0 || len(observed.Findings) != 0 {
		t.Fatalf("owned parent/children remain: %+v", observed)
	}
}
