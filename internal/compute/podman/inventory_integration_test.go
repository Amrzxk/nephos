//go:build linux && integration

package podman

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/Amrzxk/nephos/internal/compute"
)

func TestRuntimeOrphanInventoryReal(t *testing.T) {
	ctx, client, ref := realInstance(t)
	// The ordinary adapter fixture is a fully owned real running container.
	// A second stopped container deliberately has only a partial marker.
	name := "foreign-inventory-" + string(ref.ID)[:12]
	out, err := exec.CommandContext(ctx, "podman", "create", "--name", name,
		"--label", "io.nephos.managed=false", "--label", "io.nephos.test=inventory",
		"--network=none", image, "/bin/true").CombinedOutput()
	if err != nil {
		t.Fatalf("create marked negative: %v %s", err, out)
	}
	foreignID := strings.TrimSpace(string(out))
	if !runtimeIDPattern.MatchString(foreignID) {
		t.Fatalf("invalid fixture identity: %q", foreignID)
	}
	t.Cleanup(func() {
		out, err := exec.CommandContext(context.WithoutCancel(ctx), "podman", "rm", foreignID).CombinedOutput()
		if err != nil {
			t.Errorf("remove exact negative fixture: %v %s", err, out)
		}
	})
	items, err := client.Inventory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ownedFound, ambiguousFound bool
	for _, item := range items {
		switch item.Reference.ID {
		case ref.ID:
			ownedFound = item.Managed && item.Reference == ref && item.OwnershipError == ""
		case compute.RuntimeID(foreignID):
			ambiguousFound = !item.Managed && item.Name == name && item.OwnershipError != ""
		}
	}
	if !ownedFound || !ambiguousFound {
		t.Fatalf("real list/inspect lost owned or malformed candidate: %+v", items)
	}
	if status, err := client.Inspect(ctx, ref); err != nil || !status.Running {
		t.Fatalf("observation changed owned instance: %+v %v", status, err)
	}
	if out, err := exec.CommandContext(ctx, "podman", "container", "exists", foreignID).CombinedOutput(); err != nil {
		t.Fatalf("observation removed ambiguous neighbor: %v %s", err, out)
	}
}
