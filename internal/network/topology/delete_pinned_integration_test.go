//go:build linux && integration

package topology

import (
	"testing"

	"github.com/vishvananda/netlink"
	vnetns "github.com/vishvananda/netns"

	"github.com/Amrzxk/nephos/internal/network/netns"
)

func TestDeleteVPCCleansKernelBeforeUnlinkWithIndependentPin(t *testing.T) {
	ctx, engine, vpc, _ := eniTestWorld(t)
	name, _ := netns.Name(vpc.ShortIndex)
	pin, err := vnetns.GetFromPath("/run/netns/" + name)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pin.Close() }()
	h, err := netlink.NewHandleAt(pin)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if err := engine.DeleteVPC(ctx, vpc); err != nil {
		t.Fatal(err)
	}
	links, err := h.LinkList()
	if err != nil {
		t.Fatal(err)
	}
	for _, link := range links {
		if link.Attrs().Name != "lo" {
			t.Errorf("owned kernel link survives unlinked namespace: %s", link.Attrs().Name)
		}
	}
	rules, err := h.RuleList(netlink.FAMILY_V4)
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range rules {
		if rule.Priority == localPriority || rule.Priority == dropPriority {
			t.Errorf("owned policy rule survives unlink: %s", rule.String())
		}
	}
}

func TestOrphanVPCWithExitedENIAndStaleOwnedPolicy(t *testing.T) {
	ctx, engine, vpc, subnets := eniTestWorld(t)
	instance := eniTestContainer(t, subnets[0], "10.0.1.4")
	ensureTestENI(ctx, t, engine, vpc, subnets[0], instance)
	// Independent kernel loss models the veth disappearing on runtime exit,
	// without using DeleteENI to rewrite the source set first.
	name, _ := netns.Name(vpc.ShortIndex)
	if err := netns.WithHandle(ctx, name, func(h *netlink.Handle) error {
		linkName, _ := eniLinkName(instance.eni.ShortIndex)
		link, err := h.LinkByName(linkName)
		if err != nil {
			return err
		}
		return h.LinkDel(link)
	}); err != nil {
		t.Fatal(err)
	}
	if err := engine.CollectOrphans(ctx, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
}
