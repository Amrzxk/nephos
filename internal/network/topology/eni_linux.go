package topology

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"

	"github.com/vishvananda/netlink"

	"github.com/Amrzxk/nephos/internal/model"
	"github.com/Amrzxk/nephos/internal/network/netns"
)

// EnsureENI creates an isolated pair and enforces source checks before link-up.
// Shared VPC changes use the same lock as gateway sweeps and deletion.
func (e *Engine) EnsureENI(ctx context.Context, vpc model.VPC, subnet model.Subnet, eni model.ENI, target *netns.InstanceTarget) error {
	if target == nil || target.Inode() == 0 {
		return fmt.Errorf("missing verified instance namespace")
	}
	if _, err := desiredGateways(vpc, []model.Subnet{subnet}); err != nil {
		return err
	}
	if eni.SubnetID != subnet.ID || eni.WorkspaceID != subnet.WorkspaceID || subnet.WorkspaceID != vpc.WorkspaceID || !subnet.CIDRBlock.Contains(eni.PrivateIP) {
		return fmt.Errorf("ENI %s does not belong to subnet %s in VPC %s", eni.ID, subnet.ID, vpc.ID)
	}
	first := subnet.CIDRBlock.Addr().Next().Next().Next().Next()
	if eni.PrivateIP.Compare(first) < 0 || !subnet.CIDRBlock.Contains(eni.PrivateIP.Next()) {
		return fmt.Errorf("ENI %s uses a reserved subnet address", eni.ID)
	}
	name, err := eniLinkName(eni.ShortIndex)
	if err != nil {
		return err
	}
	namespace, err := netns.Name(vpc.ShortIndex)
	if err != nil {
		return err
	}
	alias := eniAlias(eni, target.Inode())
	if _, err := parseENIAlias(alias); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return target.WithFD(ctx, func(fd int) error {
		return netns.WithHandle(ctx, namespace, func(handle *netlink.Handle) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			gateway := subnet.CIDRBlock.Addr().Next()
			dummy, err := handle.LinkByName(routerLinkName)
			if err != nil {
				return fmt.Errorf("find converged router: %w", err)
			}
			addresses, err := handle.AddrList(dummy, netlink.FAMILY_V4)
			if err != nil {
				return err
			}
			gatewayReady := false
			for _, address := range addresses {
				if address.IP.Equal(net.IP(gateway.AsSlice())) && address.Label == gatewayLabel {
					gatewayReady = true
				}
			}
			if !gatewayReady {
				return fmt.Errorf("subnet %s gateway is not ready", subnet.ID)
			}
			router, err := handle.LinkByName(name)
			var missing netlink.LinkNotFoundError
			created := errors.As(err, &missing)
			if err != nil && !created {
				return fmt.Errorf("find router ENI %s: %w", name, err)
			}
			if !created {
				if err := verifyExistingENI(ctx, handle, router, eni, target, fd); err != nil {
					return fmt.Errorf("verify retained ENI %s: %w", name, err)
				}
			}
			if created {
				err = netns.WithInstanceHandle(ctx, target, func(peerHandle *netlink.Handle) error {
					if _, err := peerHandle.LinkByName("eth0"); err == nil {
						return fmt.Errorf("refusing pre-existing foreign eth0")
					} else if !errors.As(err, &missing) {
						return err
					}
					return nil
				})
				if err != nil {
					return err
				}
				pair := &netlink.Veth{LinkAttrs: netlink.LinkAttrs{Name: name, Alias: alias}, PeerName: "vp" + strconv.FormatInt(eni.ShortIndex, 10), PeerNamespace: netlink.NsFd(fd)}
				if err := handle.LinkAdd(pair); err != nil {
					return fmt.Errorf("create ENI pair %s: %w", name, err)
				}
				router, err = handle.LinkByName(name)
				if err != nil {
					return fmt.Errorf("find created ENI %s: %w", name, err)
				}
			}
			// A failed operation leaves no new live pair. Existing pairs stay down on
			// failed policy reinstallation rather than carrying unprotected traffic.
			success := false
			defer func() {
				if !success && created {
					_ = handle.LinkDel(router)
				}
			}()
			if created || router.Attrs().Alias != alias {
				// This kernel does not retain IFLA_IFALIAS from RTM_NEWLINK.
				// Set and verify the marker explicitly before any policy uses it.
				if err := handle.LinkSetAlias(router, alias); err != nil {
					return fmt.Errorf("mark router ENI %s: %w", name, err)
				}
				router, err = handle.LinkByName(name)
				if err != nil {
					return err
				}
				if router.Attrs().Alias != alias {
					return fmt.Errorf("router ENI %s ownership marker did not persist", name)
				}
			}
			if err := handle.LinkSetDown(router); err != nil {
				return fmt.Errorf("quiesce ENI %s: %w", name, err)
			}
			if err := setENISysctls(name); err != nil {
				return err
			}
			nsID, err := handle.GetNetNsIdByFd(fd)
			if err != nil {
				return fmt.Errorf("verify ENI peer namespace: %w", err)
			}
			if nsID < 0 || router.Attrs().NetNsID != nsID {
				return fmt.Errorf("ENI %s peer is not in pinned instance namespace", name)
			}
			if err := netns.WithInstanceHandle(ctx, target, func(peerHandle *netlink.Handle) error {
				peer, err := peerHandle.LinkByName("eth0")
				if errors.As(err, &missing) {
					peer, err = peerHandle.LinkByName("vp" + strconv.FormatInt(eni.ShortIndex, 10))
				}
				if err != nil {
					return fmt.Errorf("find instance ENI: %w", err)
				}
				if peer.Type() != "veth" || peer.Attrs().ParentIndex != router.Attrs().Index || router.Attrs().ParentIndex != peer.Attrs().Index {
					return fmt.Errorf("refusing unrelated instance link")
				}
				if !created {
					if err := verifyENIOwner(peer.Attrs().Alias, eni); err != nil {
						return err
					}
				} else if peer.Attrs().Alias != alias && peer.Attrs().Alias != "" {
					return fmt.Errorf("refusing foreign instance ENI")
				}
				if err := peerHandle.LinkSetDown(peer); err != nil {
					return err
				}
				if err := peerHandle.LinkSetAlias(peer, alias); err != nil {
					return fmt.Errorf("mark instance ENI: %w", err)
				}
				if peer.Attrs().Name != "eth0" {
					if err := peerHandle.LinkSetName(peer, "eth0"); err != nil {
						return fmt.Errorf("rename instance ENI: %w", err)
					}
					peer, err = peerHandle.LinkByName("eth0")
					if err != nil {
						return err
					}
				}
				mac, err := net.ParseMAC(eni.MACAddress)
				if err != nil {
					return err
				}
				if err := peerHandle.LinkSetHardwareAddr(peer, mac); err != nil {
					return fmt.Errorf("set instance MAC: %w", err)
				}
				address, err := netlink.ParseAddr(fmt.Sprintf("%s/%d", eni.PrivateIP, subnet.CIDRBlock.Bits()))
				if err != nil {
					return err
				}
				if err := peerHandle.AddrReplace(peer, address); err != nil {
					return fmt.Errorf("set instance address: %w", err)
				}
				lo, err := peerHandle.LinkByName("lo")
				if err != nil {
					return err
				}
				return peerHandle.LinkSetUp(lo)
			}); err != nil {
				return err
			}
			// Install anti-spoof before either end becomes active. Linux requires
			// an up device for route nexthops, so routes follow activation.
			if err := applyENISourceChecks(ctx, handle); err != nil {
				return err
			}
			if err := netns.WithInstanceHandle(ctx, target, func(peerHandle *netlink.Handle) error {
				peer, err := peerHandle.LinkByName("eth0")
				if err != nil {
					return err
				}
				if err := peerHandle.LinkSetUp(peer); err != nil {
					return err
				}
				_, gatewayNet, err := net.ParseCIDR(gateway.String() + "/32")
				if err != nil {
					return err
				}
				if err := peerHandle.RouteReplace(&netlink.Route{LinkIndex: peer.Attrs().Index, Dst: gatewayNet, Scope: netlink.SCOPE_LINK}); err != nil {
					return fmt.Errorf("set on-link gateway route: %w", err)
				}
				if err := peerHandle.RouteReplace(&netlink.Route{LinkIndex: peer.Attrs().Index, Gw: net.IP(gateway.AsSlice())}); err != nil {
					return fmt.Errorf("set instance default route: %w", err)
				}
				return nil
			}); err != nil {
				return err
			}
			if err := handle.LinkSetUp(router); err != nil {
				return fmt.Errorf("activate ENI %s: %w", name, err)
			}
			_, hostNet, err := net.ParseCIDR(eni.PrivateIP.String() + "/32")
			if err != nil {
				return err
			}
			if err := handle.RouteReplace(&netlink.Route{LinkIndex: router.Attrs().Index, Dst: hostNet, Scope: netlink.SCOPE_LINK, Table: mainRouteTable}); err != nil {
				return fmt.Errorf("set ENI host route: %w", err)
			}
			success = true
			return nil
		})
	})
}

