package main

// Tests for the pure renderer.
//
// These need no kernel, no root, and no namespaces, which is the point: the
// real internal/network/firewall renderer will be testable the same way, and
// that is what lets the semantics be pinned down cheaply and exactly
// (ARCHITECTURE §11, RISKS T3).

import (
	"strings"
	"testing"
)

func sampleVPC() VPCDesired {
	return VPCDesired{
		Name:       "main",
		CIDR:       "10.0.0.0/16",
		ResolverIP: "10.0.0.2",
		IGWIface:   "ig1",
		Subnets: []Subnet{
			{Name: "pub", CIDR: "10.0.1.0/24", Gway: "10.0.1.1", NACL: []NACLRule{
				{ID: "acl-1", RuleNumber: 100, Protocol: "tcp", FromPort: 22, ToPort: 22, CIDR: "198.51.100.10/32", Allow: true},
				{ID: "acl-1", RuleNumber: 200, Protocol: protocolAll, CIDR: "0.0.0.0/0", Allow: false},
			}},
			{Name: "priv", CIDR: "10.0.2.0/24", Gway: "10.0.2.1"},
		},
		ENIs: []ENI{
			{ID: "eni-web", Iface: "ve1", PrivateIP: "10.0.1.10", SubnetName: "pub",
				Ingress: []SGRule{{ID: "sgr-ssh", Protocol: "tcp", FromPort: 22, ToPort: 22, CIDR: "198.51.100.10/32"}},
				Egress:  []SGRule{{ID: "sgr-all", Protocol: protocolAll, CIDR: "0.0.0.0/0"}}},
			{ID: "eni-app", Iface: "ve3", PrivateIP: "10.0.2.10", SubnetName: "priv",
				Ingress: []SGRule{{ID: "sgr-ref", Protocol: "tcp", FromPort: 8080, ToPort: 8080, SourceSG: "sg-web"}},
				Egress:  []SGRule{{ID: "sgr-all", Protocol: protocolAll, CIDR: "0.0.0.0/0"}}},
		},
		NAT1to1: []NATMapping{{ID: "eipassoc-1", PrivateIP: "10.0.1.10", PublicIP: "203.0.113.10"}},
	}
}

// Determinism is a hard requirement, not a nicety: golden tests depend on it,
// and a ruleset that reordered itself would churn the kernel on every reconcile.
func TestRenderIsDeterministic(t *testing.T) {
	v := sampleVPC()
	first := Render(v)
	for i := 0; i < 20; i++ {
		if got := Render(v); got != first {
			t.Fatalf("Render is not deterministic; run %d differed", i)
		}
	}
}

func TestRenderEvaluationOrder(t *testing.T) {
	out := Render(sampleVPC())

	// ADR-0005's forward-chain order. Getting this wrong is how a simulator
	// ends up teaching the wrong thing, so assert the order, not just presence.
	steps := []string{
		"jump antispoof",
		"jump nacl_out_pub",
		"ct state established,related accept",
		"iifname vmap @sg_egress_by_eni",
		"oifname vmap @sg_ingress_by_eni",
	}
	last := -1
	for _, s := range steps {
		idx := strings.Index(out, s)
		if idx < 0 {
			t.Fatalf("rendered ruleset is missing %q\n%s", s, out)
		}
		if idx < last {
			t.Errorf("%q appears out of order in the forward chain", s)
		}
		last = idx
	}
}

func TestRenderNACLOnlyAtSubnetBoundary(t *testing.T) {
	out := Render(sampleVPC())

	// The defining property of a network ACL: it applies ONLY when traffic
	// crosses a subnet boundary. Traffic staying inside a subnet must not be
	// matched, which is what the "oifname != @sn_x" half encodes.
	for _, want := range []string{
		`iifname @sn_pub oifname != @sn_pub jump nacl_out_pub`,
		`oifname @sn_pub iifname != @sn_pub jump nacl_in_pub`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered ruleset is missing the subnet-boundary guard %q", want)
		}
	}
}

func TestRenderSecurityGroupDefaults(t *testing.T) {
	out := Render(sampleVPC())

	// Security groups are allow-only: a match returns, and the chain ends in a
	// drop. Dropping (not rejecting) is what makes blocked traffic time out,
	// which is the behaviour a learner has to be able to recognise.
	if !strings.Contains(out, `tcp dport 22 counter return comment "nephos:sgr-ssh"`) &&
		!strings.Contains(out, `ip saddr 198.51.100.10/32 tcp dport 22 counter return comment "nephos:sgr-ssh"`) {
		t.Errorf("expected an allow rule rendered as a return with its rule ID\n%s", out)
	}
	if !strings.Contains(out, `counter drop comment "nephos:sg-default-deny:eni-web:ingress"`) {
		t.Error("expected every security group chain to end in a drop")
	}
	if strings.Contains(out, "reject") {
		t.Error("security groups must drop rather than reject, so connections time out as in AWS")
	}
}

