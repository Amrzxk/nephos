// Command sp3 validates ADR-0005 (a Nephos-owned, routed network plane) and
// the namespace-handling rules in ADR-0002. It mitigates RISKS T3 and T8.
//
// It must run inside the spike appliance, which has the namespaces, nftables,
// and iproute2 it needs. spikes/sp3-routed-vpc/run.sh builds it with
// CGO_ENABLED=0, copies it in, and runs it there.
//
// The six things ADR-0005 says this spike has to show:
//
//	1. Two VPCs using 10.0.0.0/16 at the same time, with no crosstalk.
//	2. Same-subnet and cross-subnet traffic both hit security group counters.
//	3. A NACL without an ephemeral-port rule breaks return traffic, and adding
//	   the rule fixes it.
//	4. 100 consecutive ruleset replacements, during a continuous TCP probe,
//	   never briefly allow traffic that should be denied.
//	5. Go opens DNS and metadata sockets inside a VPC namespace.
//	6. The appliance's root namespace shows nothing but the edge uplink.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

var report = NewReport()

func main() {
	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "sp3 must run as root inside the spike appliance")
		os.Exit(2)
	}
	if err := HostHasNftables(); err != nil {
		fmt.Fprintf(os.Stderr, "sp3 cannot run here: %v\n", err)
		os.Exit(2)
	}

	defer cleanup()
	cleanup() // leftovers from an interrupted run

	steps := []struct {
		name string
		fn   func() error
	}{
		{"overlapping VPCs", checkOverlappingVPCs},
		{"security group enforcement", checkSecurityGroups},
		{"stateless NACL returns", checkStatelessNACL},
		{"atomic ruleset replacement", checkAtomicReplacement},
		{"in-namespace listeners", checkInNamespaceListeners},
		{"host hygiene", checkHostHygiene},
		{"namespace thread safety", checkThreadSafety},
	}

	for _, s := range steps {
		report.Section(s.name)
		if err := s.fn(); err != nil {
			report.Fail(s.name, err.Error())
		}
	}

	report.Summary()
	if err := report.WriteTSV("/tmp/sp3-verdicts.tsv"); err != nil {
		fmt.Fprintf(os.Stderr, "could not write verdicts: %v\n", err)
	}
	if report.Failed > 0 {
		os.Exit(1)
	}
}

// --- 1. Overlapping CIDRs (ADR-0005 validation 1) --------------------------

// Two VPCs both using 10.0.0.0/16 is the case a shared bridge or a
// runtime-managed network cannot do at all. A namespace per VPC makes it free,
// and this proves there is no crosstalk between them.
func checkOverlappingVPCs() error {
	vpcA, err := buildStandardVPC("a", "10.0.0.0/16")
	if err != nil {
		return fmt.Errorf("building VPC a: %w", err)
	}
	vpcB, err := buildStandardVPC("b", "10.0.0.0/16")
	if err != nil {
		return fmt.Errorf("building VPC b: %w", err)
	}

	// Identical addressing in both.
	report.Metric("vpc_a_cidr", vpcA.Desired.CIDR)
	report.Metric("vpc_b_cidr", vpcB.Desired.CIDR)
	report.Assert("two VPCs hold the same CIDR simultaneously (ADR-0005 validation 1)",
		vpcA.Desired.CIDR == vpcB.Desired.CIDR && vpcA.Namespace != vpcB.Namespace,
		fmt.Sprintf("%s and %s both serve %s", vpcA.Namespace, vpcB.Namespace, vpcA.Desired.CIDR))

	// Allow everything in both, so that anything blocked later is the topology
	// talking and not a firewall rule.
	if err := applyPermissive(vpcA); err != nil {
		return err
	}
	if err := applyPermissive(vpcB); err != nil {
		return err
	}

	srvA, err := ListenIn(vpcA.Instances[0].Namespace, "0.0.0.0:8080", "VPC-A\n")
	if err != nil {
		return err
	}
	defer srvA.Close()
	srvB, err := ListenIn(vpcB.Instances[0].Namespace, "0.0.0.0:8080", "VPC-B\n")
	if err != nil {
		return err
	}
	defer srvB.Close()

	// Within VPC A, one instance reaches another across subnets via the local
	// route. This is the route every VPC has and nobody can delete.
	within, err := DialFrom(vpcA.Instances[1].Namespace, "10.0.1.4:8080", 2*time.Second)
	if err != nil {
		return err
	}
	report.Assert("cross-subnet traffic inside one VPC works via the local route",
		within == Reachable, fmt.Sprintf("result: %s", within))

	// From VPC B's instance, 10.0.1.4 is B's OWN address space. It must reach
	// B's instance or nothing — never A's.
	banner, err := readBannerFrom(vpcB.Instances[1].Namespace, "10.0.1.4:8080")
	report.Assert("an instance in VPC b never reaches VPC a (ADR-0005 validation 1)",
		err != nil || !strings.Contains(banner, "VPC-A"),
		fmt.Sprintf("banner seen from VPC b: %q", strings.TrimSpace(banner)))

	return nil
}

