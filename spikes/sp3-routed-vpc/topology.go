package main

// Building the routed network plane: namespaces, veth pairs, proxy ARP, policy
// routing (ADR-0005, ARCHITECTURE §5).
//
// The shape being validated: there is NO per-subnet bridge. Every interface
// connects directly to its VPC router namespace, so every packet — including
// same-subnet traffic — crosses one enforcement point, exactly as it does
// through the AWS hypervisor. That is what makes same-subnet security groups
// work, and it is why Option C was chosen over a bridge per subnet.

import (
	"fmt"
	"net"
	"os/exec"
	"strings"

	"github.com/vishvananda/netlink"
)

// Routing table IDs are 1000 + index, matching ARCHITECTURE §5.1.
const routeTableBase = 1000

// Policy rule priorities encode ADR-0005's ordering. The local route must be
// consulted first, then per-interface tables, and anything unmatched must be
// silently blackholed rather than leaking to a default route.
const (
	prioLocal     = 100
	prioPerENI    = 200
	prioBlackhole = 32000
)

// Instance is a spike stand-in for a real instance: a namespace holding the
// far end of an ENI's veth pair. SP1 and SP2 cover real containers; SP3 only
// needs something that owns an address and can open sockets.
type Instance struct {
	Name      string
	Namespace string
	ENI       ENI
	Subnet    Subnet
}

// VPC is a built VPC: its namespace plus the instances attached to it.
type VPC struct {
	Desired   VPCDesired
	Namespace string
	Instances []Instance
}

// BuildVPC creates the VPC router namespace and wires every ENI into it.
func BuildVPC(v VPCDesired, instances []Instance) (*VPC, error) {
	ns := "nx-vpc-" + v.Name
	if err := Create(ns); err != nil {
		return nil, fmt.Errorf("creating the VPC namespace: %w", err)
	}

	built := &VPC{Desired: v, Namespace: ns, Instances: instances}

	// Forwarding and proxy ARP are set per namespace only. Nothing global is
	// ever written: ADR-0005's host-hygiene rule, and RISKS S1's early warning.
	if err := Do(ns, func() error {
		if err := sysctlSet("net/ipv4/ip_forward", "1"); err != nil {
			return err
		}
		return setupRouterAddresses(v)
	}); err != nil {
		return nil, err
	}

	for i := range built.Instances {
		if err := built.attachENI(&built.Instances[i]); err != nil {
			return nil, fmt.Errorf("attaching %s: %w", built.Instances[i].Name, err)
		}
	}

	if err := built.applyPolicyRouting(); err != nil {
		return nil, err
	}
	return built, nil
}

// The router's addresses — each subnet's .1 and the VPC resolver — live on a
// dummy interface, not on any veth. That keeps them reachable from every
// subnet regardless of which ENIs exist.
func setupRouterAddresses(v VPCDesired) error {
	dummy := &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: "nxr0"}}
	if err := netlink.LinkAdd(dummy); err != nil && !isExist(err) {
		return fmt.Errorf("creating the router dummy interface: %w", err)
	}
	link, err := netlink.LinkByName("nxr0")
	if err != nil {
		return fmt.Errorf("finding the router dummy interface: %w", err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		return fmt.Errorf("bringing up the router dummy interface: %w", err)
	}

	for _, s := range v.Subnets {
		if err := addAddr(link, s.Gway+"/32"); err != nil {
			return err
		}
	}
	for _, extra := range []string{v.ResolverIP, "169.254.169.253", "169.254.169.254"} {
		if extra == "" {
			continue
		}
		if err := addAddr(link, extra+"/32"); err != nil {
			return err
		}
	}
	return nil
}

