package topology

import (
	"context"
	"fmt"

	"github.com/vishvananda/netlink"

	"github.com/Amrzxk/nephos/internal/model"
	"github.com/Amrzxk/nephos/internal/network/netns"
)

func verifyENIOwner(alias string, eni model.ENI) error {
	owner, err := parseENIAlias(alias)
	if err != nil {
		return err
	}
	if owner.ID != eni.ID || owner.IP != eni.PrivateIP || owner.MAC != eni.MACAddress {
		return fmt.Errorf("refusing foreign or mismatched ENI ownership")
	}
	return nil
}

// verifyExistingENI proves the actual attachment before any link/policy change.
// An obsolete inode in a marker can be rebound only when the reciprocal pair
// is already in the pinned, verified instance namespace. An old attachment
// in an unproven namespace is ambiguous, not permission to destroy its peer.
func verifyExistingENI(ctx context.Context, handle *netlink.Handle, router netlink.Link, eni model.ENI, target *netns.InstanceTarget, fd int) error {
	if router.Type() != "veth" {
		return fmt.Errorf("refusing foreign router ENI type")
	}
	if err := verifyENIOwner(router.Attrs().Alias, eni); err != nil {
		return err
	}
	nsID, err := handle.GetNetNsIdByFd(fd)
	if err != nil {
		return fmt.Errorf("verify existing ENI namespace: %w", err)
	}
	if nsID < 0 || router.Attrs().NetNsID != nsID {
		return fmt.Errorf("ENI attachment is not in the pinned instance namespace")
	}
	return netns.WithInstanceHandle(ctx, target, func(peerHandle *netlink.Handle) error {
		peer, err := peerHandle.LinkByName("eth0")
		if err != nil {
			return fmt.Errorf("verify existing ENI peer: %w", err)
		}
		if peer.Type() != "veth" || peer.Attrs().ParentIndex != router.Attrs().Index || router.Attrs().ParentIndex != peer.Attrs().Index {
			return fmt.Errorf("refusing unrelated existing ENI peer")
		}
		return verifyENIOwner(peer.Attrs().Alias, eni)
	})
}