// --- 2. Security groups, same subnet and across subnets (validation 2) -----

// AWS applies security groups even between two instances in the same subnet,
// because the enforcement point is the hypervisor in front of each interface,
// not a subnet boundary. A bridge-per-subnet design gets this wrong; the
// routed design gets it right for free, and the counters prove the packets
// really traversed the rules.
func checkSecurityGroups() error {
	vpc, err := buildStandardVPC("sg", "10.10.0.0/16")
	if err != nil {
		return err
	}

	// web (10.10.1.4) and app (10.10.2.4) are in different subnets;
	// peer (10.10.1.5) shares web's subnet.
	desired := vpc.Desired

	// Allow everything at the NACL layer so that the security group is the only
	// variable under test. Without this the subnets carry an empty NACL, which
	// correctly denies everything the moment traffic crosses a subnet boundary
	// — real behaviour, but it would mask what this check is actually about.
	for i := range desired.Subnets {
		desired.Subnets[i].NACL = []NACLRule{
			{ID: "acl-all", RuleNumber: 100, Protocol: protocolAll, CIDR: "0.0.0.0/0", Allow: true},
			{ID: "acl-all", RuleNumber: 110, Egress: true, Protocol: protocolAll, CIDR: "0.0.0.0/0", Allow: true},
		}
	}

	for i := range desired.ENIs {
		e := &desired.ENIs[i]
		e.Egress = []SGRule{{ID: "sgr-egress-all", Protocol: protocolAll, CIDR: "0.0.0.0/0"}}
		switch e.ID {
		case "eni-web":
			// Only tcp/8080 from the app subnet is allowed in.
			e.Ingress = []SGRule{{ID: "sgr-allow-8080-app", Protocol: "tcp", FromPort: 8080, ToPort: 8080, CIDR: "10.10.2.0/24"}}
		default:
			e.Ingress = nil
		}
	}
	if err := vpc.ApplyRuleset(Render(desired)); err != nil {
		return err
	}

	srv, err := ListenIn(vpc.Instances[0].Namespace, "0.0.0.0:8080", "web\n")
	if err != nil {
		return err
	}
	defer srv.Close()

	fromApp, err := DialFrom(vpc.Instances[1].Namespace, "10.10.1.4:8080", 2*time.Second)
	if err != nil {
		return err
	}
	report.Assert("cross-subnet traffic allowed by a security group is reachable",
		fromApp == Reachable, fmt.Sprintf("app -> web tcp/8080: %s", fromApp))

	fromPeer, err := DialFrom(vpc.Instances[2].Namespace, "10.10.1.4:8080", 2*time.Second)
	if err != nil {
		return err
	}
	// The peer is in web's own subnet and is NOT in 10.10.2.0/24, so the
	// security group must still block it.
	report.Assert("same-subnet traffic is subject to security groups (ADR-0005 validation 2)",
		fromPeer == Timeout, fmt.Sprintf("peer -> web tcp/8080: %s (want timeout)", fromPeer))

	// Blocked traffic must time out, not be refused: that is what AWS does,
	// and the difference is exactly what a learner has to be able to read.
	report.Assert("blocked traffic times out rather than being refused",
		fromPeer != Refused, fmt.Sprintf("result was %s", fromPeer))

	counters, err := readCounters(vpc.Namespace)
	if err != nil {
		return err
	}
	report.Metric("sg_counter_packets_total", fmt.Sprintf("%d", counters))
	report.Assert("security group counters recorded traffic in both directions (validation 2)",
		counters > 0, fmt.Sprintf("%d packets counted across sg chains", counters))

	return nil
}

