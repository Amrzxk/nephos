# M1 connectivity and isolation matrix

This matrix separates slice-2 VPC/subnet topology from slice-3 packet evidence.
The source behavior is
[ADR-0005](../adr/0005-nephos-owned-routed-network-plane.md); the M1 scope is
[ROADMAP](../ROADMAP.md#m1-thinnest-end-to-end-slice-two-instances-ping).
The executable rows are in `tests/vpc-subnet-smoke.sh`,
`tests/instance-ping-smoke.sh`, and privileged runtime/hook/network integration
tests. Slice 2 passed native Docker CI and WSL2. Slice-3 local privileged
WSL2/Docker Desktop results are from 2026-10-01. Native Ubuntu 24.04 also passes
all slice-3 cases, including the mandatory host nftables snapshot, in
[CI run 36973066755](https://github.com/Amrzxk/nephos/actions/runs/36973066755)
on 2026-10-02. Namespace and gateway
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
| Instance/ENI teardown | Termination frees resources only after runtime/network removal | Slice 3: terminate all instances through CLI, confirm no rows or managed Podman containers, then delete subnets/VPCs and confirm no namespaces. Test-owned Docker objects are removed and host namespace/links/routes/rules snapshots match. The additional mandatory host nftables snapshot passes on native Ubuntu; it was unavailable in the local WSL2 run. |
| Retained-root instance restart | SQLite and retained roots restore instance identity and data across appliance restart | Slice 4 planned, not run: preserve instance/subnet/VPC IDs, ENI ID, private IP, MAC, Podman container identity and a root-file marker through `down`/`up`; restore verified ENI plumbing and repeat bidirectional ping. A missing old root reports `failed`, never a fresh empty root. |
| Runtime identity repair | Discovery cannot authorize deletion of a pre-existing retained root | Slice 4 planned, not run: stale stored runtime ID discovers the correct fully owned container with the expected instance binding, repairs the row with compare-and-swap, and retains its file/root. A stale repair must not delete a discovered container; separately cover authoritative termination and cleanup of containers created by the current attempt. |
| Runtime-directory restart boundary | Volatile Podman state must not be mistaken for retained storage | Slice 4 planned, not run: fresh private runroot and libpod tmpdir at their configured paths before the first Podman call on whole-appliance start; graphroot retained. Daemon-only restart leaves live runtime directories intact. |
| Running-instance resync | Running state requires verified live networking | Slice 4 planned, not run: verify existing ENI/MAC/address/routes/namespace binding; repair safe missing plumbing or fail visibly on conflicting identity before reporting a matching observed generation. Re-run ping, isolation and spoof witnesses. |
| Whole-appliance console shutdown | Console cleanup and nested stops finish while Podman is available | Slice 4 planned, not run: active PTY/non-PTY sessions, blocked output and hook work are canceled and joined; nested instances stop gracefully and are joined before Podman exits. Recovered roots still contain their markers. |
| Durable soft reset | Reset converges despite disconnected callers and restarts | Slice 4 planned, not run: transactional create fence; staged instances → subnets → VPCs deletion; resume at every persisted boundary; caller disconnect does not abort; completed keyed retry cannot delete fresh resources; leak failure keeps the fence until explicit retry. Cache and histories remain; workspace is empty. |
| Owned orphan collection | Nephos markers discover candidates but deletion requires full ownership | Slice 4 planned, not run: collect rowless fully owned Podman/ENI/kernel orphans; reject foreign, incomplete, ambiguous or conflicting identities and root-netns aliases; record actionable failure instead of broad prefix deletion. |
| In-appliance leak closure | Unnamed namespaces and surviving references count as leaks | Slice 4 planned, not run: before Docker teardown, join controllers/GC/hooks/consoles and inspect Podman objects, processes/cgroups, namespace handles/duplicates, sockets, links/routes/rules/rulesets and hook artifacts. Named namespace absence alone cannot pass. `/proc` corroborates process/FD ownership; it does not globally enumerate socket-only namespace references. |
| Hard reset | Purge the verified old appliance independently of API/database health, then start fresh | Slice 4 planned, not run: pin container ID, prevalidate container and volume, recheck volume creation identity, refuse foreign consumers, and never start after partial purge. Prove both old objects absent before fresh readiness, empty state, new token/atomic credentials and retained actual limits unless explicitly overridden. |
| Full M1 demo and host/platform boundaries | Recovery/reset and real packets must pass on supported platforms | Slice 4 planned, not run: freshly built full demo/e2e on native Ubuntu 24.04 and Windows 10 WSL2/Docker Desktop. Compare host snapshots around inner operations from a stable post-`up` baseline and separately before/after the whole lifecycle; native nftables required, WSL unavailability explicit. Publish evidence before teardown. |

All slice-4 rows are **planned and not run**. Their deterministic fixtures,
negative controls, leak-observation boundary and evidence requirements are in
the [slice-4 verification plan](M1-slice4-verification-plan.md). They do not
supersede the historical slice-3 checkpoint or mark M1 acceptance complete.

Security groups and NACLs are M4, not M1; their packet matrix will be added
with their implementation. DNS/IMDS is M2, and internet edge routing is M3.
