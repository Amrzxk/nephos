# M1 slice 4A: local recovery checkpoint

Date: 2026-10-02. This is a **local implementation checkpoint**, not M1
acceptance closure or a native Ubuntu CI result. The approved
[slice-4 plan](../superpowers/plans/2026-10-02-m1-slice-4-recovery-reset.md)
assigns Tasks 1–2 to 4A; 4B–4E remain unimplemented.

## Source and scope

Approved docs commit `c65b725` was pushed to `main`. Implementation is on the
isolated `codex/m1-slice4-native` branch and has not been pushed or merged.
The primary checkout's eight preexisting documentation changes remain intact.

- `8a1acd2`: expected runtime identity/full ID validation and creation provenance.
- `81141f1`: provisioning migration/CAS, retained-root recovery, pinned running
  ENI convergence, and fresh volatile runtime directories.
- `25a455c`: independent-review fix for non-disruptive healthy ENI observation
  with policy validation and counter preservation.

Internal preservation state is not a new learner API field. Schema 5 backfills
running/previously observed instances; missing pre-running scratch containers
may retry, but missing provisioned roots fail visibly without replacement.
All console/network accesses retain existing security boundaries.

## Platform and evidence

Local WSL2 kernel `6.6.87.2-microsoft-standard-WSL2`, Docker Desktop Engine
28.3.2, Linux amd64, rootful cgroup v2; Docker memory 6,215,155,712 bytes.
Isolated appliances use 2 CPUs, 4 GiB and 4096 tasks. Nested `t3.micro`
remains 2 vCPU, 1 GiB and 512 tasks; no additional capabilities or host mounts.
Built appliance userspace: Podman 5.4.2, crun 1.21
(`10269840aa07fb7e6b7e1acff6198692d8ff5c88`), nftables 1.1.3.
These are measured versions, not a claim of reproducible apt inputs.

Final appliance image:
`sha256:061f8033a7508042cf395c6f6c1acc1ff0662cc83417b1c1cac7e970bc8e0e68`,
built from product commit `25a455c7fc92f16d4eded11c298c88659eb629a7`.
Subsequent progress-document changes do not alter the product inputs.

Execution logs are in the self-ignored plan workspace
`.superpowers/sdd/2026-10-02-m1-slice-4-recovery-reset/`; no API tokens are
included in this report. The smoke records all four full identity tuples.

| Check | Observed result |
|---|---|
| `go test ./... -count=1` | Pass, including identity rejection, retained cache repair, terminate race, missing-root refusal, provisioning/event rollback and migration backfill. |
| `make ci` | Pass: format, vet, lint, unit/race and supported cross-builds. |
| `make generate-check` | Pass; sqlc generated from the new migration/queries, no public API change. |
| `go mod tidy -diff` | Pass, no dependency changes. |
| `make dev-ami`, `make appliance`, `make build` | Pass, locally built development images/binaries. |
| Privileged topology integration | Pass: real ENI convergence, source checks/isolation, obsolete-marker repair and foreign/ambiguous attachment refusal; repeated healthy resync preserves carrier state, policy handles and counters, while policy drift repairs and foreign policy fails closed. |
| Privileged Podman integration | Pass: real expected-identity adapter, PTY/non-PTY streams/resize, task-ceiling refusal and cleanup. |
| Privileged hook integration | Pass: real peer/runtime/PID checks, pre-PID-1 ENI plumbing and fail-closed cases. |
| `tests/instance-ping-smoke.sh` | Pass: prior packet/console/task/hook controls; four retained markers, full runtime IDs, ENI IDs, IPs and MACs through same-container down/up; bidirectional ping; already-running missing ENI repaired by resync with unchanged root/identity; resource teardown. |
| Shell syntax and `git diff --check` | Pass. |

Bootstrap failure, appliance bootstrap, CLI lifecycle and VPC/subnet smokes
also passed at `81141f1`; the subsequent fix changes only ENI observation.
On final product head `25a455c`, all three privileged suites were rebuilt and
rerun, and the appliance/CLI were rebuilt before the full instance smoke.
Final logs: `topology-final-integration.log`, `podman-final-integration.log`,
`hook-final-integration.log`, `instance-smoke-4a-final.log`, and
`review-fix-ci.log`. None of the privileged suites skipped a test.

