package main

// The nftables renderer: desired state in, ruleset text out.
//
// This is the prototype of internal/network/firewall's renderer, and it obeys
// the constraint that package will be held to (ARCHITECTURE §13): it is PURE.
// No I/O, no clock, no randomness, no map iteration order leaking into output.
// That is what makes it golden-testable, and what lets `explain` and the data
// plane share one semantics catalog rather than drifting apart (RISKS T3).
//
// Rule evaluation order is ADR-0005's, and the order is the whole point:
//
//	1. anti-spoof (the source/dest check)
//	2. NACLs — stateless, and ONLY when crossing a subnet boundary
//	3. ct state established,related accept — security groups are stateful
//	4. source ENI's SG egress, then destination ENI's SG ingress
//	5. accept
//
// Every emitted rule carries a counter and a comment holding its Nephos rule
// ID, which is how `explain` and flow logs name the exact rule responsible.

import (
	"fmt"
	"sort"
	"strings"
)

// Protocol numbers as AWS spells them in rules: "-1" means every protocol.
const protocolAll = "-1"

// SGRule is one security group rule. Security groups are allow-only.
type SGRule struct {
	ID       string // sgr-...
	Protocol string // tcp, udp, icmp, or -1
	FromPort int
	ToPort   int
	CIDR     string // mutually exclusive with SourceSG
	SourceSG string // an SG reference, rendered as a named set
}

// NACLRule is one network ACL entry. Network ACLs are stateless and ordered.
type NACLRule struct {
	ID         string // acl-... rule
	RuleNumber int
	Egress     bool
	Protocol   string
	FromPort   int
	ToPort     int
	CIDR       string
	Allow      bool
}

// ENI is a network interface attached to a subnet, with security groups.
type ENI struct {
	ID         string // eni-...
	Iface      string // the router-side veth, e.g. ve1
	PrivateIP  string
	SubnetName string
	Ingress    []SGRule
	Egress     []SGRule
}

// Subnet groups ENIs and carries the network ACL applied at its boundary.
type Subnet struct {
	Name  string // short name used for set and chain names
	CIDR  string
	NACL  []NACLRule
	Gway  string // the .1 address on the router's dummy interface
	Reeps []string
}

// VPCDesired is everything the renderer needs. Passed by value: a renderer
// must not be able to mutate what its caller holds.
type VPCDesired struct {
	Name       string
	CIDR       string
	Subnets    []Subnet
	ENIs       []ENI
	ResolverIP string // base+2
	IGWIface   string // the veth to nx-edge, empty when no gateway is attached
	NAT1to1    []NATMapping
}

// NATMapping is the internet gateway's 1:1 translation between a private
// address and a public or Elastic IP.
type NATMapping struct {
	ID        string // eipassoc-...
	PrivateIP string
	PublicIP  string
}

// Render turns desired state into an nftables ruleset.
//
// Deterministic by construction: every collection is sorted before it is
// emitted, so the same input always produces byte-identical output. Golden
// tests depend on that, and so does the atomic-replacement property — a
// ruleset that reordered itself at random would churn the kernel on every
// reconcile.
func Render(v VPCDesired) string {
	var b strings.Builder

	enis := append([]ENI(nil), v.ENIs...)
	sort.Slice(enis, func(i, j int) bool { return enis[i].Iface < enis[j].Iface })
	subnets := append([]Subnet(nil), v.Subnets...)
	sort.Slice(subnets, func(i, j int) bool { return subnets[i].Name < subnets[j].Name })

	fmt.Fprintf(&b, "table inet nephos {\n")

	renderSubnetSets(&b, subnets, enis)
	renderSGMaps(&b, enis)
	renderNAT(&b, v)
	renderForward(&b, subnets, enis)
	renderSGChains(&b, enis)
	renderNACLChains(&b, subnets)
	renderInput(&b, v)

	fmt.Fprintf(&b, "}\n")
	return b.String()
}

// A subnet is a logical grouping, not a bridge: membership is an nftables set
// of interface names (ARCHITECTURE §5.1).
func renderSubnetSets(b *strings.Builder, subnets []Subnet, enis []ENI) {
	for _, s := range subnets {
		members := make([]string, 0, len(enis))
		for _, e := range enis {
			if e.SubnetName == s.Name {
				members = append(members, fmt.Sprintf("%q", e.Iface))
			}
		}
		sort.Strings(members)
		fmt.Fprintf(b, "  set sn_%s {\n    type ifname\n", s.Name)
		if len(members) > 0 {
			fmt.Fprintf(b, "    elements = { %s }\n", strings.Join(members, ", "))
		}
		fmt.Fprintf(b, "  }\n")
	}
}

