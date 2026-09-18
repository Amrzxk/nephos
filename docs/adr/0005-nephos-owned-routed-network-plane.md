# ADR-0005: A Nephos-owned, routed network plane

- **Status:** Accepted; **validated in M0** by [SP3](../spikes/SP3-routed-vpc-plane.md)
- **Date:** 2026-09-17

## Context

Networking is the heart of Nephos. The AWS VPC behaviors that must be reproduced on real packets:

- A VPC is a **routed layer-3 network**. There is no layer-2 broadcast domain, and an implicit router sits in front of every network interface.
- Each subnet uses **one route table**. The **local route** lets every subnet in a VPC reach every other.
- An **internet gateway** does 1:1 NAT between private IPs and public or Elastic IPs.
- A **NAT gateway** is an interface inside a subnet, and it depends on that subnet's route table.
- **Security groups** are stateful, attach to network interfaces, and apply even between instances in the same subnet.
- **Network ACLs** are stateless and apply only when traffic crosses a subnet boundary.
- Every subnet has **five reserved addresses**. The VPC DNS resolver lives at base+2, and instance metadata at 169.254.169.254.
- **Different VPCs may use overlapping CIDRs.**

Constraints: enforcement must happen **outside** the instance, because the learner has root inside it and can flush local rules. Nothing may touch the host's network namespace ([ADR-0003](0003-appliance-container-packaging.md)).

## Options considered

### Option A: Container-runtime networks (one Docker/Podman bridge network per subnet)
- Pros: very little code.
- Cons:
  - The runtime owns the NAT, iptables, and DNS rules.
  - No per-subnet route tables, no local route between subnets, no NAT gateway hop.
  - Filtering inside a bridge needs `br_netfilter`, which is kernel-global.
  - Overlapping CIDRs collide.
  - fakecloud's docs show the resulting deviation: subnets "cannot route to each other".

### Option B: A layer-2 bridge per subnet, attached to a router namespace per VPC
- Pros: a familiar Linux design; same-subnet traffic is switched in the kernel.
- Cons:
  - Same-subnet security groups need a second enforcement point (bridge-family filtering).
  - Broadcast and ARP behave unlike AWS.
  - More moving parts to keep consistent.

### Option C: A routed design. One router namespace per VPC, one veth pair per interface, proxy ARP (the Calico-style model)
- Pros:
  - Every packet, including same-subnet traffic, crosses a single enforcement point, as it does through the AWS hypervisor.
  - Routes, SGs, NACLs, anti-spoofing, and NAT all live in one namespace with one atomic nftables ruleset.
  - A namespace per VPC makes overlapping CIDRs trivial. Connection tracking is per namespace, which makes stateful SGs simple.
  - No bridges and no `br_netfilter`.
- Cons: more custom code; needs expertise in proxy ARP and policy routing; debugging needs dedicated tooling.

### Option D: Open vSwitch / OVN
- Pros: production-grade SDN with logical routers, ACLs, and NAT.
- Cons: heavy daemons; kernel module not reliably present in Docker Desktop or WSL2 kernels; steep for contributors; overkill for one host.

### Option E: An eBPF datapath
- Pros: fast and flexible.
- Cons: fragile across the kernels Nephos must support; harder for learners and contributors to inspect. May be revisited later for flow logs only.

## Decision

Nephos owns a **routed network plane** (Option C) inside the appliance.

- **Namespaces:**
  - `nx-edge`: the simulated internet ([ADR-0006](0006-learner-access-through-simulated-internet.md)).
  - `nx-vpc-<short>`: one per VPC; the VPC router.
  - `nx-nat-<short>`: one per NAT gateway.
  - Instance namespaces are created with the instance ([ADR-0004](0004-instances-as-system-containers.md)).
- **Network interface (ENI):**
  - A veth pair. The router end is named from a database-allocated index (`ve<index>`, at most 15 characters), with proxy ARP on and a /32 route.
  - The instance end is `eth0`, carrying the subnet prefix and default gateway `.1`.
  - The source/dest check is an anti-spoof rule, enabled by default.
- **Router addresses:** each subnet's `.1`, the VPC resolver (base+2), 169.254.169.253, and 169.254.169.254 live on dummy interfaces in the VPC namespace.
- **Routing:**
  - Rule order (policy rules): first `to <VPC CIDR> lookup main` (the local route); then one `iif <ENI veth> lookup <route table>` per interface; last, a catch-all `blackhole` (silent drop).
  - Each Nephos route table is a Linux routing table. Subnets without an explicit association use the VPC's main route table.
  - Routes whose target was deleted become `blackhole` routes and show that status in the API.