func TestRenderNACLRuleOrder(t *testing.T) {
	v := sampleVPC()
	// Deliberately out of order: the renderer must sort by rule number.
	v.Subnets[0].NACL = []NACLRule{
		{ID: "acl-1", RuleNumber: 200, Protocol: protocolAll, CIDR: "0.0.0.0/0", Allow: false},
		{ID: "acl-1", RuleNumber: 100, Protocol: "tcp", FromPort: 22, ToPort: 22, CIDR: "0.0.0.0/0", Allow: true},
	}
	out := Render(v)

	allow := strings.Index(out, `comment "nephos:acl-1:100"`)
	deny := strings.Index(out, `comment "nephos:acl-1:200"`)
	if allow < 0 || deny < 0 {
		t.Fatalf("both NACL rules should be rendered\n%s", out)
	}
	if allow > deny {
		t.Error("NACL rules must be emitted in rule-number order: a lower-numbered allow has to win")
	}
}

func TestRenderEveryRuleIsTraceable(t *testing.T) {
	out := Render(sampleVPC())

	// ARCHITECTURE §5.2: every rendered rule carries a counter and a comment
	// holding its Nephos rule ID. `explain` and flow logs depend on it, and
	// without it Nephos could not name the rule that blocked a flow.
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.Contains(trimmed, "counter") {
			continue
		}
		if !strings.Contains(trimmed, "comment \"nephos:") {
			t.Errorf("a counted rule carries no nephos rule ID: %q", trimmed)
		}
	}
}

func TestRenderSGReferenceUsesNamedSet(t *testing.T) {
	out := Render(sampleVPC())
	if !strings.Contains(out, "@sgref_sg-web") {
		t.Errorf("a security group reference should render as a named set\n%s", out)
	}
}

func TestRenderIGWNatIsInsideTheVPC(t *testing.T) {
	out := Render(sampleVPC())

	// ADR-0005: the 1:1 NAT runs on the gateway link INSIDE the VPC namespace,
	// so private addresses never reach nx-edge.
	if !strings.Contains(out, `iifname "ig1" ip daddr 203.0.113.10 dnat ip to 10.0.1.10`) {
		t.Error("expected inbound DNAT from the public address to the private one")
	}
	if !strings.Contains(out, `oifname "ig1" ip saddr 10.0.1.10 snat ip to 203.0.113.10`) {
		t.Error("expected outbound SNAT from the private address to the public one")
	}
}

func TestRenderDNSAndIMDSBypassFirewalls(t *testing.T) {
	out := Render(sampleVPC())

	// As in AWS, security groups and network ACLs do not filter VPC DNS or
	// instance metadata. A learner who blocks all egress must still resolve
	// names, and modelling that wrongly would teach a false lesson.
	if !strings.Contains(out, "169.254.169.253") || !strings.Contains(out, "udp dport 53") {
		t.Error("expected the VPC resolver to be reachable from the input chain")
	}
	if !strings.Contains(out, "ip daddr 169.254.169.254 tcp dport 80") {
		t.Error("expected IMDS to be reachable from the input chain")
	}
}

func TestRenderAntiSpoof(t *testing.T) {
	out := Render(sampleVPC())
	if !strings.Contains(out, `iifname "ve1" ip saddr != 10.0.1.10 counter drop`) {
		t.Errorf("expected a source/dest check dropping spoofed sources\n%s", out)
	}
}

func TestRenderEmptyVPC(t *testing.T) {
	// A VPC with no interfaces must still render a valid, closed ruleset
	// rather than an empty one that would default to allowing traffic.
	out := Render(VPCDesired{Name: "empty", CIDR: "10.0.0.0/16"})
	if !strings.Contains(out, "policy drop") {
		t.Error("an empty VPC must still render a default-drop forward chain")
	}
}

func TestProtoMatch(t *testing.T) {
	tests := []struct {
		name           string
		proto          string
		from, to       int
		want           string
		wantEmptyMatch bool
	}{
		{name: "all protocols matches everything", proto: protocolAll, wantEmptyMatch: true},
		{name: "icmp has no ports", proto: "icmp", want: "ip protocol icmp"},
		{name: "single tcp port", proto: "tcp", from: 22, to: 22, want: "tcp dport 22"},
		{name: "tcp port range", proto: "tcp", from: 32768, to: 60999, want: "tcp dport 32768-60999"},
		{name: "udp single port", proto: "udp", from: 53, to: 53, want: "udp dport 53"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := protoMatch(tt.proto, tt.from, tt.to)
			if tt.wantEmptyMatch {
				if len(got) != 0 {
					t.Errorf("protoMatch(%q) = %v, want no match clause", tt.proto, got)
				}
				return
			}
			if len(got) != 1 || got[0] != tt.want {
				t.Errorf("protoMatch(%q, %d, %d) = %v, want [%q]", tt.proto, tt.from, tt.to, got, tt.want)
			}
		})
	}
}