func renderSGMaps(b *strings.Builder, enis []ENI) {
	var in, out []string
	for _, e := range enis {
		out = append(out, fmt.Sprintf("%q : jump sg_out_%s", e.Iface, e.Iface))
		in = append(in, fmt.Sprintf("%q : jump sg_in_%s", e.Iface, e.Iface))
	}
	fmt.Fprintf(b, "  map sg_egress_by_eni {\n    type ifname : verdict\n")
	if len(out) > 0 {
		fmt.Fprintf(b, "    elements = { %s }\n", strings.Join(out, ", "))
	}
	fmt.Fprintf(b, "  }\n")
	fmt.Fprintf(b, "  map sg_ingress_by_eni {\n    type ifname : verdict\n")
	if len(in) > 0 {
		fmt.Fprintf(b, "    elements = { %s }\n", strings.Join(in, ", "))
	}
	fmt.Fprintf(b, "  }\n")
}

// The internet gateway's 1:1 NAT runs INSIDE the VPC namespace, on the gateway
// link, so private addresses never reach nx-edge (ADR-0005).
func renderNAT(b *strings.Builder, v VPCDesired) {
	mappings := append([]NATMapping(nil), v.NAT1to1...)
	sort.Slice(mappings, func(i, j int) bool { return mappings[i].PrivateIP < mappings[j].PrivateIP })

	fmt.Fprintf(b, "  chain prerouting {\n    type nat hook prerouting priority dstnat; policy accept;\n")
	for _, m := range mappings {
		fmt.Fprintf(b, "    iifname %q ip daddr %s dnat ip to %s comment \"nephos:%s\"\n",
			v.IGWIface, m.PublicIP, m.PrivateIP, m.ID)
	}
	fmt.Fprintf(b, "  }\n")

	fmt.Fprintf(b, "  chain postrouting {\n    type nat hook postrouting priority srcnat; policy accept;\n")
	for _, m := range mappings {
		fmt.Fprintf(b, "    oifname %q ip saddr %s snat ip to %s comment \"nephos:%s\"\n",
			v.IGWIface, m.PrivateIP, m.PublicIP, m.ID)
	}
	fmt.Fprintf(b, "  }\n")
}

func renderForward(b *strings.Builder, subnets []Subnet, enis []ENI) {
	fmt.Fprintf(b, "  chain antispoof {\n")
	for _, e := range enis {
		// The source/dest check. Anything arriving on an ENI claiming an
		// address that is not its own is a spoof and is dropped.
		fmt.Fprintf(b, "    iifname %q ip saddr != %s counter drop comment \"nephos:srcdstcheck:%s\"\n",
			e.Iface, e.PrivateIP, e.ID)
	}
	fmt.Fprintf(b, "  }\n")

	fmt.Fprintf(b, "  chain forward {\n")
	fmt.Fprintf(b, "    type filter hook forward priority filter; policy drop;\n")
	fmt.Fprintf(b, "    jump antispoof\n")

	// NACLs apply ONLY when traffic crosses a subnet boundary. This is the
	// single most misunderstood thing about them, and the iifname/oifname set
	// comparison is what encodes it.
	for _, s := range subnets {
		fmt.Fprintf(b, "    iifname @sn_%s oifname != @sn_%s jump nacl_out_%s\n", s.Name, s.Name, s.Name)
		fmt.Fprintf(b, "    oifname @sn_%s iifname != @sn_%s jump nacl_in_%s\n", s.Name, s.Name, s.Name)
	}

	// Security groups are stateful; network ACLs above are not, which is why
	// conntrack is consulted only after the NACLs have had their say.
	fmt.Fprintf(b, "    ct state established,related accept\n")
	fmt.Fprintf(b, "    ct state invalid drop\n")
	fmt.Fprintf(b, "    iifname vmap @sg_egress_by_eni\n")
	fmt.Fprintf(b, "    oifname vmap @sg_ingress_by_eni\n")
	fmt.Fprintf(b, "    accept\n")
	fmt.Fprintf(b, "  }\n")
}

// Security groups are allow-only: a match returns, and each chain ends in a
// drop. Blocked traffic is dropped rather than rejected, so connections time
// out exactly as they do in AWS.
func renderSGChains(b *strings.Builder, enis []ENI) {
	for _, e := range enis {
		fmt.Fprintf(b, "  chain sg_in_%s {\n", e.Iface)
		for _, r := range sortedSGRules(e.Ingress) {
			fmt.Fprintf(b, "    %s counter return comment \"nephos:%s\"\n", sgMatch(r, true), r.ID)
		}
		fmt.Fprintf(b, "    counter drop comment \"nephos:sg-default-deny:%s:ingress\"\n  }\n", e.ID)

		fmt.Fprintf(b, "  chain sg_out_%s {\n", e.Iface)
		for _, r := range sortedSGRules(e.Egress) {
			fmt.Fprintf(b, "    %s counter return comment \"nephos:%s\"\n", sgMatch(r, false), r.ID)
		}
		fmt.Fprintf(b, "    counter drop comment \"nephos:sg-default-deny:%s:egress\"\n  }\n", e.ID)
	}
}