The tagged packages were compiled with `CGO_ENABLED=0 go test -tags integration
-c` and copied into the test-owned appliance; each ran with
`NEPHOS_IN_APPLIANCE=1` and `-test.v -test.count=1`. The bounded task helper
was also built and copied into that appliance. Ordinary `go test ./...` does
not substitute for these privileged checks.

The privileged integration fixture used `--network none`, a private cgroup
namespace and a dedicated volume, not host bind mounts. Cleanup validated its
exact Docker ID/label and volume timestamp/label/consumer absence. The full
smoke refuses preexisting `nephos`/`nephos-data`, uses a temporary CLI home,
and cleans only its captured test objects. Controlled probes were similarly
identity-checked and removed; their logs remain in the plan workspace.

## Evidence-backed refinements

### Fresh crun state is required

The first real restart failed: Podman inspected all retained IDs as stopped,
but crun Start rejected them with `container ... already exists`. A controlled
one-container rerun preserved `/run/crun/<full ID>/status` after stopping the
same outer Docker container. Restarting that outer ID reproduced the error.
With all nested tasks stopped, refreshing **only `/run/crun`** in the probe
let the exact same full inner ID start; its root-file marker remained intact.

Whole-appliance entrypoint now prepares fresh private mode-0700 tmpfs at
the unchanged runroot, `/run/libpod`, and crun's verified `/run/crun` rundir
before any Podman command. Graphroot, container database, images, userns
allocations and writable layers stay persistent. The contract test was red
for missing crun mount/privacy, then green; the full retained-root smoke
subsequently passed. This is an ephemeral-state fix, not a changed accepted
platform assumption or a reason to recreate learner roots. Daemon-only
restart must never run this initialization with live instances.

### Conservative ENI attachment proof

4A can rebind an obsolete inode marker only on a full-owned reciprocal pair
whose actual peer is already in the pinned verified instance namespace.
Another live attachment or a foreign peer marker is refused without mutation.
This is not general orphan deletion; 4B owns inventory/GC and lifetime closure.

## Independent review

A fresh read-only review of `c65b725..81141f1` found no Critical or Minor
issues and one Important issue: unconditional ENI convergence flapped healthy
links and recreated the source-check table on every resync. A real-kernel
regression reproduced carrier changes `2 → 8` over three reconciliations,
counter loss `7 → 2`, and table-handle replacement.

`25a455c` adds read-only verification of links/aliases/MAC/IP, router/guest
routes, router sysctls and the full nft table/set/chain/ordered rules. Healthy
resync does not mutate links or counters; detected drift uses fail-closed
repair. Unverifiable or foreign policy quiesces only the proven-owned pair
without adopting the table. Regression, drift-repair and foreign-refusal
integration tests pass, as do unit/race/lint/cross checks. Independent
re-review of `25a455c` accepted the fix with no new findings, conditional on
the rebuilt-appliance smoke finishing. That final smoke completed with exit
0, preserved all four marker/identity witnesses and ping, repaired live ENI
drift, and cleaned its own objects. 4A is ready for the 4B delivery boundary.

The review excluded later 4B lifetime/stop/GC/leak closure, 4C/4D reset, and
4E native/platform evidence. These remain required under the approved plan;
they are not waived or counted as 4A verification.

## Remaining gates

- New 4A code has **not** run on native Ubuntu 24.04 CI; implementation
  push/PR/merge requires separate authority. Historical slice-3 CI does not
  prove new recovery code.
- Host namespace, links, routes and policy-rule snapshots matched around the
  local whole-lifecycle smoke. Host nftables inspection was unavailable in
  WSL2 and is **not** reported as passing; native CI must require it.
- This is not manual native Windows/macOS console QA or the full M1 demo.
- 4B graceful nested stop, console/hook/effect lifetime drain, owned orphan
  collection and complete in-appliance leak checker remain next.
- Soft/hard reset, real `make e2e`, and full native/WSL demo are 4C–4E.
  Existing row/container/namespace teardown checks are not a complete leak
  proof. Every M1 acceptance checkbox remains unchecked.