// DeleteENI tears down only the marked router link and its derived policy.
func (e *Engine) DeleteENI(ctx context.Context, vpc model.VPC, eni model.ENI) error {
	name, err := eniLinkName(eni.ShortIndex)
	if err != nil {
		return err
	}
	namespace, err := netns.Name(vpc.ShortIndex)
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	err = netns.WithHandle(ctx, namespace, func(handle *netlink.Handle) error {
		link, err := handle.LinkByName(name)
		var missing netlink.LinkNotFoundError
		if err != nil && !errors.As(err, &missing) {
			return err
		}
		if err == nil {
			record, err := parseENIAlias(link.Attrs().Alias)
			if err != nil {
				return err
			}
			if link.Type() != "veth" || record.ID != eni.ID || record.IP != eni.PrivateIP || record.MAC != eni.MACAddress {
				return fmt.Errorf("refusing foreign router ENI %s", name)
			}
			if err := handle.LinkDel(link); err != nil {
				return fmt.Errorf("delete ENI %s: %w", name, err)
			}
		}
		return applyENISourceChecks(ctx, handle)
	})
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func setENISysctls(name string) error {
	if !canonicalENILink(name) {
		return fmt.Errorf("invalid ENI sysctl target %q", name)
	}
	for key, value := range map[string]string{"proxy_arp": "1", "send_redirects": "0", "rp_filter": "0"} {
		if err := os.WriteFile("/proc/sys/net/ipv4/conf/"+name+"/"+key, []byte(value), 0o600); err != nil {
			return fmt.Errorf("set router ENI %s %s: %w", name, key, err)
		}
	}
	return nil
}
