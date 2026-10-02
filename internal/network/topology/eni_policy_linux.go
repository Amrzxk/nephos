package topology

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	"github.com/vishvananda/netlink"

	"github.com/Amrzxk/nephos/internal/model"
)

const eniPolicyComment = "nephos:m1"

type eniIdentity struct {
	ID        string
	IP        netip.Addr
	MAC       string
	Namespace uint64
}

func eniLinkName(index int64) (string, error) {
	if index < 1 || index > 9999999999999 {
		return "", fmt.Errorf("ENI index %d exceeds the kernel name budget", index)
	}
	return "ve" + strconv.FormatInt(index, 10), nil
}

func canonicalENILink(name string) bool {
	digits, ok := strings.CutPrefix(name, "ve")
	if !ok {
		return false
	}
	index, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return false
	}
	canonical, err := eniLinkName(index)
	return err == nil && canonical == name
}

func eniAlias(eni model.ENI, inode uint64) string {
	return fmt.Sprintf("nephos:eni:%s %s %s %d", eni.ID, eni.PrivateIP, eni.MACAddress, inode)
}

func parseENIAlias(alias string) (eniIdentity, error) {
	fields := strings.Fields(alias)
	if len(fields) != 4 {
		return eniIdentity{}, fmt.Errorf("foreign ENI ownership marker %q", alias)
	}
	id, ok := strings.CutPrefix(fields[0], "nephos:eni:")
	if !ok || len(id) != 21 || !strings.HasPrefix(id, "eni-") {
		return eniIdentity{}, fmt.Errorf("invalid ENI ownership ID")
	}
	for _, ch := range id[4:] {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return eniIdentity{}, fmt.Errorf("invalid ENI ownership ID")
		}
	}
	ip, err := netip.ParseAddr(fields[1])
	if err != nil {
		return eniIdentity{}, fmt.Errorf("decode ENI marker IP: %w", err)
	}
	if !ip.Is4() {
		return eniIdentity{}, fmt.Errorf("ENI marker IP is not IPv4")
	}
	mac, err := net.ParseMAC(fields[2])
	if err != nil {
		return eniIdentity{}, fmt.Errorf("decode ENI marker MAC: %w", err)
	}
	if len(mac) != 6 || mac[0]&3 != 2 {
		return eniIdentity{}, fmt.Errorf("invalid ENI marker MAC")
	}
	inode, err := strconv.ParseUint(fields[3], 10, 64)
	if err != nil {
		return eniIdentity{}, fmt.Errorf("decode ENI namespace inode: %w", err)
	}
	if inode == 0 {
		return eniIdentity{}, fmt.Errorf("invalid ENI namespace inode")
	}
	return eniIdentity{ID: id, IP: ip, MAC: mac.String(), Namespace: inode}, nil
}

// renderENISourceChecks is pure and replaces only the M1-owned table atomically.
func renderENISourceChecks(records map[string]eniIdentity, exists bool) (string, error) {
	names := make([]string, 0, len(records))
	for name, record := range records {
		if !canonicalENILink(name) || !record.IP.Is4() {
			return "", fmt.Errorf("invalid source-check interface or address %q", name)
		}
		names = append(names, name)
	}
	sort.Strings(names)
	var b strings.Builder
	if exists {
		b.WriteString("delete table inet nephos\n")
	}
	b.WriteString("table inet nephos {\n comment \"" + eniPolicyComment + "\"\n set eni_sources {\n type ifname . ipv4_addr\n")
	if len(names) > 0 {
		b.WriteString(" elements = { ")
		for i, name := range names {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "%q . %s", name, records[name].IP)
		}
		b.WriteString(" }\n")
	}
	b.WriteString(" }\n chain source_check {\n type filter hook prerouting priority -300; policy accept;\n iifname . ip saddr @eni_sources return\n iifname \"ve*\" counter drop comment \"nephos:anti-spoof\"\n }\n}\n")
	return b.String(), nil
}

func applyENISourceChecks(ctx context.Context, handle *netlink.Handle) error {
	links, err := handle.LinkList()
	if err != nil {
		return fmt.Errorf("list router ENIs: %w", err)
	}
	records := make(map[string]eniIdentity)
	for _, link := range links {
		if !strings.HasPrefix(link.Attrs().Name, "ve") {
			continue
		}
		if !canonicalENILink(link.Attrs().Name) || link.Type() != "veth" {
			return fmt.Errorf("refusing foreign ENI link %q", link.Attrs().Name)
		}
		record, err := parseENIAlias(link.Attrs().Alias)
		if err != nil {
			return err
		}
		records[link.Attrs().Name] = record
	}
	// This runs only inside the VPC namespace on netns' disposable thread.
	command := exec.CommandContext(ctx, "nft", "-j", "list", "ruleset")
	output, err := command.Output()
	if err != nil {
		return fmt.Errorf("inspect VPC nftables: %w", err)
	}
	var rules struct {
		Nftables []struct {
			Table *struct{ Family, Name, Comment string }
		}
	}
	if err := json.Unmarshal(output, &rules); err != nil {
		return fmt.Errorf("decode VPC nftables: %w", err)
	}
	exists := false
	for _, entry := range rules.Nftables {
		if entry.Table != nil && entry.Table.Family == "inet" && entry.Table.Name == "nephos" {
			if entry.Table.Comment != eniPolicyComment {
				return fmt.Errorf("refusing foreign nftables table inet nephos")
			}
			exists = true
		}
	}
	script, err := renderENISourceChecks(records, exists)
	if err != nil {
		return err
	}
	command = exec.CommandContext(ctx, "nft", "-f", "-")
	command.Stdin = strings.NewReader(script)
	output, err = command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("apply ENI source checks: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}
