//go:build linux && integration

package topology

import (
	"fmt"
	"testing"

	"github.com/vishvananda/netlink"

	"github.com/Amrzxk/nephos/internal/network/netns"
)

func TestENIObsoleteInodeOwnership(t *testing.T) {
	for _, kind := range []string{"owned pinned pair", "foreign peer marker", "different live attachment"} {
		t.Run(kind, func(t *testing.T) {
			ctx, engine, vpc, subnets := eniTestWorld(t)
			instance := eniTestContainer(t, subnets[0], "10.0.1.4")
			ensureTestENI(ctx, t, engine, vpc, subnets[0], instance)
			nsName, _ := netns.Name(vpc.ShortIndex)
			name, _ := eniLinkName(instance.eni.ShortIndex)
			old := eniAlias(instance.eni, instance.target.Inode()+1)
			if err := netns.WithHandle(ctx, nsName, func(h *netlink.Handle) error {
				link, err := h.LinkByName(name)
				if err != nil {
					return err
				}
				return h.LinkSetAlias(link, old)
			}); err != nil {
				t.Fatal(err)
			}
			peerMarker := old
			if kind == "foreign peer marker" {
				peerMarker = "foreign-test-peer"
			}
			if err := netns.WithInstanceHandle(ctx, instance.target, func(h *netlink.Handle) error {
				link, err := h.LinkByName("eth0")
				if err != nil {
					return err
				}
				return h.LinkSetAlias(link, peerMarker)
			}); err != nil {
				t.Fatal(err)
			}
			target := instance.target
			if kind == "different live attachment" {
				target = eniTestContainer(t, subnets[0], "10.0.1.5").target
			}
			err := engine.EnsureENI(ctx, vpc, subnets[0], instance.eni, target)
			want := old
			if kind == "owned pinned pair" {
				if err != nil {
					t.Fatalf("owned stale inode could not converge: %v", err)
				}
				want = eniAlias(instance.eni, target.Inode())
			} else if err == nil {
				t.Fatal("foreign/ambiguous ENI adopted")
			}
			if err := netns.WithHandle(ctx, nsName, func(h *netlink.Handle) error {
				link, err := h.LinkByName(name)
				if err != nil {
					return err
				}
				if link.Attrs().Alias != want {
					return fmt.Errorf("router alias=%q want=%q", link.Attrs().Alias, want)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if err := netns.WithInstanceHandle(ctx, instance.target, func(h *netlink.Handle) error {
				peer, err := h.LinkByName("eth0")
				if err != nil {
					return err
				}
				if kind == "owned pinned pair" {
					peerMarker = want
				}
				if peer.Attrs().Alias != peerMarker {
					return fmt.Errorf("peer ownership changed without proof: %q", peer.Attrs().Alias)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
