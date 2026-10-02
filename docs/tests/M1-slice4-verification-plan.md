# M1 slice-4 verification plan

Date: 2026-10-02. **Planned; no slice-4 checks have run.** This document is a
test design, not a verification checkpoint or M1 sign-off. Slice 4 is not
implemented; M1 roadmap acceptance boxes remain unchecked.

Canonical inputs are the [slice-4 design](../superpowers/specs/2026-10-02-m1-slice-4-recovery-reset-design.md),
[implementation plan](../superpowers/plans/2026-10-02-m1-slice-4-recovery-reset.md),
[roadmap](../ROADMAP.md#m1-thinnest-end-to-end-slice-two-instances-ping),
[ADR-0004](../adr/0004-instances-as-system-containers.md),
[ADR-0007](../adr/0007-sqlite-state-and-reconciliation.md), and the
[connectivity matrix](M1-connectivity-matrix.md). The
[slice-3 checkpoint](M1-slice3-local-verification.md) remains the historical
record of its packet, console and fixed-limit checks. It does not prove
retained-root restart, crash orphan collection, reset or comprehensive leak
closure.

## Delivery and evidence map

Every row below is **planned / not run**. Execution records must add the
candidate commit, exact commands, platform, result and artifact links before
claiming a pass. A skipped check must state its exact reason.

| Delivery | Primary assertions | Evidence layers |
|---|---|---|
| 4A: retained-root recovery | Runtime identity/provenance, file and address persistence, ENI verification, volatile directory startup | Store/adapter/controller unit tests; privileged Podman/hook/network integration; real-appliance restart fixture |
| 4B: lifecycle, ownership and leak foundations | Graceful console/nested shutdown, strict deletion ownership, rowless orphan discovery, quiescence and reference closure | Pure ownership tests; deterministic controller/GC/session tests; privileged kernel/process/FD fixtures |
| 4C: durable soft reset | Transactional create fence, staged deletion, disconnected caller and restart resume, keyed replay, retained histories, failed leak retry | Store/service/API/CLI tests; privileged reset/leak integration; live-appliance soft-reset e2e |
| 4D: hard reset | Docker identity guards, health/database bypass, partial failure, fresh readiness/token/limits | Fake Docker/credential tests; real test-owned Docker lifecycle fixture |
| 4E: M1 demo and QA | Fresh builds, full packet/restart/reset demo, native/WSL platforms and host hygiene | Native Ubuntu 24.04 e2e CI; Windows 10 WSL2/Docker Desktop manual replay; saved artifacts |

Use table-driven unit tests, real privileged `integration` tests and the
`e2e` build tag where appropriate. Failure injection belongs in test-only
helpers copied into a test-owned appliance, never shipped binaries or a
public product API. Deterministic restarts at named boundaries are M1 work;
randomized `SIGKILL` chaos/soak and repeated 100-cycle reset testing remain M8.

## 4A/4B: recovery and graceful shutdown

| Fixture | Required result |
|---|---|
| Down/up with two instances in different subnets | Write distinct file markers using authenticated console; capture VPC/subnet/instance IDs, ENI IDs, private IPs, MACs, Podman IDs and retained-root identity before shutdown. After fresh readiness, all identities and exact file bytes match, both instances run and bidirectional three-packet ping succeeds. |
| Stale SQLite runtime ID with a correct pre-existing root | Inspect/discover using the expected instance binding and complete ownership metadata; compare-and-swap repairs only the expected row/generation/runtime ID. The discovered container/root/marker survives. No Delete is authorized merely because discovery returned it. |
| Generation advances during discovery or repair | Force terminate/reset/reconcile races at deterministic barriers. Reject stale publication; retain the discovered old container for the authoritative deletion path. Track whether a container was newly created in this attempt separately from discovery of an existing container. |
| Generation advances during new Create | Only a container proven created by that attempt may use that attempt's immediate cleanup path; failure retains an owned cleanup identity for retry. The analogous discovery race must never reuse this cleanup authority. |
| Wrong binding, foreign/incomplete labels, name collision, two owned candidates | Fail visibly and record the conflicting identities. Exercise inspect/start/stop/delete/exec, especially a cached ID belonging to another fully valid managed instance, and assert no wrong-instance mutation. Do not adopt, relabel, remove or recreate through the ambiguity. A friendly name or `nx-` prefix alone is insufficient. |
| Provisioned instance loses its retained runtime/root | Report `failed` with an actionable missing-root reason; do not call fresh Create or silently erase the instance's data contract. Persist provisioned atomically with first observed running state/event; protect legacy running/observed rows through migration, and discover any legacy cached runtime identity before creation. An instance that never acquired a root remains a distinct creation case. |
| Container already running during daemon resync | Validate pinned namespace ownership and current ENI identity, MAC, address, links, routes/rules and source checks. Repair only safe missing plumbing; conflicting namespace or identity fails visibly. Do not mark observed generation current based only on Podman running status. |
| Whole-appliance startup with stale volatile files | Before any Podman invocation, validate the built configuration and initialize fresh private runroot at `/var/lib/nephos/runroot` and rootful libpod tmpdir at `/run/libpod`, without changing the paths recorded in Podman's database. Retain graphroot `/var/lib/nephos/containers`; assert retained container/root/AMI identity. Verify mount privacy and startup failure if directory initialization fails. |
| Daemon-only restart while Podman and instances run | Preserve live runroot/tmpdir and runtime service; do not apply whole-appliance volatile-directory replacement. Recover via verified inspection/resync and retain console-capable instances. |
| Down with active PTY and non-PTY execs | Cover idle input, blocked stdout/stderr consumers, pending resize and trailing output. Close/cancel sessions and join workers/duplicated handles before nested Stop; restore local terminal state where applicable. A shutdown transport close must not become a fabricated successful remote exit. |
| Down with nested instances and concurrent hook work | Close admission, cancel/join controllers, GC, hooks and consoles as designed; stop and join nested instances while Podman is still available, then stop runtime supervision. Assert no leftover owned exec/conmon descendants or populated instance cgroups, and preserve retained writable roots. Cover bounded stop failure and explicit error reporting. |
| Start admitted before exclusive drain, hook arrives afterwards | Resolve the hook through an ephemeral pending-start lease plus existing peer/runtime/namespace validation. Admit that verified child while rejecting independent/forged effects; exclusivity waits for both parent and all children, even if parent ownership closes first. No hook timeout/deadlock is disguised as a completed drain. |
| Unexpected controller/server exit or required runtime/store inspection failure | Use the same bounded cleanup path as signal shutdown. Do not report ready on systemic failure; an individual failed instance remains visible without making otherwise completed initial recovery unready. Do not introduce automatic daemon respawn. |

Re-run the slice-3 packet controls after recovery: bidirectional same-VPC
ping, overlapping-VPC remote-only destination isolation, source spoof/drop
and destination counters, and hook-before-PID-1/fail-closed behavior. File,
identity and packet assertions are separate; a namespace or gateway existing
cannot substitute for a delivered-packet witness.
For the 4B shutdown fixtures, test the proposed shared daemon budget of 25
seconds, with nested stop at most 15 seconds within it and Docker outer stop
35 seconds. These are engineering constants to verify, not measured runtime
guarantees or a separate timeout multiplied serially for each instance.

## 4B: ownership, orphan collection and leak observation

Discovery deliberately covers broad Nephos markers, including names/prefixes
and labels. Deletion requires the complete expected ownership tuple and
current pinned identities. A marker is evidence to investigate, not deletion
authority. Ordinary user/foreign objects must survive every fixture.

| Fixture | Required result |
|---|---|
| Rowless fully owned Podman container and ENI objects | Discover beyond database rows; validate full ownership and instance/ENI binding; remove in dependency order, including owned peers/routes/rules/source-check objects and hook artifacts. Repeat collection idempotently. |
| Interrupted create/terminate | Inject boundaries before/after runtime creation, SQLite publication, hook plumbing, runtime removal, ENI removal and final lease release. Resume/collect without adopting a wrong root, reusing an IP too early or losing cleanup identity. |
| Partial labels, prefix-only names, different workspace/appliance scope | Discover candidates, refuse destructive cleanup, return actionable ambiguous/foreign diagnostics and fail leak closure when an unresolved Nephos candidate remains. Do not remove unrelated objects to make the report pass. |
| Wrong ENI namespace, swapped binding, reused link identity | Reinspect pinned namespace/link identity immediately before mutation; fail closed. Assert foreign links/routes/rules and live resource bindings remain intact. |
| Named namespace aliases appliance root or another namespace | Compare inode/ownership, reject unsafe traversal or mutation; never program/delete objects through an alias to appliance root. Assert root links/routes/rules/nftables remain unchanged. |
| Malformed namespace/link/ruleset ownership markers | Test canonical namespace index range 1–99999999, leading-zero rejection, symlink/non-NSFS targets, full ENI alias/peer binding and current gateway/ruleset markers. Generic `ve*`, `vp*`, `eth0` or `libpod-*` names alone cannot authorize deletion. |
| Namespace name removed while Nephos holds a duplicate handle | Leak check fails while the owned original/duplicate reference is open. Close/join both, then prove a successful recheck; `ip netns list` absence alone is insufficient. |
| Owned netlink/socket reference retains a namespace | Inventory socket creation and release within the namespace; quiescence joins owners, closes all duplicate references and owned sockets, then verifies zero ownership inventory. Use bounded test fixtures for socket-only retention. |
| Owned process or cgroup retains instance namespaces | Inspect Podman/conmon/exec descendants and task/cgroup population, correlate namespace inode and process/FD witnesses, fail until joined/removed. Exclude expected appliance baseline processes explicitly. |
| Collection/console/hook cleanup overlaps leak check | Deterministic barriers prove the check waits for controller, GC, hook and console work and their handle/socket owners to finish. No resource mutation may race the final success snapshot. |

The final leak assertion runs **inside the still-live appliance before any
Docker container stop/removal or volume removal**. Removing the outer
container can destroy references and hide a failed product cleanup. Inspect
managed Podman objects, instance/exec/conmon descendants, populated owned
cgroups, named namespaces, ENI/veth objects, routes/policy rules, Nephos
rulesets, hook artifacts and product-owned namespace references.

The ownership inventory must include namespace handles and every duplicate,
netlink handles, ordinary sockets opened in a namespace and their duplicates,
and work/process owners that can keep those references alive. The final
barrier closes admission and joins owners before asserting inventory zero.
Expected long-lived appliance root/runtime baseline objects are not instance
leaks and must be enumerated, rather than hidden by an arbitrary count.

`/proc/<pid>/ns/net`, process FD links and namespace inode comparison provide
corroborating evidence for process/namespace-FD retention. They cannot
globally enumerate all socket-only namespace references: a socket may retain
a namespace without a visible named mount or namespace FD. The claim is
zero **Nephos-owned** leaks, supported by complete ownership tracking,
quiescence and independent process/FD/kernel checks. Do not weaken that claim
to zero names, or claim a global kernel namespace census. External diagnostic
tools must release any namespace/socket/FD references they hold before the
final assertion; unknown or untracked product ownership is a failure.
Assert the reported result requires `complete:true`, `clean:true`, no
findings/inspection errors, empty resource/lease rows and zero outstanding
product effects. A timeout or unreadable required inventory cannot pass.

## 4C: durable soft reset

Exercise the admitted operation independently from its HTTP/CLI waiter.
SQLite is the source of truth; no durable reset state exists only in memory.

| Fixture | Required result |
|---|---|
| Reset admission races resource create | A transaction admits one operation, fences genuinely new resource creation, and commits initial instance deletion intents/events atomically. A create committed first belongs to reset; a genuinely new create after the fence is rejected before ID/IP/index allocation or event writes. Same-key create replay returns its original snapshot without creating a resource. Include VPC, subnet and instance transactions. |
| Multiple reset callers | Same key returns/joins the same durable operation. Concurrent new keys join the active operation with durable alias binding before waiting; no competing workers. Same-key parameter mismatch conflicts. Authentication and default-workspace scoping remain enforced. |
| Dependency staging | Mark/delete instances first; only after runtime/ENI/lease closure atomically advance phase plus subnet intents/events; only after subnet closure do the same for VPCs; run orphan collection and final quiescent leak verification. Reentry cannot increment deletion generations repeatedly. Assert no stage prematurely reports completion. |
| Caller disconnect, CLI timeout or process interrupt | The waiter fails or disconnects with accurate status, but admitted work continues. A later observer can obtain the durable operation result without resubmitting deletion blindly. |
| Restart at each persisted boundary | Inject daemon restart before/after admission/fence, each stage transition, per-resource deletion completion, orphan cleanup, leak result and operation completion/fence release. Automatic startup resume converges with exactly one authoritative operation. |
| Crash between final deletion and durable completion | Preserve the fence and resume final collection/check; report success only after durable completion and verified zero leaks. |
| Completed operation replay with fresh resources | Complete reset, release the fence, create new resources, then repeat the old key. Return the original completed result; do not fence, enqueue or delete the fresh resources. |
| Failed leak check | Persist failure and diagnostics, retain the create fence, refuse success, and continue to report the failed operation across daemon restart. Require explicit retry for coordinator phase advancement/new-layer intents/success/fence release. Already committed deletions and strict periodic orphan GC may progress; even after they repair the fault, startup/resync cannot silently mark the reset succeeded or release its fence. Explicit user teardown stays dependency-checked. |
| Explicit failed-operation retry | Use a new invocation key bound to the named failed operation; concurrent callers join one retry path, perform fresh ownership/collection/leak checks and release the fence only on durable success. Foreign or ambiguous remnants still fail; stale/unknown IDs cannot target a newer operation. Resume of an already succeeded operation replays success and leaves fresh resources untouched. |
| History and cache preservation | Keep AMI cache, durable events and monotonic replay cursor, request/idempotency replay history and allocated kernel short-index history. All resource/ENI/IP leases are gone; newly created resources use fresh IDs/indexes as specified. |
| Empty reset and repeat new reset | Empty default workspace completes with zero leaks and no default VPC creation. A new reset after completion is a separate operation; old-key replay remains a historical result. |

Add store/service/API/CLI tests for transaction rollback, stage error
reporting, result serialization and bounded synchronous wait. Keep direct
resource deletion's dependency behavior; reset's ordered worker does not
justify bypassing ordinary dependency checks or deleting SQLite blindly.

The planned API contract is `POST /v1/workspaces/{workspace}/reset` with a
required `Idempotency-Key` and strict body `{}` or
`{"resume_operation_id":"reset-..."}`. POST returns 200 only after a complete,
clean leak result; there is no 202-success shortcut. Read-only GET at the same
path returns latest status or exact status by `operation_id` query. Test
bearer auth, localhost Host allowlist, rejected browser Origin, unknown
fields/workspaces, and typed error envelopes: `ResetInProgress` (409),
`IdempotentParameterMismatch` (409), `ResetFailed` (409), `ResetTimeout` (504)
and `InvalidResetID.NotFound` (404). Errors carry the reset ID when known.

Pin operation states `running|failed|succeeded` and phases
`instances|subnets|vpcs|verify|done`; an inspection/drain error cannot become
a clean result. GET/CLI `reset --status` cannot mutate. `reset --retry` binds
its observed failed ID even if status changes before submission. The
two-minute default `--timeout` waiter uses a reset-specific HTTP deadline
rather than the ordinary ten-second resource client timeout, and reuses its
key across transport retries. Keep active/failed aliases and completed
aliases/results for at least 24 hours after completion. Reject console
admission while fenced; read/list/events and idempotent teardown remain
usable. Test that success commits fence release atomically after effects
are joined and cannot be followed by a stale worker recreating an object.

## 4D: hard reset and purge

Hard reset uses Docker and local configuration, so it remains available when
the API is unhealthy, authentication is unavailable or SQLite is corrupt.
Test identity guards using fakes first, then only test-owned real objects.

| Fixture | Required result |
|---|---|
| Healthy and unhealthy/corrupt-database appliance | Purge does not depend on API/reset worker or opening SQLite. Pin the old container ID; capture actual CPU, memory and PID limits before teardown. |
| Competing lifecycle commands or failed replacement preflight | Acquire the exclusive CLI lifecycle lock and recheck after acquisition; preflight Docker, local image and replacement limits before destructive work when possible. Competing `up`/purge/reset cannot race within the same configuration directory. Document that different configuration directories/external Docker actors are not governed by that lock. |
| Foreign container or volume under expected name | Prevalidate both objects before stopping/removing either. Refuse with no destructive partial action when ownership, mount association or volume identity is wrong/ambiguous. |
| Foreign consumer of the old volume | Inspect all consumers, including stopped containers, before stopping/removing the old appliance; an unrelated consumer or incomplete consumer inspection refuses purge with zero destructive calls. Recheck before volume deletion; do not force-remove the consumer or its data. Owned appliance association does not confer authority over another container. |
| Container name reused during purge | Reinspect/remove the pinned old ID; a new container under the old name is not the target. Foreign replacement blocks fresh startup rather than being removed. |
| Volume name recreated between inspection and removal | Reinspect creation identity/ownership/consumer set immediately before volume deletion; refuse a changed target. Record limitations of Docker's volume name operation and do not describe a recheck as an atomic identity delete. |
| Failure after only one old object is removed | Return failure with exact surviving state; do not invoke `up` or overwrite credentials as if fresh startup succeeded. Retry revalidates remaining objects and any name reuse. |
| Successful purge followed by failed fresh start | First prove both old objects absent; report the startup error without claiming ready/empty state. Retain accurate local credential state and permit safe recovery through the specified CLI behavior. |
| Fresh startup succeeds | New container and volume creation identities, empty default workspace and ready API; token differs from old token, old token rejected; credentials atomically replaced with mode 0600 and never exposed in artifacts. |
| Non-default old appliance limits and explicit overrides | Fresh `up` inherits inspected actual CPU/memory/PID limits unless caller overrides individual values; CLI defaults cannot overwrite preserved values implicitly. |
| Test cleanup after a failing fixture | Reinspect exact test-owned identities before cleanup. Never delete a pre-existing or replaced foreign container/volume. Capture failure artifacts first. |

For credentials, test write/rename/chmod errors and interruption around atomic
replacement; preserve a usable prior file when replacement has not completed.
Include no-container/no-volume and partially absent cases in the hard-reset
contract tests. CLI hard reset must distinguish purge success, fresh startup
failure and ready success.

## 4E: full demo, platforms and artifacts

The future `docs/demos/M1.md` and `make e2e` must replay the roadmap's locally
built two-subnet/two-instance ping, file write, down/up, file/identity/ping
recheck and soft reset. Extend the automated harness with hard reset,
overlapping-VPC isolation, spoof/drop witnesses, hook failure, graceful
console shutdown and deterministic recovery/reset faults. Do not silently
replace the roadmap demo with namespace-only checks.

Required closure runs are native Ubuntu 24.04 with rootful Docker in CI and
Windows 10 with WSL2 and Docker Desktop using Linux/WSL project commands. Both
must build the development AMI, appliance, CLI and test-only helpers from the
candidate source; cached old tags/binaries are not fresh-build evidence.
The fixture refuses pre-existing `nephos`/`nephos-data`, uses a temporary
credential directory and gates cleanup on exact owned identities.
Native Windows/macOS console runtime QA remains distinct; successful
cross-builds or Linux console execution do not establish that evidence.

Host hygiene has two separate comparison windows:

1. Capture a stable host baseline after `up` has completed and Docker has
   installed its expected plumbing. Compare namespace inode, links, routes,
   policy rules and readable nftables around inner recovery/reset operations;
   do not confuse normal Docker lifecycle changes with product host mutation.
2. Capture before appliance creation and after verified test-owned outer
   teardown to check the whole appliance lifecycle. Keep the in-appliance
   zero-leak result from before this teardown as separate evidence.

Native CI must install/use nftables and fail if the required host snapshot
cannot be read. WSL2 must explicitly record unavailable host inspection and
its reason, never label it passing. Platform/kernel/Docker differences and
unavailable checks remain in the execution record.

Before teardown, save redacted artifacts for success and failure: source
commit and dirty status; build commands and image IDs/digests; exact Go,
Docker, Podman, crun, kernel and relevant distro/package versions; API
resource/operation states and event cursors; runtime identity/label/limit
inspection; marker hashes; packet/counter witnesses; ownership and leak
reports; process/cgroup/FD corroboration; and both host comparison windows
as they become available. Upload CI artifacts even if a gate fails. Never
publish credentials, bearer headers or instance secrets.

Implementation verification also requires `make ci`, `make generate-check`,
appropriate privileged integration checks, vulnerability scanning and
`git diff --check`. Record commands that could not run and their exact
reason. `make e2e` is currently a placeholder; this planning change runs no
Docker or future reset command and records no new runtime pass.

Only after implementation, reviewed evidence and both required platform
runs may the roadmap/CHANGELOG and fidelity documentation claim M1 closure.