- **Internet gateway:**
  - A veth pair `ig<index>` between the VPC namespace and `nx-edge`.
  - 1:1 NAT runs on that link **inside the VPC namespace**: SNAT from private to public on the way out, DNAT from public to private on the way in. Private addresses therefore never reach `nx-edge`.
  - Packets sent to the gateway from an interface with no public IP are dropped, as in AWS.
- **NAT gateway:** its own namespace with an interface in its subnet (no security groups) and a required Elastic IP; it masquerades forwarded traffic. Its subnet's route table decides whether it actually reaches the internet.
- **Firewall:** `table inet nephos` in each VPC namespace. The forward chain evaluates in this order:
  1. anti-spoof;
  2. NACL egress rules of the source subnet and ingress rules of the destination subnet, only when the subnets differ. Stateless, in rule-number order, default deny;
  3. `ct state established,related accept`;
  4. source interface's SG egress rules, then destination interface's SG ingress rules. An allow is a `return`, and each chain ends in `drop`;
  5. accept.

  Other rules:
  - SG references use named IP sets.
  - Every rendered rule carries a counter and a comment with its Nephos rule ID (used by `explain` and flow logs).
  - The input chain admits only DNS and metadata traffic to router addresses. SGs don't apply to those, as in AWS.
- **Rendering:** a pure function turns a VPC's desired state into ruleset text, covered by golden-file tests. The ruleset is applied atomically with `nft -f` inside the VPC namespace, and any relevant change re-renders it in full.
- **Host hygiene:** only per-namespace sysctls change (`ip_forward`, `proxy_arp`, `send_redirects`, `rp_filter`). Nothing runs in the host namespace.
- **Honesty rule:** if any namespace, route, or nftables operation fails, the affected resource moves to `failed` with an error and the reconciler retries. Nephos never reports a rule as active when it isn't.

## Consequences

- Positive: faithful AWS semantics in one inspectable place per VPC; overlapping CIDRs work; the static analyzer (`explain`) and the renderer share one semantics catalog.
- Negative / costs:
  - The custom network code is Nephos's largest engineering risk ([RISKS](../RISKS.md) T3). It requires the connectivity test matrix and differential testing against `explain`.
  - A `nephos debug network <vpc>` command must show namespaces, links, routes, rules, and the live ruleset.
- Documented deviations:
  - MTU is 1500 or lower, not AWS's 9001.
  - All flows are connection-tracked (AWS's "untracked connection" nuance isn't modeled).
  - No IPv6 in the MVP.

## Validation

**M0 spike SP3** must show:

1. Two VPCs using 10.0.0.0/16 at the same time, with no crosstalk.
2. Same-subnet and cross-subnet traffic both hit SG counters.
3. A NACL without an ephemeral-port rule breaks return traffic, and adding the rule fixes it.
4. 100 consecutive ruleset replacements, run during a continuous TCP probe, never briefly allow traffic that should be denied.
5. Go code opens DNS and metadata sockets inside a VPC namespace.
6. The appliance's root namespace shows nothing but the edge uplink.

**Result (2026-09-18):** all six pass on Windows 10 with WSL2 and Docker Desktop
([SP3](../spikes/SP3-routed-vpc-plane.md), 21 of 21 assertions). Two VPCs held
10.0.0.0/16 simultaneously with no crosstalk; same-subnet traffic was blocked by
a security group while cross-subnet traffic it allowed got through; a network
ACL missing an ephemeral-port rule made the connection hang, and adding the rule
fixed it; 100 ruleset replacements during 84 concurrent probe attempts allowed
**zero** packets that should have been denied; Go opened DNS and IMDS sockets
inside a VPC namespace; and the appliance root namespace held nothing but `lo`
and its uplink. The namespace stress test is race-clean, which is
[RISKS T8](../RISKS.md#t8-go-and-network-namespace-pitfalls).

Proxy ARP and policy routing behaved correctly on the WSL2 kernel, so no
superseding ADR is needed and `internal/network` can be written against this
shape.

Two details the spike showed are load-bearing rather than incidental:
`send_redirects=0` on each router-side interface (without it the router can tell
an instance to bypass it, bypassing enforcement), and the gateway needing an
explicit on-link `/32` route inside the instance before it can be a default
gateway.

**Revisit** if proxy-ARP routing proves unreliable on Docker Desktop or WSL2 kernels.
