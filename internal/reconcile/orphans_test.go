package reconcile

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Amrzxk/nephos/internal/compute"
	"github.com/Amrzxk/nephos/internal/model"
	"github.com/Amrzxk/nephos/internal/network/topology"
	"github.com/Amrzxk/nephos/internal/store"
)

type orphanRuntime struct {
	compute.Runtime
	items   []compute.ObservedContainer
	err     error
	removed []compute.Reference
}

type orphanNetwork struct {
	state     topology.Inventory
	err       error
	collected bool
	runtime   *orphanRuntime
}

func (n *orphanNetwork) Inventory(context.Context) (topology.Inventory, error) { return n.state, n.err }
func (n *orphanNetwork) CollectOrphans(context.Context, []model.VPC, []model.Subnet, []model.ENI) error {
	if len(n.runtime.removed) != 0 {
		return errors.New("runtime parent deleted before network proof")
	}
	n.collected = true
	return nil
}

func TestOrphanCollectionValidatesBothInventoriesBeforeDeletingChildren(t *testing.T) {
	for _, fault := range []string{"none", "runtime", "network", "alias"} {
		t.Run(fault, func(t *testing.T) {
			ctx := context.Background()
			s, err := store.Open(ctx, filepath.Join(t.TempDir(), "db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			ref := compute.Reference{Identity: compute.Identity{WorkspaceID: "default", InstanceID: "i-00000000000000001"}, ID: compute.RuntimeID(strings.Repeat("a", 64))}
			runtime := &orphanRuntime{items: []compute.ObservedContainer{{Reference: ref, Name: ref.Identity.InstanceID, Managed: true}}}
			network := &orphanNetwork{runtime: runtime}
			switch fault {
			case "runtime":
				runtime.items[0].Managed = false
			case "network":
				network.err = errors.New("denied netlink")
			case "alias":
				network.state.Namespaces = []topology.NamespaceObservation{{Name: "nx-vpc-01", OwnershipError: "noncanonical name"}}
			}
			err = CollectOrphans(ctx, s, runtime, network)
			if fault == "none" {
				if err != nil || !network.collected || len(runtime.removed) != 1 {
					t.Fatalf("collection=%v runtime=%+v err=%v", network.collected, runtime.removed, err)
				}
			} else if err == nil || network.collected || len(runtime.removed) != 0 {
				t.Fatalf("uncertainty authorized deletion: %+v err=%v", network, err)
			}
		})
	}
}

func (r *orphanRuntime) Inventory(context.Context) ([]compute.ObservedContainer, error) {
	return r.items, r.err
}
func (r *orphanRuntime) Delete(_ context.Context, ref compute.Reference) error {
	r.removed = append(r.removed, ref)
	return nil
}

func TestRuntimeOrphanCollection(t *testing.T) {
	pending := compute.Reference{Identity: compute.Identity{WorkspaceID: "default", InstanceID: "i-00000000000000001"}, ID: compute.RuntimeID(strings.Repeat("a", 64))}
	retained := compute.Reference{Identity: compute.Identity{WorkspaceID: "default", InstanceID: "i-00000000000000002"}, ID: compute.RuntimeID(strings.Repeat("b", 64))}
	orphan := compute.Reference{Identity: compute.Identity{WorkspaceID: "default", InstanceID: "i-00000000000000003"}, ID: compute.RuntimeID(strings.Repeat("c", 64))}
	runtime := &orphanRuntime{items: []compute.ObservedContainer{
		{Reference: pending, Name: pending.Identity.InstanceID, Managed: true},
		{Reference: retained, Name: retained.Identity.InstanceID, Managed: true},
		{Reference: orphan, Name: orphan.Identity.InstanceID, Managed: true},
	}}
	desired := []model.Instance{
		{ID: pending.Identity.InstanceID, WorkspaceID: "default"},
		{ID: retained.Identity.InstanceID, WorkspaceID: "default", RuntimeID: strings.Repeat("f", 64)},
	}
	if err := collectRuntimeOrphans(context.Background(), desired, runtime); err != nil {
		t.Fatal(err)
	}
	if len(runtime.removed) != 1 || runtime.removed[0] != orphan {
		t.Fatalf("cached IDs became ownership or rowless container survived: %+v", runtime.removed)
	}
}

func TestAmbiguousOwnershipFailsBeforeRuntimeDeletion(t *testing.T) {
	for _, fault := range []string{"incomplete", "ambiguous", "contradictory", "duplicate"} {
		t.Run(fault, func(t *testing.T) {
			ref := compute.Reference{Identity: compute.Identity{WorkspaceID: "default", InstanceID: "i-00000000000000001"}, ID: compute.RuntimeID(strings.Repeat("a", 64))}
			item := compute.ObservedContainer{Reference: ref, Name: ref.Identity.InstanceID, Managed: true}
			runtime := &orphanRuntime{items: []compute.ObservedContainer{item}}
			switch fault {
			case "incomplete":
				runtime.err = compute.ErrInspectionUnavailable
			case "ambiguous":
				item.Managed = false
				item.OwnershipError = "contradictory marker"
				runtime.items = append(runtime.items, item)
			case "contradictory":
				item.Name = "foreign"
				runtime.items = append(runtime.items, item)
			case "duplicate":
				item.Reference.ID = compute.RuntimeID(strings.Repeat("b", 64))
				runtime.items = append(runtime.items, item)
			}
			err := collectRuntimeOrphans(context.Background(), nil, runtime)
			if err == nil || len(runtime.removed) != 0 {
				t.Fatalf("uncertain inventory authorized mutation: %+v %v", runtime.removed, err)
			}
			if fault == "incomplete" && !errors.Is(err, compute.ErrInspectionUnavailable) {
				t.Fatalf("lost inspection classification: %v", err)
			}
		})
	}
}
