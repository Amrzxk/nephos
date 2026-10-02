# M1 Slice 4: Recovery, Reset, and Zero-Leak Closure

**Status:** Recommendations accepted on 2026-10-02: option A and the
recommended delivery order. This document and its companion plan are the
requested written elaboration, approved for native execution with independent
4A–4E delivery-boundary reviews on 2026-10-02. Slice 4 is not
implemented; no new runtime or platform test result is claimed here.

**Intent:** finish M1's reliability boundary without adding M2 features.
Contributors must retain instance files and identity across appliance
restart, recover interrupted cleanup, and obtain an honest zero-Nephos-leak
result before M1 closes. Preserve the open-source, locally built workflow.

**Base:** merged slice 3, PR #6, main commit `84462f8`. The older dirty
`codex/m1-slice3-plan` checkout is not an implementation base. Preserve its
uncommitted planning work. Read the [M1 integration design](2026-09-23-m1-two-instances-ping-design.md),
[roadmap](../../ROADMAP.md#m1-thinnest-end-to-end-slice-two-instances-ping),
and [slice-3 checkpoint](../../tests/M1-slice3-local-verification.md).

## 1. Scope and decisions

Use SQLite as the only durable authority, level-triggered reconciliation,
rootful Podman through its private REST service, and the Nephos-owned routed
network inside the privileged appliance. ADR-0003/0004/0005/0007/0008 remain
accepted. A reset-operation row is desired control-plane state in that same
database, not an external queue or second store. In-memory lifetime tracking
accounts for live handles and workers; it never becomes resource intent.

Implementation constants and helper/API names below are explicit written
elaborations of the accepted recommendations, covered by written-plan approval;
they are not retrospective claims that each spelling/value was discussed.

One implicit `default` workspace, explicit VPCs, fixed `t3.micro` (2 vCPU,
1 GiB, 512 tasks), appliance defaults (2 CPUs, 4 GiB, 4096 tasks), Linux
5.15+, cgroup v2, Go 1.27.1, and `CGO_ENABLED=0` remain unchanged. Keep
unprivileged ICMP, default capabilities plus `NET_ADMIN`, `userns=auto`,
default seccomp, and no devices. Network/PID host modes, host bind mounts,
host firewall/global sysctl changes, and runtime networks modeling subnets
remain prohibited.

No workspace management, default VPC, SG/NACL, internet edge, SSH, DNS,
IMDS, user data, selectable AMIs/types, capacity quotas, user-facing instance
stop/start, web console, or general workflow/operations framework. M8's
random-kill campaign and repeated-reset endurance test stay in M8; M1 uses
deterministic boundary injection. Native Windows/macOS console runtime QA
is not implied by cross-builds.

**Reset alternatives:** A, durable automatic continuation with a synchronous
HTTP waiter, is selected. B, manual continuation of every interruption,
would strand a creation fence after routine disconnect/restart. C, a 202
asynchronous public operation API, would change M1's synchronous reset
contract. A read-only reset-status endpoint supports diagnosis and explicit
retry; it does not turn the selected POST into C.

## 2. Retained-root recovery before reset work

Treat the Podman ID in SQLite as a cached observation, never sufficient
identity proof. Every inspect/start/stop/delete/exec call binds the expected
workspace and instance ID to the observed full container ID, canonical name,
managed label, workspace label, instance label, and OCI instance annotation.
A cached ID pointing to another valid Nephos instance is not acceptable.
Collision, contradictory metadata, or incomplete inspection fails closed.

Return creation provenance separately: created in this attempt versus
rediscovered. A failed generation/ID write must not delete a rediscovered
retained container merely because the write failed. Rebind a verified
discovered ID using a generation-and-old-ID compare-and-swap. Distinguish
that stale-cache case from a terminate race; termination may deliberately
remove the exact expected instance, and a newly created losing attempt still
needs owned cleanup. Do not retry create/delete in a destructive hot loop.

Persist a `provisioned` fact on an instance. Before setting it, creation may
retry from the AMI; set it atomically with the first observed running state
and event. Once set, an absent retained container/root is visible `failed`
with a preservation-specific reason, never a silent fresh AMI. Migration
backfill protects existing running/observed instances; any legacy cached
runtime ID also requires discovery before creation. Missing pre-running
scratch containers may be retried, but existing roots are always reused.

At whole-appliance startup, mount private fresh tmpfs at the existing
configured runroot `/var/lib/nephos/runroot` and rootful libpod temporary
directory `/run/libpod` before the first Podman command. Preserve graphroot
`/var/lib/nephos/containers`, the container database, images, user namespace
allocations, and writable layers. Validate the built Podman configuration
against those paths; do not casually change paths recorded in its database.
Never refresh these mounts on a daemon-only restart while nested containers
can still be alive. Continue the existing fail-closed whole-appliance policy;
do not introduce transparent daemon respawn.

The running fast path must inspect the expected instance, pin and validate
its live PID/user/network namespace identity, and observe/repair its ENI.
Retain instance ID, ENI ID, IP, MAC, and short index; the new namespace inode
may legitimately differ after appliance restart. An old-inode ENI may be
replaced only after strict ownership and obsolete-attachment proof. Foreign
or ambiguous links must cause failure, not adoption or deletion.

## 3. Shutdown, sessions, and lifetime accounting

Introduce daemon-owned admission and lifetime accounting for resource
effects, hooks, runtime exec, namespace handles/duplicates, namespace-bound
sockets/netlink handles, and workers. Register before exposure and unregister
only after close/reap completes. A namespace worker's result is not lifetime
completion if deferred cleanup still holds its namespace. Keep this registry
ephemeral; startup reconstructs observations from SQLite and the runtime.

The effect barrier permits verified child work of an already admitted effect
while blocking new independent effects. Podman Start synchronously invokes
the separately served OCI hook: rejecting that hook after admitting Start
would prevent the parent from finishing and deadlock/fail shutdown. Register
an ephemeral pending-start lease by exact expected runtime identity; after
existing private-peer/runtime/namespace checks, attach the hook as a child
even while drain is pending. No caller-supplied parent label grants admission.
The barrier waits for parent and children; closing a parent cannot erase a
still-running hook. Test Start → drain pending → hook deterministically.

Public HTTP requests and upgraded consoles derive from daemon lifetime.
Closing the public HTTP server alone does not drain upgraded WebSockets.
On reset or shutdown, close admission, cancel matching console sessions,
close/reap only their execs, and join their pumps before instance deletion or
store closure. A disconnect must never kill a shared instance. Unexpected
controller or server exit follows the same cleanup path as a signal.

On whole-appliance shutdown, stop new effects and drain sessions/controllers,
then stop nested instances while Podman is alive, then exit the daemon and
stop Podman. Preserve container records/roots even if graceful stop times
out. Use one bounded budget, not a timeout per instance multiplied serially.
Initial implementation budget: daemon drain/stop at most 25 seconds, nested
stop at most 15 seconds within it, Docker outer stop 35 seconds. These are
explicit proposed engineering constants for this written plan, not measured
guarantees; validate them with bounded failure tests. Do not promise arbitrary
unflushed application writes survive power loss.

Startup restores an unfinished reset before any ordinary instance start.
Run orphan observation and the initial reconcile sweep with its persistent
fence respected. Health becomes ready after initial recovery/sweep, not only
when every resource is healthy. A resource-specific failure remains visible;
systemic store/runtime/inventory failure prevents readiness. A persisted
terminal reset failure may expose ready read/status APIs with creation still
fenced so a user can diagnose and explicitly retry it.

## 4. Ownership inventory and leak proof

Add startup and periodic runtime orphan collection, not just iteration over
existing instance rows. Compare inventory by expected workspace/instance
identity; a pending row with no cached ID is not an orphan. Include rowless
managed containers and ENIs inside otherwise live VPCs. Preserve the cached
AMI. Order child cleanup before parent namespace removal.

Observation is broader than destructive authority: scan reserved names and
Nephos markers so malformed objects cannot disappear from a clean result.
Delete only fully proven, correctly scoped owned objects with no matching
desired owner. For namespaces require canonical `nx-vpc-<index>` with index
1–99999999, no leading zero, non-symlink NSFS identity, and no alias to the
appliance root or another desired namespace. Runtime proof uses all identity
fields in §2. ENI proof uses its full alias/ENI identity, IP/MAC/index and
namespace/peer relationship; gateway `nxr0:gw` and `inet nephos` table comment
`nephos:m1` are additional current-plane markers. Generic `ve*`, `vp*`,
`eth0`, or `libpod-*` names alone are not deletion authority. Unmarked
foreign objects remain untouched; ambiguous Nephos-marked objects make
cleanup fail with object identity and reason.

Zero named namespaces or zero database rows is not zero leaks. A namespace
can survive unlink because a process, FD, socket, or worker still references
it. Before unlink, remove/check owned routes, veths, gateway and ruleset in
the pinned namespace; account for all product-owned references and tasks.
After draining effects, correlate owned runtime/cgroups/processes and known
namespace identities with accessible appliance `/proc` task namespace and
namespace-FD observations. Required inspection failure or unresolved runtime
timeout is an error, never a clean empty inventory.

Linux has no universal namespace enumeration here. A `/proc` namespace/FD
scan cannot prove absence of a namespace held solely by an arbitrary foreign
socket. Product-created sockets/duplicates must therefore be registered and
joined. Tests independently demonstrate this blind spot and prove the
product-accounted equivalent blocks clean success. This defines ownership
and evidence honestly; it does not relax zero **Nephos** leaks or authorize
destroying arbitrary externally held namespaces.

The leak result includes `complete`, `clean`, object-level findings and
inspection errors. Only `complete:true`, `clean:true`, no findings/errors,
empty M1 resources/leases, and zero outstanding product effects can succeed.
Control-plane listeners, Podman service, the retained AMI/store, and explicitly
foreign unmarked objects are not deleted resource leaks.

## 5. Durable soft reset: option A

Persist one active reset per workspace, its operation ID, phase, state,
timestamps, last error, keyed invocation aliases and terminal result in
SQLite. Use an internal `reset-` plus 17 lowercase hex ID; it is a Nephos
control operation, not a new AWS resource type. State is `running`, `failed`,
or `succeeded`; phases are `instances`, `subnets`, `vpcs`, `verify`, `done`.
The initial operation/fence, instance deletion intents and events commit
atomically. Each subsequent phase advance and that layer's intents/events
also commit atomically, after the previous layer is physically gone. Never
mark all parent and child rows deleting at once; existing dependency checks
and instance teardown still need parent rows.

The creation fence is checked in the resource service's **creation-decision
transaction**, before allocating IDs/IPs/indexes or writing events. A create
committed before the fence is included; a genuinely new create after it is
rejected. Harmless same-key create replays return their original snapshot.
Do not guard every `WithTx`: reads, status writes and teardown must continue.
Reject new console admission while fenced. Read/list/events and idempotent
delete/terminate remain usable; they cannot circumvent dependency order.

Operation work is owned by daemon context after commit, not the HTTP request.
Cancel before commit aborts; disconnect/deadline after commit detaches only
the waiter. Startup automatically continues `running` operations. Phase
reentry is idempotent and does not increment deletion generations repeatedly.
Transient resource teardown failures use existing bounded backoff. Ambiguous
ownership, incomplete required inspection, or residual leaks at verification
persist `failed` and retain the fence. They do not become automatic destructive
phase continuations on every restart; explicit retry reopens the same operation
at its stored phase. This pauses coordinator phase advancement, new layer
intents and success/fence release, not already committed deletion intent.
Ordinary reconcilers may continue those deletions, and strict periodic orphan
GC may repair residue; neither changes a failed reset to success or releases
its fence. Explicit user delete/terminate stays idempotent and dependency-
checked. Test failed status/fence across startup/resync even when such cleanup
progresses. Hard reset remains a separate escape hatch.

After ordered teardown, obtain a drain/barrier over compute/network/GC/hook
effects and console cleanup, collect proven orphans, and run complete leak
verification while creation remains fenced. Resolve effects whose transport
timed out through authoritative observation; do not assume cancellation
prevented the external effect. A failed drain times out visibly. Record
success and clear the fence atomically only after this proof. A crash between
proof and commit reruns verification. Do not allow stale in-flight workers
to materialize objects after success; restart admission only after the barrier
and success commit have both completed.

Keep the implicit workspace, API token, AMI cache, event history, 24-hour
resource-create replay snapshots and monotonically allocated kernel-index
history. Soft reset empties resources/leases, not every SQLite table. SSE IDs
remain monotonic, and a create replay may correctly refer to a deleted resource.

## 6. Reset API, retries, and CLI

Edit OpenAPI first and generate interfaces/client during implementation.
Keep bearer auth, localhost Host allowlist, rejected browser Origin, strict
JSON and the existing error envelope. Add only these reset-specific operations:

| Method/path | Contract |
|---|---|
| `POST /v1/workspaces/{workspace}/reset` | Synchronous waiter; required `Idempotency-Key`; body `{}` starts/joins, or `{ "resume_operation_id": "reset-..." }` explicitly retries a failed operation. Unknown fields/workspaces fail. |
| `GET /v1/workspaces/{workspace}/reset` | Read latest reset status, or exact status with `operation_id` query; 404 if absent. No destructive work and no general operations endpoint. |

POST returns 200 only for fully verified success with
`ResetResult{operation, leak_check}`. GET returns
`ResetOperation{id,workspace,state,phase,state_reason,created_at,updated_at}`.
Error responses retain the existing envelope and identify the reset ID via
`resource_id`; GET exposes the durable failure/progress. New errors are
`ResetInProgress` (409, fenced new create/console or invalid continuation),
`IdempotentParameterMismatch` (409), `ResetFailed` (409, failed teardown
ownership or leak proof), `ResetTimeout` (504, waiter deadline), and
`InvalidResetID.NotFound` (404). Unauthorized calls cannot create an operation.
No 202-success shortcut or clean result on timeout/inspection failure.

Concurrent new keys join the same active operation and bind to its ID;
same-key request mismatch conflicts. Persist alias binding before waiting.
Retain completed reset request aliases/results for at least 24 hours after
completion; never expire an active/failed binding. A completed key replay
returns the recorded result without inspecting/deleting resources created
later. A failed key replay observes its current durable operation without
secretly retrying: while failed it returns that failure, and after explicit
continuation it may join the resumed run or replay its later success.
Explicit resume uses a new invocation key bound to the named failed
operation, not a new reset. Concurrent resume callers join that same operation;
resume of an already succeeded operation replays its result, never resets
new resources. Stale/unknown targets cannot target a newer operation.

`nephos reset [--timeout D] [-o json]` generates one key per invocation,
reuses it for transport retries, and waits (default two minutes, overrideable)
using a reset-specific HTTP deadline, not the resource client's ten seconds.
Cancellation/timeout reports the operation ID when known and says it continues
in the appliance; do not start an unkeyed replacement. `nephos reset --status
[-o json]` reads the latest operation. `nephos reset --retry` reads its failed
ID and submits explicit continuation with a new key; bind to that ID even if
status changes between calls. `--hard` cannot combine with `--status`/`--retry`.
These are concrete written elaborations of the accepted retry recommendation,
covered by the written-plan approval; preserve them during API coding.

## 7. Identity-safe hard reset

Hard reset uses the Docker Engine directly and never requires a working
daemon, token, health endpoint or SQLite. Preflight Docker, local image and
replacement limits before destructive work when possible. Acquire an
exclusive lifecycle lock in the same CLI configuration directory, recheck
after lock acquisition, and validate **both** old objects before deleting
either: exact ownership, captured full container ID, expected named-volume
mount, and owned volume creation identity. A foreign volume must prevent
removal of an otherwise owned container.
Inspect all volume consumers, including stopped containers; any consumer
other than the captured old appliance prevents destructive purge. Required
consumer inspection failure is not permission to delete first.

Stop/remove the captured container ID, not just its reusable name. Before
volume removal re-inspect ownership/creation identity and refuse replacement
or unrelated consumers; never force-remove foreign users. Docker volume
names have no atomic compare-and-remove primitive, so this coordination does
not promise immunity to arbitrary external Docker actors or different CLI
configuration directories. Verify the captured old container and old volume
are absent **before** creating same-name replacements.

Preserve observed appliance CPU/memory/pids limits unless flags explicitly
override a field; with no old container use defaults. Purge failure, volume
in-use, changed identity, or uncertain deletion stops before fresh `up` and
reports which stages completed. After a successful purge, fresh `up` must
reach ready, expose empty resource state and a new token; only then atomically
replace local credentials. Startup or credential-save failure is partial
failure, never reset success. Ordinary `down --purge` shares identity-safe
purge but does not restart. No broad cleanup or other Docker object removal.

## 8. Delivery order and evidence

| Increment | Independently reviewable outcome |
|---|---|
| 4A | Expected runtime identity/provenance/CAS repair and retained-root restart, including running ENI repair and fresh ephemeral runtime state. |
| 4B | Daemon/session/stop lifecycle, strict ownership inventory/GC, namespace reference accounting and fail-closed leak verifier. |
| 4C | SQLite reset phases/fence, automatic interruption recovery, API/CLI keyed synchronous reset, explicit failure retry and history preservation. |
| 4D | Identity-safe Docker purge and hard reset, limits and credential lifecycle. |
| 4E | Replayable demo, real e2e target, native Ubuntu CI, matching WSL2 manual QA and evidence-backed M1 closure. |

The [implementation plan](../plans/2026-10-02-m1-slice-4-recovery-reset.md)
specifies tasks/interfaces and red/green verification. The
[verification plan](../../tests/M1-slice4-verification-plan.md) maps fault
boundaries and residual objects to independent witnesses. Runtime-ID hazards
were established by static code review, not a freshly reproduced Docker
failure; add failing deterministic regressions before fixing them.

Require actual privileged-appliance restart/marker/IP/ENI/MAC/ping tests,
interruption at each reset transaction/effect boundary, create/reset races,
failed leak inspection, rowless orphans and foreign-object negatives,
long-lived PTY/non-PTY exec drain, and hard-purge stage failures. Assert leaks
inside the **still-running** appliance before Docker cleanup. Compare host
network state around inner operations against a post-`up` baseline, and also
compare before/after the whole appliance lifecycle. Ordinary Docker port
publishing is not a Nephos host mutation. Endpoint snapshots alone do not
prove absence of transient host mutations.

CI runs the full demo/e2e on native Ubuntu 24.04 from local builds, requires
readable host nftables, and collects logs/inventory/effect diagnostics before
teardown with artifacts even on failure. Record image IDs, source commit,
actual Podman/crun/conmon/nftables versions, limits, platform and commands;
current unversioned apt inputs do not establish full image reproducibility.
Run matching QA on Windows 10/WSL2/Docker Desktop; unavailable host `nft`
inspection is explicitly unavailable, never a pass. Preserve slice-3 packet
evidence and distinguish it from new restart/reset closure.

Only after required evidence exists update M1 acceptance checkboxes, the
demo/evidence report, roadmap, fidelity status, AGENTS, risks and changelog.
Do not retire broader T1/T4/T8/S5 risks or claim M8 chaos coverage from M1.
If a controlled supported-platform test establishes an accepted architectural
assumption cannot be met, stop and write the required superseding ADR. Do not
weaken the test or replace the retained root to manufacture success.

## 9. Review handoff

**Documentation review, 2026-10-02:** the main agent checked scope,
placeholders, internal consistency and requirement-to-task coverage. Two
independent read-only reviews identified and closed parent/child hook-drain
admission, failed-reset versus committed-deletion semantics, full runtime
method-matrix coverage and e2e build-prerequisite wording. These are written
contract reviews, not runtime verification. Subsequent human written-plan
approval selected native execution and independent delivery-boundary reviews.

The user approved the written plan and native execution on 2026-10-02,
authorized committing/pushing these docs to main, and separately authorized
signed-off local implementation commits and isolated privileged tests with
cleanup of only test-owned Docker objects. Implement in 4A–4E order, with
independent review at each boundary. Implementation pushes/merges remain
separate decisions; no slice-3 permission is silently inherited.