// Network ACLs are stateless and evaluated in rule-number order: allow is a
// return, deny is a drop, and an unmatched packet hits the final drop. A
// custom NACL therefore denies everything until rules are added.
func renderNACLChains(b *strings.Builder, subnets []Subnet) {
	for _, s := range subnets {
		for _, egress := range []bool{false, true} {
			name := "nacl_in_" + s.Name
			if egress {
				name = "nacl_out_" + s.Name
			}
			fmt.Fprintf(b, "  chain %s {\n", name)
			rules := make([]NACLRule, 0, len(s.NACL))
			for _, r := range s.NACL {
				if r.Egress == egress {
					rules = append(rules, r)
				}
			}
			sort.Slice(rules, func(i, j int) bool { return rules[i].RuleNumber < rules[j].RuleNumber })
			for _, r := range rules {
				verdict := "drop"
				if r.Allow {
					verdict = "return"
				}
				fmt.Fprintf(b, "    %s counter %s comment \"nephos:%s:%d\"\n",
					naclMatch(r), verdict, r.ID, r.RuleNumber)
			}
			fmt.Fprintf(b, "    counter drop comment \"nephos:nacl-default-deny:%s\"\n  }\n", s.Name)
		}
	}
}

// Security groups and network ACLs do not filter VPC DNS or instance metadata
// traffic, as in AWS. Modelling that faithfully matters: a learner who blocks
// all egress must still be able to resolve names.
func renderInput(b *strings.Builder, v VPCDesired) {
	fmt.Fprintf(b, "  chain input {\n    type filter hook input priority filter; policy drop;\n")
	fmt.Fprintf(b, "    iif lo accept\n")
	fmt.Fprintf(b, "    ct state established,related accept\n")
	if v.ResolverIP != "" {
		fmt.Fprintf(b, "    ip daddr { %s, 169.254.169.253 } udp dport 53 counter accept comment \"nephos:vpc-dns\"\n", v.ResolverIP)
		fmt.Fprintf(b, "    ip daddr { %s, 169.254.169.253 } tcp dport 53 counter accept comment \"nephos:vpc-dns\"\n", v.ResolverIP)
	}
	fmt.Fprintf(b, "    ip daddr 169.254.169.254 tcp dport 80 counter accept comment \"nephos:imds\"\n")
	fmt.Fprintf(b, "    icmp type echo-request counter accept comment \"nephos:router-icmp\"\n")
	fmt.Fprintf(b, "  }\n")
}

func sortedSGRules(rules []SGRule) []SGRule {
	out := append([]SGRule(nil), rules...)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func sgMatch(r SGRule, ingress bool) string {
	var parts []string
	if r.CIDR != "" && r.CIDR != "0.0.0.0/0" {
		if ingress {
			parts = append(parts, "ip saddr "+r.CIDR)
		} else {
			parts = append(parts, "ip daddr "+r.CIDR)
		}
	}
	if r.SourceSG != "" {
		// SG references become named IP sets (ADR-0005).
		if ingress {
			parts = append(parts, "ip saddr @sgref_"+r.SourceSG)
		} else {
			parts = append(parts, "ip daddr @sgref_"+r.SourceSG)
		}
	}
	parts = append(parts, protoMatch(r.Protocol, r.FromPort, r.ToPort)...)
	if len(parts) == 0 {
		return "meta l4proto { tcp, udp, icmp }"
	}
	return strings.Join(parts, " ")
}

func naclMatch(r NACLRule) string {
	var parts []string
	if r.CIDR != "" && r.CIDR != "0.0.0.0/0" {
		if r.Egress {
			parts = append(parts, "ip daddr "+r.CIDR)
		} else {
			parts = append(parts, "ip saddr "+r.CIDR)
		}
	}
	parts = append(parts, protoMatch(r.Protocol, r.FromPort, r.ToPort)...)
	if len(parts) == 0 {
		return "meta l4proto { tcp, udp, icmp }"
	}
	return strings.Join(parts, " ")
}

func protoMatch(proto string, from, to int) []string {
	switch proto {
	case protocolAll, "":
		return nil
	case "icmp":
		return []string{"ip protocol icmp"}
	case "tcp", "udp":
		if from == 0 && to == 0 {
			return []string{proto}
		}
		if from == to {
			return []string{fmt.Sprintf("%s dport %d", proto, from)}
		}
		return []string{fmt.Sprintf("%s dport %d-%d", proto, from, to)}
	default:
		return []string{"ip protocol " + proto}
	}
}