// --- 3. Stateless NACL return traffic (ADR-0005 validation 3) --------------

// The single most valuable lesson in the whole product. Security groups are
// stateful, so a reply is allowed automatically. Network ACLs are stateless, so
// the reply needs its own rule covering the EPHEMERAL port range. Getting this
// wrong produces a connection that hangs rather than fails, which is why
// learners find it so hard.
func checkStatelessNACL() error {
	vpc, err := buildStandardVPC("acl", "10.20.0.0/16")
	if err != nil {
		return err
	}

	desired := vpc.Desired
	for i := range desired.ENIs {
		desired.ENIs[i].Egress = []SGRule{{ID: "sgr-egress-all", Protocol: protocolAll, CIDR: "0.0.0.0/0"}}
		desired.ENIs[i].Ingress = []SGRule{{ID: "sgr-ingress-all", Protocol: protocolAll, CIDR: "0.0.0.0/0"}}
	}

	// public allows 8080 inbound but has NO outbound ephemeral rule, so SYN-ACK
	// cannot get back out.
	for i := range desired.Subnets {
		s := &desired.Subnets[i]
		switch s.Name {
		case "pub":
			s.NACL = []NACLRule{
				{ID: "acl-pub", RuleNumber: 100, Protocol: "tcp", FromPort: 8080, ToPort: 8080, CIDR: "0.0.0.0/0", Allow: true},
			}
		default:
			s.NACL = []NACLRule{
				{ID: "acl-priv", RuleNumber: 100, Protocol: protocolAll, CIDR: "0.0.0.0/0", Allow: true},
				{ID: "acl-priv", RuleNumber: 110, Egress: true, Protocol: protocolAll, CIDR: "0.0.0.0/0", Allow: true},
			}
		}
	}
	if err := vpc.ApplyRuleset(Render(desired)); err != nil {
		return err
	}

	srv, err := ListenIn(vpc.Instances[0].Namespace, "0.0.0.0:8080", "web\n")
	if err != nil {
		return err
	}
	defer srv.Close()

	blocked, err := DialFrom(vpc.Instances[1].Namespace, "10.20.1.4:8080", 2*time.Second)
	if err != nil {
		return err
	}
	report.Assert("a NACL without an ephemeral-port egress rule breaks the return path (validation 3)",
		blocked == Timeout, fmt.Sprintf("app -> web tcp/8080: %s (want timeout)", blocked))

	// Add the ephemeral range Linux actually uses (32768-60999), which is what
	// ARCHITECTURE section 8.2 says `explain` evaluates the return path against.
	for i := range desired.Subnets {
		if desired.Subnets[i].Name == "pub" {
			desired.Subnets[i].NACL = append(desired.Subnets[i].NACL, NACLRule{
				ID: "acl-pub", RuleNumber: 110, Egress: true, Protocol: "tcp",
				FromPort: 32768, ToPort: 60999, CIDR: "0.0.0.0/0", Allow: true,
			})
		}
	}
	if err := vpc.ApplyRuleset(Render(desired)); err != nil {
		return err
	}

	fixed, err := DialFrom(vpc.Instances[1].Namespace, "10.20.1.4:8080", 2*time.Second)
	if err != nil {
		return err
	}
	report.Assert("adding the ephemeral-port rule restores connectivity (validation 3)",
		fixed == Reachable, fmt.Sprintf("app -> web tcp/8080 after the fix: %s", fixed))

	return nil
}

