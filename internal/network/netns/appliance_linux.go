package netns

import (
	"context"
	"errors"
	"fmt"

	"github.com/vishvananda/netlink"
	vnetns "github.com/vishvananda/netns"
)

// InspectAppliance runs read-only observation in the appliance's private root
// namespace. The caller must never mutate kernel state through this callback.
// This is not the host namespace and confers no destructive ownership.
func InspectAppliance(ctx context.Context, fn func(*netlink.Handle) error) error {
	root, err := vnetns.GetFromPath("/proc/1/ns/net")
	if err != nil {
		return fmt.Errorf("pin appliance namespace for inspection: %w", err)
	}
	release := trackReference(ctx, "control-plane-namespace-fd", fmt.Sprintf("inspection-fd:%d", root))
	return errors.Join(withPinnedHandle(ctx, root, fn), closeTrackedFD(ctx, int(root), release))
}