// attachENI creates the veth pair for one interface: the router end stays in
// the VPC namespace with proxy ARP and a /32 route, and the instance end
// becomes eth0 inside the instance namespace (ADR-0005).
func (v *VPC) attachENI(inst *Instance) error {
	if err := Create(inst.Namespace); err != nil {
		return err
	}

	routerSide := inst.ENI.Iface
	instSide := routerSide + "p"

	// Created in the current namespace, then moved: both ends have to exist
	// somewhere first.
	veth := &netlink.Veth{
		LinkAttrs: netlink.LinkAttrs{Name: routerSide, MTU: 1500},
		PeerName:  instSide,
	}
	_ = netlink.LinkDel(&netlink.Veth{LinkAttrs: netlink.LinkAttrs{Name: routerSide}})
	if err := netlink.LinkAdd(veth); err != nil {
		return fmt.Errorf("creating veth %s: %w", routerSide, err)
	}

	if err := moveLinkToNamespace(routerSide, v.Namespace); err != nil {
		return err
	}
	if err := moveLinkToNamespace(instSide, inst.Namespace); err != nil {
		return err
	}

	// Router end: proxy ARP plus a /32 route to the instance. There is no
	// layer-2 domain, so the router answers ARP for every address the
	// instance asks about, and the /32 is what actually forwards the packet.
	if err := Do(v.Namespace, func() error {
		link, err := netlink.LinkByName(routerSide)
		if err != nil {
			return fmt.Errorf("finding %s in the VPC namespace: %w", routerSide, err)
		}
		if err := netlink.LinkSetUp(link); err != nil {
			return fmt.Errorf("bringing up %s: %w", routerSide, err)
		}
		if err := sysctlSet("net/ipv4/conf/"+routerSide+"/proxy_arp", "1"); err != nil {
			return err
		}
		// Redirects would let the router tell an instance to bypass it, which
		// would bypass enforcement.
		if err := sysctlSet("net/ipv4/conf/"+routerSide+"/send_redirects", "0"); err != nil {
			return err
		}
		return addRoute(link, inst.ENI.PrivateIP+"/32", 0)
	}); err != nil {
		return err
	}

	// Instance end: the subnet prefix and a default route via the subnet's .1.
	return Do(inst.Namespace, func() error {
		link, err := netlink.LinkByName(instSide)
		if err != nil {
			return fmt.Errorf("finding %s in the instance namespace: %w", instSide, err)
		}
		if err := netlink.LinkSetName(link, "eth0"); err != nil {
			return fmt.Errorf("renaming %s to eth0: %w", instSide, err)
		}
		link, err = netlink.LinkByName("eth0")
		if err != nil {
			return fmt.Errorf("finding eth0: %w", err)
		}
		if err := netlink.LinkSetUp(link); err != nil {
			return fmt.Errorf("bringing up eth0: %w", err)
		}
		if lo, err := netlink.LinkByName("lo"); err == nil {
			_ = netlink.LinkSetUp(lo)
		}

		prefixLen := maskLen(inst.Subnet.CIDR)
		if err := addAddr(link, fmt.Sprintf("%s/%d", inst.ENI.PrivateIP, prefixLen)); err != nil {
			return err
		}
		// The gateway is outside the instance's own /32 view, so it needs an
		// explicit on-link route before it can be used as a default gateway.
		if err := addRoute(link, inst.Subnet.Gway+"/32", 0); err != nil {
			return err
		}
		return netlink.RouteAdd(&netlink.Route{
			LinkIndex: link.Attrs().Index,
			Gw:        net.ParseIP(inst.Subnet.Gway),
		})
	})
}

// applyPolicyRouting installs ADR-0005's rule order:
//
//	100   to <VPC CIDR> lookup main     — the local route, always first
//	200   iif <ENI> lookup <table>      — one per interface
//	32000 blackhole                     — silent drop, never a default route
//
// The local route coming first is what makes every subnet in a VPC able to
// reach every other, and why it cannot be overridden by a more specific route.
func (v *VPC) applyPolicyRouting() error {
	return Do(v.Namespace, func() error {
		_, vpcNet, err := net.ParseCIDR(v.Desired.CIDR)
		if err != nil {
			return fmt.Errorf("parsing the VPC CIDR: %w", err)
		}

		localRule := netlink.NewRule()
		localRule.Priority = prioLocal
		localRule.Dst = vpcNet
		localRule.Table = 254 // main
		if err := netlink.RuleAdd(localRule); err != nil && !isExist(err) {
			return fmt.Errorf("adding the local-route rule: %w", err)
		}

		for i, inst := range v.Instances {
			table := routeTableBase + i + 1
			rule := netlink.NewRule()
			rule.Priority = prioPerENI
			rule.IifName = inst.ENI.Iface
			rule.Table = table
			if err := netlink.RuleAdd(rule); err != nil && !isExist(err) {
				return fmt.Errorf("adding the policy rule for %s: %w", inst.ENI.Iface, err)
			}
		}

		blackhole := netlink.NewRule()
		blackhole.Priority = prioBlackhole
		blackhole.Table = 254
		blackhole.Type = 6 // RTN_BLACKHOLE: unmatched traffic goes nowhere, silently
		if err := netlink.RuleAdd(blackhole); err != nil && !isExist(err) {
			return fmt.Errorf("adding the catch-all blackhole rule: %w", err)
		}
		return nil
	})
}

// ApplyRuleset renders the desired state and replaces the whole table
// atomically with `nft -f`. Atomic replacement is what ADR-0005 validation 4
// tests: during 100 consecutive replacements a denied flow must never be
// briefly allowed.
func (v *VPC) ApplyRuleset(ruleset string) error {
	script := "table inet nephos\ndelete table inet nephos\n" + ruleset
	return Do(v.Namespace, func() error {
		cmd := exec.Command("nft", "-f", "-")
		cmd.Stdin = strings.NewReader(script)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("applying the ruleset: %w: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	})
}

// Teardown removes every namespace this VPC owns. Only names carrying the
// Nephos prefix are ever touched (ADR-0007).
func (v *VPC) Teardown() error {
	var firstErr error
	for _, inst := range v.Instances {
		if err := Delete(inst.Namespace); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if err := Delete(v.Namespace); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}