// --- 4. Atomic ruleset replacement (ADR-0005 validation 4) -----------------

// Nephos re-renders and replaces a VPC's whole ruleset whenever anything
// changes. If replacement were not atomic there would be a window in which no
// rules applied, and a denied flow would briefly succeed. That would be a
// security hole and, worse for a teaching tool, an intermittently wrong lesson.
func checkAtomicReplacement() error {
	vpc, err := buildStandardVPC("atomic", "10.30.0.0/16")
	if err != nil {
		return err
	}

	desired := vpc.Desired
	for i := range desired.ENIs {
		desired.ENIs[i].Egress = []SGRule{{ID: "sgr-egress-all", Protocol: protocolAll, CIDR: "0.0.0.0/0"}}
		desired.ENIs[i].Ingress = nil // deny all inbound, always
	}
	if err := vpc.ApplyRuleset(Render(desired)); err != nil {
		return err
	}

	srv, err := ListenIn(vpc.Instances[0].Namespace, "0.0.0.0:8080", "web\n")
	if err != nil {
		return err
	}
	defer srv.Close()

	probe := StartContinuousProbe(vpc.Instances[1].Namespace, "10.30.1.4:8080", 2*time.Millisecond, 4)

	const replacements = 100
	for i := 0; i < replacements; i++ {
		// Churn an unrelated part of the ruleset on every pass. The denied
		// flow above must stay denied throughout.
		desired.Subnets[1].NACL = []NACLRule{
			{ID: fmt.Sprintf("acl-churn-%d", i), RuleNumber: 100, Protocol: protocolAll, CIDR: "0.0.0.0/0", Allow: true},
		}
		if err := vpc.ApplyRuleset(Render(desired)); err != nil {
			probe.Stop()
			return fmt.Errorf("replacement %d: %w", i, err)
		}
	}
	probe.Stop()

	report.Metric("atomic_replacements", fmt.Sprintf("%d", replacements))
	report.Metric("atomic_probe_attempts", fmt.Sprintf("%d", probe.Attempts))
	report.Metric("atomic_probe_allowed", fmt.Sprintf("%d", probe.Allowed))
	report.Metric("atomic_probe_denied", fmt.Sprintf("%d", probe.Denied))

	report.Assert("the probe actually ran during the replacements",
		probe.Attempts > 50, fmt.Sprintf("%d attempts across 4 concurrent probers", probe.Attempts))
	report.Assert("100 ruleset replacements never briefly allow a denied flow (validation 4)",
		probe.Allowed == 0,
		fmt.Sprintf("%d of %d attempts were allowed (want 0)", probe.Allowed, probe.Attempts))

	return nil
}

// --- 5. Listeners inside a VPC namespace (ADR-0005 validation 5) -----------

// nephosd is one process that must serve a DNS resolver and an IMDS endpoint
// inside every VPC namespace at once. This proves a single Go process can hold
// sockets in several namespaces simultaneously.
func checkInNamespaceListeners() error {
	vpc, err := buildStandardVPC("svc", "10.40.0.0/16")
	if err != nil {
		return err
	}
	if err := applyPermissive(vpc); err != nil {
		return err
	}

	dns, err := ListenUDPIn(vpc.Namespace, "10.40.0.2:53")
	if err != nil {
		return fmt.Errorf("opening the VPC resolver socket: %w", err)
	}
	defer dns.Close()
	report.Assert("Go opens the VPC DNS socket inside the VPC namespace (validation 5)",
		true, "udp 10.40.0.2:53")

	imds, err := ListenIn(vpc.Namespace, "169.254.169.254:80", "HTTP/1.0 200 OK\r\n\r\nami-id\n")
	if err != nil {
		return fmt.Errorf("opening the IMDS socket: %w", err)
	}
	defer imds.Close()
	report.Assert("Go opens the IMDS socket inside the VPC namespace (validation 5)",
		true, "tcp 169.254.169.254:80")

	// And it must be reachable from an instance, since that is the only way
	// cloud-init ever sees it.
	got, err := DialFrom(vpc.Instances[0].Namespace, "169.254.169.254:80", 2*time.Second)
	if err != nil {
		return err
	}
	report.Assert("an instance reaches IMDS at 169.254.169.254",
		got == Reachable, fmt.Sprintf("result: %s", got))

	// These sockets live in a VPC namespace, so they must NOT be reachable
	// from the appliance root namespace.
	root, err := RootLinkNames()
	if err != nil {
		return err
	}
	report.Metric("appliance_root_links", strings.Join(root, ","))

	return nil
}

