# M1 connectivity and isolation matrix

This matrix separates slice-2 VPC/subnet topology from slice-3 packet evidence.
The source behavior is
[ADR-0005](../adr/0005-nephos-owned-routed-network-plane.md); the M1 scope is
[ROADMAP](../ROADMAP.md#m1-thinnest-end-to-end-slice-two-instances-ping).
The executable rows are in `tests/vpc-subnet-smoke.sh`,
`tests/instance-ping-smoke.sh`, and privileged runtime/hook/network integration
tests. Slice 2 passed native Docker CI and WSL2. Slice-3 local privileged
WSL2/Docker Desktop results are from 2026-10-01; native Ubuntu CI is wired
after the earlier smokes but awaits branch push. Namespace and gateway
existence alone is not proof that packets traverse them. The
[local execution checkpoint](M1-slice3-local-verification.md) also records
the independent review, console-cancellation regression, and remaining gates.

| Case | AWS-style behavior / Nephos constraint | Slice and assertion |
|---|---|---|
| Two VPCs both `10.0.0.0/16` | VPC address spaces are independent; overlapping ranges are legal | Slice 2: both become `available`, have different `nx-vpc-*` namespace names/short indexes, and keep separate gateways. |
| Subnet `10.0.1.0/24` in VPC A and `10.0.2.0/24` in VPC B | Each subnet has a base+1 gateway in its own VPC router | Slice 2: `nxr0` has the expected `/32` only in its owning namespace; deletion removes it. |
| Appliance `down` then `up` | SQLite desired state reconstructs kernel state | Slice 2: IDs, namespaces/short indexes, and `/32` gateways survive a restart. |
| Host boundary | VPC topology stays inside the appliance, not the host namespace | Slice 2: host namespace inode, links, routes, policy rules, and (when readable) nftables are snapshotted before startup and compared after test-owned object cleanup. Native CI requires nftables inspection. |
| Cross-subnet ICMP in one VPC | The local route permits private connectivity between subnets | Slice 3: A/one `10.0.1.4` → A/two `10.0.2.4` receives all 3 replies and destination echo-request counters advance; reverse ping succeeds. Hook observer proves configured `eth0` before PID 1; integration tests inspect veth/MAC and `/32` routes. |
| Same-address VPC packet isolation | No crosstalk between VPCs with overlapping CIDRs | Slice 3: A and B both use `10.0.0.0/16`; A/one and B/overlap-source independently receive `.1.4`. B/source → B/remote-only `.1.5` succeeds and advances its counter. A/one → `.1.5` fails and B's request counter stays unchanged. A has no `.1.5` instance, so a failed return path cannot hide delivery. |
| Instance source spoof | Only the assigned private IP may source packets | Slice 3: after the correct-source positive control, A/one adds forged `.1.5` and sends 2 requests to A/two. Ping fails, the VPC anti-spoof counter advances by at least 2, and two's request count is unchanged. Removing the forged address restores successful ping. |
| Failed hook | Missing plumbing must fail closed | Slice 3: an instance-scoped test fault is recorded before the production hook executes; API reports `failed`, an unobserved generation and a hook-specific reason; Podman confirms PID 1 is not running. |
| Exec streams and safety ceiling | Console is labeled out-of-band; resource limits protect the appliance | Slice 3: non-TTY console preserves NUL bytes on separate stdout/stderr, stdin EOF/trailing output and exit 7. Bounded task creation reaches `pids.max=512`, is refused while existing tasks survive, releases its workers, and subsequent exec remains responsive. |
| Instance/ENI teardown | Termination frees resources only after runtime/network removal | Slice 3: terminate all instances through CLI, confirm no rows or managed Podman containers, then delete subnets/VPCs and confirm no namespaces. Test-owned Docker objects are removed and host namespace/links/routes/rules snapshots match. Native CI requires the additional host nftables snapshot; it was unavailable in this local WSL2 run. |
| Retained-root instance restart | SQLite and retained roots restore instances/addresses/files across appliance restart | Slice 4: pending; the slice-2 VPC/subnet restart does not prove this. |
| Soft/hard reset and leak closure | Reset leaves an empty default workspace and no owned kernel/runtime leaks | Slice 4: pending, including the replayable full e2e demo and platform QA. |

Security groups and NACLs are M4, not M1; their packet matrix will be added
with their implementation. DNS/IMDS is M2, and internet edge routing is M3.
