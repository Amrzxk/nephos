//go:build linux && integration

package reconcile

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Amrzxk/nephos/internal/compute"
	"github.com/Amrzxk/nephos/internal/compute/podman"
	"github.com/Amrzxk/nephos/internal/model"
)

func TestRuntimeOrphanCollectionReal(t *testing.T) {
	if os.Getenv("NEPHOS_IN_APPLIANCE") != "1" {
		t.Skip("requires the isolated privileged appliance and its private Podman service")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runtime := podman.New("/run/podman/podman.sock")
	base := time.Now().UnixNano()
	instances := make([]model.Instance, 3)
	refs := make([]compute.Reference, 3)
	for i := range instances {
		instances[i] = model.Instance{ID: fmt.Sprintf("i-%017x", base+int64(i)), WorkspaceID: "default", InstanceType: "t3.micro"}
		created, err := runtime.Create(ctx, instances[i])
		if err != nil || !created.Created {
			t.Fatalf("create exact fixture: %+v %v", created, err)
		}
		refs[i] = created.Reference
		t.Cleanup(func() {
			cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer stop()
			if err := runtime.Delete(cleanup, created.Reference); err != nil {
				t.Error(err)
			}
		})
	}
	// The first owner is pending (no cached ID); the second cache points
	// somewhere stale. Neither condition grants orphan deletion authority.
	instances[1].RuntimeID = strings.Repeat("f", 64)
	if err := collectRuntimeOrphans(ctx, instances[:2], runtime); err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		actual, err := runtime.Lookup(ctx, refs[i].Identity)
		if err != nil || actual != refs[i] {
			t.Fatalf("desired root replaced or removed: %+v %v", actual, err)
		}
	}
	if _, err := runtime.Lookup(ctx, refs[2].Identity); !errors.Is(err, compute.ErrNotFound) {
		t.Fatalf("rowless owned root survived: %v", err)
	}
}
