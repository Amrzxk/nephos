package main

// Small wrappers over netlink and per-namespace sysctls.
//
// Every one of these must already be running inside the target namespace (see
// Do in netns.go). They are idempotent on purpose: ADR-0007 requires every
// engine operation to converge rather than fail when the object already
// exists, because reconcilers run the same code on every resync.

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
)

// isExist reports whether an error means "already there", which for an
// idempotent operation is success.
func isExist(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, os.ErrExist) || errors.Is(err, syscall.EEXIST) {
		return true
	}
	return strings.Contains(err.Error(), "file exists")
}

// sysctlSet writes a PER-NAMESPACE sysctl.
//
// The caller must already be inside the target network namespace. Nephos never
// writes a kernel-global sysctl: not net/ipv4/conf/all/*, not
// net/ipv4/conf/default/* outside its own namespaces, and never anything under
// /sys/module. That is ADR-0003's promise and RISKS S1's early-warning signal,
// so the guard is here rather than in a comment.
func sysctlSet(key, value string) error {
	if strings.HasPrefix(key, "net/ipv4/conf/all/") {
		return fmt.Errorf("refusing to write the kernel-global sysctl %q: Nephos changes per-namespace settings only (ADR-0003)", key)
	}
	path := "/proc/sys/" + key
	if err := os.WriteFile(path, []byte(value), 0o644); err != nil {
		return fmt.Errorf("setting sysctl %s=%s: %w", key, value, err)
	}
	return nil
}

func sysctlGet(key string) (string, error) {
	data, err := os.ReadFile("/proc/sys/" + key)
	if err != nil {
		return "", fmt.Errorf("reading sysctl %s: %w", key, err)
	}
	return strings.TrimSpace(string(data)), nil
}

func addAddr(link netlink.Link, cidr string) error {
	addr, err := netlink.ParseAddr(cidr)
	if err != nil {
		return fmt.Errorf("parsing address %q: %w", cidr, err)
	}
	if err := netlink.AddrAdd(link, addr); err != nil && !isExist(err) {
		return fmt.Errorf("adding address %s to %s: %w", cidr, link.Attrs().Name, err)
	}
	return nil
}

// addRoute adds an on-link route, optionally into a specific routing table.
// Table 0 means the main table.
func addRoute(link netlink.Link, cidr string, table int) error {
	_, dst, err := net.ParseCIDR(cidr)
	if err != nil {
		return fmt.Errorf("parsing route destination %q: %w", cidr, err)
	}
	route := &netlink.Route{
		LinkIndex: link.Attrs().Index,
		Dst:       dst,
		Scope:     netlink.SCOPE_LINK,
	}
	if table != 0 {
		route.Table = table
	}
	if err := netlink.RouteAdd(route); err != nil && !isExist(err) {
		return fmt.Errorf("adding route %s via %s: %w", cidr, link.Attrs().Name, err)
	}
	return nil
}

// moveLinkToNamespace moves an interface into a named network namespace. The
// link must currently be visible to the caller.
func moveLinkToNamespace(name, nsName string) error {
	link, err := netlink.LinkByName(name)
	if err != nil {
		return fmt.Errorf("finding link %s: %w", name, err)
	}
	handle, err := netns.GetFromPath("/run/netns/" + nsName)
	if err != nil {
		return fmt.Errorf("opening namespace %s: %w", nsName, err)
	}
	defer handle.Close()

	if err := netlink.LinkSetNsFd(link, int(handle)); err != nil {
		return fmt.Errorf("moving %s into %s: %w", name, nsName, err)
	}
	return nil
}

// maskLen returns the prefix length of a CIDR, defaulting to /24 for input
// that cannot be parsed. The spike controls its own inputs; the real IPAM
// validates them up front and returns an AWS-style error.
func maskLen(cidr string) int {
	parts := strings.Split(cidr, "/")
	if len(parts) != 2 {
		return 24
	}
	n, err := strconv.Atoi(parts[1])
	if err != nil {
		return 24
	}
	return n
}

// LinkNamesIn lists the interfaces present in a namespace. Used to prove that
// the appliance's root namespace holds nothing but its uplink
// (ADR-0005 validation 6).
func LinkNamesIn(nsName string) ([]string, error) {
	var names []string
	err := Do(nsName, func() error {
		links, err := netlink.LinkList()
		if err != nil {
			return fmt.Errorf("listing links: %w", err)
		}
		for _, l := range links {
			names = append(names, l.Attrs().Name)
		}
		return nil
	})
	return names, err
}

// RootLinkNames lists the interfaces in the caller's current namespace, which
// inside the appliance is the appliance root namespace.
func RootLinkNames() ([]string, error) {
	links, err := netlink.LinkList()
	if err != nil {
		return nil, fmt.Errorf("listing links: %w", err)
	}
	names := make([]string, 0, len(links))
	for _, l := range links {
		names = append(names, l.Attrs().Name)
	}
	return names, nil
}
