//go:build linux && integration

package topology

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/vishvananda/netlink"
)

func TestInventoryReportsRootMarkerWithoutDeletingItReal(t *testing.T) {
	if os.Getenv("NEPHOS_IN_APPLIANCE") != "1" {
		t.Skip("requires isolated privileged appliance")
	}
	ctx := context.Background()
	link := &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: "nx-inv-test"}}
	if err := netlink.LinkAdd(link); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := netlink.LinkDel(link); err != nil {
			t.Error(err)
		}
	})
	if err := netlink.LinkSetAlias(link, "nephos:malformed"); err != nil {
		t.Fatal(err)
	}
	engine := New()
	observed, err := engine.Inventory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, finding := range observed.Findings {
		found = found || strings.HasPrefix(finding.Identity, "appliance-root/")
	}
	if !found {
		t.Fatal("marked root link disappeared from broad inventory")
	}
	if err := engine.CollectOrphans(ctx, nil, nil, nil); err == nil {
		t.Fatal("root marker granted cleanup")
	}
	if _, err := netlink.LinkByName("nx-inv-test"); err != nil {
		t.Fatal("root marker was mutated:", err)
	}
}