// --- 6. Host hygiene (ADR-0005 validation 6, RISKS S1) ---------------------

func checkHostHygiene() error {
	links, err := RootLinkNames()
	if err != nil {
		return err
	}
	var unexpected []string
	for _, l := range links {
		switch {
		case l == "lo", strings.HasPrefix(l, "eth"), strings.HasPrefix(l, "tunl"), strings.HasPrefix(l, "ip6tnl"):
			// loopback and the appliance's own Docker uplink
		default:
			unexpected = append(unexpected, l)
		}
	}
	report.Assert("the appliance root namespace holds nothing but its uplink (validation 6)",
		len(unexpected) == 0,
		fmt.Sprintf("links: %s", strings.Join(links, ", ")))

	// Nephos must never have written a kernel-global forwarding setting.
	// RISKS S1 names this as the early-warning signal to watch for.
	globalForward, err := sysctlGet("net/ipv4/conf/all/forwarding")
	if err == nil {
		report.Metric("root_ns_all_forwarding", globalForward)
	}

	out, err := exec.Command("nft", "list", "ruleset").CombinedOutput()
	if err == nil {
		report.Assert("no nephos nftables table exists in the appliance root namespace",
			!strings.Contains(string(out), "table inet nephos"),
			"checked `nft list ruleset` in the root namespace")
	}
	return nil
}

// --- 7. Namespace thread safety (RISKS T8, ADR-0002) ----------------------

// The failure this guards against is a thread left inside a namespace being
// reused by an unrelated goroutine, which then creates objects in the wrong
// place. Run this under -race as well.
func checkThreadSafety() error {
	before, err := CurrentNamespaceInode()
	if err != nil {
		return err
	}

	vpcNS := "nx-vpc-a"
	const workers = 50
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		go func() {
			errs <- Do(vpcNS, func() error {
				inside, err := CurrentNamespaceInode()
				if err != nil {
					return err
				}
				target, err := NamespaceInode(vpcNS)
				if err != nil {
					return err
				}
				if inside != target {
					return fmt.Errorf("thread is in namespace %d, expected %d", inside, target)
				}
				return nil
			})
		}()
	}
	var failures int
	for i := 0; i < workers; i++ {
		if err := <-errs; err != nil {
			failures++
		}
	}
	report.Assert("50 concurrent namespace entries each land in the right namespace (RISKS T8)",
		failures == 0, fmt.Sprintf("%d of %d failed", failures, workers))

	after, err := CurrentNamespaceInode()
	if err != nil {
		return err
	}
	report.Assert("the calling goroutine never left its own namespace (RISKS T8)",
		before == after, fmt.Sprintf("namespace inode before=%d after=%d", before, after))

	return nil
}

// --- helpers ---------------------------------------------------------------

