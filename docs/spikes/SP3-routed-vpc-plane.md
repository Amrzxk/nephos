# SP3: The routed VPC network plane

- **Status:** **PASS** (21 of 21 assertions)
- **Date:** 2026-09-18
- **Validates:** [ADR-0005](../adr/0005-nephos-owned-routed-network-plane.md) (a Nephos-owned, routed network plane), [ADR-0002](../adr/0002-go-for-control-plane-and-cli.md) (Go, and the namespace rules)
- **Mitigates:** [RISKS](../RISKS.md) T3, T8
- **Reproduce:** `./spikes/run.sh sp3`

## Question

This is the spike that mattered most. ADR-0005 chose a routed design — one
router namespace per VPC, one veth pair per interface, proxy ARP, policy
routing, one atomic nftables ruleset — over the far simpler bridge-per-subnet
and container-runtime-network options. The custom network code is Nephos's
largest engineering risk.

Does that design actually behave like AWS on a WSL2 kernel, and can Go drive it
without stranding threads in the wrong network namespace?

If not, ADR-0005 needs superseding before M1 writes a line of `internal/network`.

## Environment

Same appliance as [SP1](SP1-appliance-nested-containers.md): Debian 13, kernel
6.6.87.2-microsoft-standard-WSL2, nftables, iproute2. The spike is a Go program
built `CGO_ENABLED=0` and run inside the appliance.

"Instances" here are plain network namespaces holding the far end of each veth,
rather than full containers. SP1 and SP2 cover real containers; SP3 only needs
something that owns an address and can open sockets, and this keeps 12 instances
cheap.

## Results

### The six ADR-0005 validation criteria

| # | Criterion | Verdict | Evidence |
|---|---|---|---|
| 1 | Two VPCs using 10.0.0.0/16 at once, no crosstalk | **PASS** | `nx-vpc-a` and `nx-vpc-b` both serve `10.0.0.0/16`; a dial to `10.0.1.4` from VPC b returns b's banner, never a's |
| 2 | Same-subnet and cross-subnet traffic both hit SG counters | **PASS** | 6 packets counted across `sg_*` chains; see below |
| 3 | A NACL without an ephemeral-port rule breaks return traffic | **PASS** | times out before the rule, reachable after |
| 4 | 100 ruleset replacements never briefly allow a denied flow | **PASS** | 0 of 84 probe attempts allowed |
| 5 | Go opens DNS and metadata sockets inside a VPC namespace | **PASS** | `udp 10.40.0.2:53` and `tcp 169.254.169.254:80`, reachable from an instance |
| 6 | The appliance root namespace shows nothing but the uplink | **PASS** | links are exactly `lo, eth0` |

### Same-subnet security groups — the property that justified the design

Two instances in the **same subnet**, `peer` (10.10.1.5) and `web` (10.10.1.4):

```
peer -> web tcp/8080: timeout   (web's SG allows tcp/8080 only from 10.10.2.0/24)
app  -> web tcp/8080: reachable (app is 10.10.2.4, and is allowed)
```

This is the single strongest argument for the routed design. In AWS, security
groups apply between instances in the same subnet, because the enforcement point
sits in front of each interface rather than at a subnet boundary. A
bridge-per-subnet design would have switched `peer -> web` in the kernel without
consulting any rule, and would have needed a second enforcement point in the
bridge family to fix it. Here it is free, and the counters prove the packets
really traversed the chains.

Blocked traffic **times out** rather than being refused, which is what AWS does
and what a learner has to be able to tell apart from "nothing is listening".

### The stateless NACL trap reproduces exactly

The lesson [LABS](../LABS.md) calls `stateless-trap`:

```
NACL allows tcp/8080 inbound, nothing outbound  -> app -> web times out
add egress tcp 32768-60999                      -> app -> web reachable
```

The connection hangs rather than failing, because the SYN-ACK is silently
dropped on the way back. Security groups are stateful and would have allowed the
reply automatically; network ACLs are not. Reproducing this faithfully — hang,
not refusal — is the whole point of enforcing on real packets.

### Atomic replacement holds under churn

100 consecutive full-ruleset replacements with `nft -f`, while four concurrent
probers hammered a flow that must stay denied:

| | |
|---|---|
| Replacements | 100 |
| Probe attempts | 84 |
| **Allowed** | **0** |
| Denied | 84 |

Zero leaks. This matters beyond security: a window in which no rules applied
would make Nephos intermittently teach the wrong thing.

> The first run of this check managed only 8 attempts, because a denied flow
> costs the full dial timeout and one prober with a 300 ms timeout barely
> samples the window. Eight attempts would have been weak evidence for a
> "never" claim. Four probers at a 100 ms timeout give 84, which is worth
> asserting on. The check now fails if fewer than 50 attempts land.

### Namespace thread safety (RISKS T8)

The subtlest hazard in the whole project: the Go runtime moves goroutines between
OS threads, `setns` changes the namespace of a *thread*, and a switched thread
returned to the scheduler will later create sockets or nftables rules in the
wrong namespace — intermittently, and almost undebuggably.

| Check | Verdict |
|---|---|
| 50 concurrent namespace entries each land in the right namespace | **PASS** (0 of 50 failed) |
| The calling goroutine never left its own namespace | **PASS** (inode 4026532511 before and after) |
| `go test -race` on the renderer and namespace helpers | **PASS** |

The rule that makes this work, and which `internal/network/netns` must keep:
**a thread that switched namespace is never returned to the scheduler.** Every
entry runs on its own `runtime.LockOSThread()` goroutine with no matching
`UnlockOSThread`, so the runtime destroys the thread when the goroutine returns.

Listeners are the interesting case: the socket is created on the
soon-to-be-discarded thread, and only the accept loop runs on ordinary
goroutines, because a bound socket carries its namespace with it. That is what
lets one `nephosd` process serve DNS and IMDS inside every VPC namespace at once.

### Host hygiene (ADR-0003, RISKS S1)

The appliance's root namespace holds exactly `lo` and `eth0` — its Docker
uplink, nothing else. No `table inet nephos` exists there. Every VPC's
namespaces, veths, addresses, routes and rules live inside their own namespace,
and only per-namespace sysctls were written.

## The renderer is pure, and that is what makes this testable

The nftables renderer is a pure function: desired state in, ruleset text out. No
I/O, no clock, no randomness, and every collection sorted before it is emitted.

That bought a test suite needing no kernel, no root and no namespaces, covering:

- **evaluation order** — anti-spoof → NACL → conntrack → SG egress → SG ingress,
  asserted as an *ordering*, not merely as presence;
- **the subnet-boundary guard** — `iifname @sn_x oifname != @sn_x`, which is what
  makes NACLs apply only across boundaries;
- **default deny** — every SG chain ends in `drop`, never `reject`;
- **rule-number ordering** for NACLs;
- **traceability** — every counted rule carries a `nephos:<id>` comment, without
  which `explain` could not name the rule that blocked a flow;
- determinism across 20 renders.

This is the discipline `internal/network/firewall` inherits, and it is how
[RISKS T3](../RISKS.md#t3-network-behavior-diverges-from-aws) gets pinned down
cheaply.

## Findings to carry into M1

1. **An empty NACL denies everything across a subnet boundary.** Correct — a
   custom network ACL denies until you add rules — but it surprised the spike's
   own author mid-run, when a security group test failed for NACL reasons. Two
   consequences: `explain` must be able to say "the NACL, not the security
   group", and the default NACL must be created allow-all, exactly as AWS does.

2. **The local-route rule must come first, and the catch-all must blackhole.**
   Rule priorities 100 (local) / 200 (per-ENI) / 32000 (blackhole) reproduce
   AWS's behaviour that every subnet in a VPC can reach every other and that the
   local route cannot be overridden. Unmatched traffic goes nowhere *silently* —
   never to a default route.

3. **Interface names must be allocated, not derived.** `ve` plus a
   database-allocated short index stays inside the kernel's 15-character limit;
   deriving names from resource IDs would not.

4. **`send_redirects=0` is not optional.** Without it the router can tell an
   instance to bypass it for same-subnet traffic, which would bypass
   enforcement entirely.

## Verdict

**ADR-0005 is validated.** The routed design reproduces same-subnet security
groups, stateless NACL returns, overlapping CIDRs, and atomic rule replacement
on a WSL2 kernel, and Go drives it race-free without leaking threads between
namespaces.

No superseding ADR is needed. `internal/network` can be written against this
shape in M1.

## Outstanding

- [ ] **Native Linux leg** — as for SP1, the `Spikes` workflow covers it but
      needs the branch pushed.
- [ ] Internet gateway 1:1 NAT is **rendered and unit-tested but not yet
      exercised on live packets**: SP3 builds no `nx-edge` namespace. M3 is where
      that becomes real, and the connectivity matrix should cover it.
- [ ] SG references (`@sgref_*` sets) are rendered but not populated; M4 wires
      the set members.