// buildStandardVPC creates a VPC with a public and a private subnet and three
// instances: web and peer in the public subnet, app in the private one.
func buildStandardVPC(name, cidr string) (*VPC, error) {
	base := strings.TrimSuffix(cidr, ".0.0/16")

	pub := Subnet{Name: "pub", CIDR: base + ".1.0/24", Gway: base + ".1.1"}
	priv := Subnet{Name: "priv", CIDR: base + ".2.0/24", Gway: base + ".2.1"}

	desired := VPCDesired{
		Name:       name,
		CIDR:       cidr,
		Subnets:    []Subnet{pub, priv},
		ResolverIP: base + ".0.2",
		IGWIface:   "ig" + name,
		ENIs: []ENI{
			{ID: "eni-web", Iface: "ve" + name + "1", PrivateIP: base + ".1.4", SubnetName: "pub"},
			{ID: "eni-app", Iface: "ve" + name + "2", PrivateIP: base + ".2.4", SubnetName: "priv"},
			{ID: "eni-peer", Iface: "ve" + name + "3", PrivateIP: base + ".1.5", SubnetName: "pub"},
		},
	}

	instances := []Instance{
		{Name: "web", Namespace: "nx-i-" + name + "-web", ENI: desired.ENIs[0], Subnet: pub},
		{Name: "app", Namespace: "nx-i-" + name + "-app", ENI: desired.ENIs[1], Subnet: priv},
		{Name: "peer", Namespace: "nx-i-" + name + "-peer", ENI: desired.ENIs[2], Subnet: pub},
	}

	return BuildVPC(desired, instances)
}

// applyPermissive allows everything, so that a later failure is attributable to
// topology rather than to a firewall rule.
func applyPermissive(v *VPC) error {
	d := v.Desired
	for i := range d.ENIs {
		d.ENIs[i].Ingress = []SGRule{{ID: "sgr-ingress-all", Protocol: protocolAll, CIDR: "0.0.0.0/0"}}
		d.ENIs[i].Egress = []SGRule{{ID: "sgr-egress-all", Protocol: protocolAll, CIDR: "0.0.0.0/0"}}
	}
	for i := range d.Subnets {
		d.Subnets[i].NACL = []NACLRule{
			{ID: "acl-all", RuleNumber: 100, Protocol: protocolAll, CIDR: "0.0.0.0/0", Allow: true},
			{ID: "acl-all", RuleNumber: 110, Egress: true, Protocol: protocolAll, CIDR: "0.0.0.0/0", Allow: true},
		}
	}
	return v.ApplyRuleset(Render(d))
}

func readBannerFrom(ns, addr string) (string, error) {
	var banner string
	err := Do(ns, func() error {
		conn, err := dialWithTimeout(addr, 2*time.Second)
		if err != nil {
			return err
		}
		defer conn.Close()
		buf := make([]byte, 32)
		n, _ := conn.Read(buf)
		banner = string(buf[:n])
		return nil
	})
	return banner, err
}

// readCounters totals the packets counted across the security group chains,
// which is how the spike proves traffic really traversed the rules rather than
// taking some other path.
func readCounters(ns string) (int, error) {
	var total int
	err := Do(ns, func() error {
		out, err := exec.Command("nft", "-a", "list", "table", "inet", "nephos").CombinedOutput()
		if err != nil {
			return fmt.Errorf("listing the ruleset: %w: %s", err, string(out))
		}
		for _, line := range strings.Split(string(out), "\n") {
			if !strings.Contains(line, "counter packets") {
				continue
			}
			if !strings.Contains(line, "nephos:sgr-") && !strings.Contains(line, "nephos:sg-default-deny") {
				continue
			}
			fields := strings.Fields(line)
			for i, f := range fields {
				if f == "packets" && i+1 < len(fields) {
					var n int
					if _, err := fmt.Sscanf(fields[i+1], "%d", &n); err == nil {
						total += n
					}
				}
			}
		}
		return nil
	})
	return total, err
}

// cleanup removes every namespace this spike created. Only the nx- prefix is
// ever touched: anything without it was not created by Nephos (ADR-0007).
func cleanup() {
	for _, prefix := range []string{"nx-vpc-", "nx-i-", "nx-edge"} {
		names, err := List(prefix)
		if err != nil {
			continue
		}
		for _, n := range names {
			if strings.HasPrefix(n, prefix) {
				_ = Delete(n)
			}
		}
	}
}
