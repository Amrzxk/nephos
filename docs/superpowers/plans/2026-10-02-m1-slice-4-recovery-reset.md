# M1 Slice 4: Recovery, Reset, and Closure Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** retain M1 instance roots across appliance restart, make both reset
levels identity-safe, and prove zero Nephos leaks before closing M1.

**Architecture:** expected runtime identities and creation provenance protect
retained roots; ephemeral lifetime accounting and ownership inventory make
cleanup observable. A SQLite reset coordinator automatically resumes durable
phases, while HTTP only waits for their verified result. Hard reset uses a
separate identity-pinned Docker purge followed by fresh bootstrap.

**Tech Stack:** existing Go 1.27.1, pure-Go SQLite/sqlc, OpenAPI 3.1/oapi-codegen,
Podman libpod REST, Linux namespaces/netlink/nftables, coder/websocket,
Docker Engine HTTP API, Bash and GitHub Actions. No new daemon or cgo dependency.

**Spec:** [2026-10-02-m1-slice-4-recovery-reset-design.md](../specs/2026-10-02-m1-slice-4-recovery-reset-design.md).
Read it together with the [M1 integration design](../specs/2026-09-23-m1-two-instances-ping-design.md).

**Status:** Approved for native execution, 2026-10-02, starting with 4A and
independent reviews at each 4A–4E delivery boundary. All tasks remain
unimplemented at approval. The user authorized committing/pushing the docs
to main and separately authorized signed-off local implementation commits
and privileged isolated-appliance tests, cleaning only test-owned objects.
Implementation pushes and merges remain separate decisions; slice-3
authorization is not inherited.

**Execution checkpoint:** 4A (Tasks 1–2) is implemented and independently
reviewed locally on `codex/m1-slice4-native`, product head `25a455c`.
The [4A report](../../tests/M1-slice4a-local-verification.md) records tests,
the crun refinement and the resolved healthy-resync review finding. No
implementation push/merge or native Ubuntu CI result is claimed. Continue
with 4B; Tasks 3–11 remain unimplemented.

**Base:** merged main `84462f8` (PR #6). Reuse a suitable managed worktree
and prepare a dedicated `codex/` branch at execution time. The preserved
dirty `codex/m1-slice3-plan` checkout is not the execution base. Follow the
worktree skill, inspect attached worktrees before creating one, and preserve
all existing user work. Do not replay slice-1/2/3 completed task checklists.

## Global Constraints

- One implicit `default` workspace, explicit VPCs, `/v1/workspaces/default/...`, no later-milestone resource or general workflow framework.
- Fixed `t3.micro`: 2 vCPU, 1 GiB, 512 tasks; appliance defaults: 2 CPUs, 4 GiB, 4096 tasks. Linux 5.15+, cgroup v2, Go 1.27.1 and normal `CGO_ENABLED=0` builds.
- Keep `userns=auto`, default capabilities plus `NET_ADMIN`, default seccomp, no devices, and unprivileged ICMP. No host network/PID mode, host bind mount, host firewall/global sysctl change, or runtime subnet network.
- SQLite is the only durable authority. Reset phases, invocation aliases and fences live there; queues and handle/effect registries are ephemeral observations.
- Edit `api/openapi.yaml` and `internal/store/queries.sql` before generation. Never hand-edit generated files or already-applied migrations; add migrations 0005 and 0006.
- Whole-appliance startup refreshes only private ephemeral `/var/lib/nephos/runroot`, `/run/libpod`, and `/run/crun` at their current configured paths, before first Podman use; graphroot `/var/lib/nephos/containers` and writable roots remain persistent. No daemon-only runtime-directory reset. Controlled 4A testing proved crun's stale status otherwise rejects starting a retained container ID; refreshing only its temporary directory preserved that exact root and marker.
- Soft reset keeps events, 24-hour create replay snapshots and kernel-index allocation history. Only a complete clean leak proof permits success/fence release.
- Default reset waiter: two minutes, `--timeout` override; drain/stop maximum 25 seconds, nested stop maximum 15 seconds within it, Docker stop 35 seconds. These new engineering constants require validation, not unmeasured timing claims.
- Every task uses a red/green test cycle and signed-off Conventional Commits when authorized. Expected results below are not checks already run.
- No M1 acceptance box is checked until actual native Ubuntu 24.04 CI and Windows 10/WSL2/Docker Desktop evidence support it. Cross-builds do not prove native Windows/macOS console runtime behavior; M8 random chaos stays out of M1.
- A controlled failure of an accepted platform assumption requires a superseding ADR, not weakened tests or silent root replacement.

## Review Focus

- A stale ID can name another valid managed container: every operation must reject it, and CAS failure must not delete a rediscovered root (Tasks 1–2).
- A POST can lose its connection after commit, or its completed key can replay after new resources exist: cleanup continues once, and replay must never delete the later resources (Tasks 6–8).
- A namespace can have no pathname but still hold rules, links or sockets: join owned references and require complete independent inventory, not empty `ip netns` output (Tasks 3–5, 10).
- A blocked console or timed-out external create can survive HTTP shutdown: admission, worker/effect barriers and authoritative rediscovery must prevent success before cleanup (Tasks 3, 5, 7, 10).
- A named Docker object can be replaced or a volume can be foreign/in-use: validate both before deletion, pin container ID, recheck volume identity and never fresh-start after partial purge (Task 9).

## Delivery and file map

| Increment | Tasks | Reviewable boundary |
|---|---|---|
| 4A | 1–2 | Identity-safe retained-root recovery and running ENI convergence. |
| 4B | 3–5 | Lifetime/stop supervision, owned orphan collection and complete leak evidence. |
| 4C | 6–8 | Durable reset state machine, automatic continuation, synchronous API/CLI and explicit retry. |
| 4D | 9 | Docker identity-safe purge and fresh bootstrap. |
| 4E | 10–11 | Real e2e/demo/platform evidence and conditional milestone closure. |

Use this order. Review each boundary; do not combine unrelated fixes into
the final evidence change. These are subdivisions of M1 slice 4, not new
roadmap milestones or implied authorization to publish stacked PRs.

- `internal/compute/runtime.go`, `podman/{instance,exec,inventory}.go`: expected identities, provenance, PID observations and runtime inventory/stop. Keep small adapter files; update fakes and console callers with the interface.
- `internal/model/{instance,leak}.go`, store migration 0005 and `{instances,instance_reconcile}.go`: provisioning preservation fact, engine-independent leak DTOs and guarded runtime rebind/status transitions.
- `internal/reconcile/{instance,orphans}.go`, `cmd/nephosd/instance_network.go`: retained discovery, running ENI verification and orphan coordination.
- `internal/lifecycle/{gate,registry}.go`: ephemeral effect/admission barrier and reference/session accounting, no store/API/engine imports.
- `internal/network/netns/{netns_linux,instance_linux,lifetime_linux}.go`, topology `{eni_linux,eni_policy_linux,inventory_linux}.go`: namespace identity/ref accounting, strict child cleanup and broad marked-object inventory. Linux build tags stay local.
- `internal/leakcheck/{check,types}.go`: cross-engine complete/clean result from narrow interfaces; no destructive API decisions.
- `internal/model/reset.go`, store migration 0006 and `reset.go`, `internal/service/reset.go`: durable operation/aliases/fence and transactional phase decisions.
- `internal/reconcile/reset.go`: daemon-owned worker, barriers/verification, startup resume.
- `api/openapi.yaml`, generated server/client, `internal/apiserver/reset.go`, `cmd/nephos/{reset,reset_client}.go`: reset-only authenticated contract and synchronous client waiting.
- `cmd/nephosd/{main,lifecycle}.go`, `internal/apiserver/console.go`, `images/appliance/entrypoint.sh`: stop/readiness/admission/console integration. Split main rather than accumulating unrelated responsibilities.
- `internal/appliance/{engine,docker,lifecycle,purge,lock}.go`, `cmd/nephos/lifecycle.go`: pinned Docker identities, serialized lifecycle and hard reset.
- `tests/m1-e2e.sh`, `tests/e2e/m1_test.go`, test-only fixtures, `Makefile`, `.github/workflows/ci.yml`, `docs/demos/M1.md`: replayable proof, artifacts and guarded cleanup.
- Canonical docs and [verification plan](../../tests/M1-slice4-verification-plan.md): preserve old evidence, add current results only when actually observed.

### Task 1: Bind runtime operations to expected instance identity

**Files:** Modify `internal/compute/runtime.go`, `internal/compute/podman/instance.go`,
`internal/compute/podman/exec.go`, `internal/reconcile/instance.go`,
`internal/apiserver/{server,console}.go`, related runtime fakes.
Create `internal/compute/podman/identity_test.go`.

**Interfaces:** define `compute.Identity{WorkspaceID,InstanceID string}`,
`compute.Reference{Identity Identity, ID RuntimeID}` and
`compute.CreateResult{Reference Reference, Created bool}`. Extend
`Status{Running bool, PID int}`. `Runtime.Lookup(ctx context.Context,
identity Identity) (Reference,error)` returns `compute.ErrNotFound` only for
authoritatively absent expected containers. `Create(ctx, model.Instance)
(CreateResult,error)` distinguishes discovery; `Start(ctx, Reference) error`,
`Delete(ctx, Reference) error`, `Inspect(ctx, Reference) (Status,error)`,
`Exec(ctx, Reference, ExecRequest) (ExecSession,error)` replace ID-only calls.
Every adapter operation validates current observed metadata against the reference.

- [x] **Step 1: Add `TestRuntimeExpectedIdentity` and `TestCreateProvenance`.** Table cases: correct full ID/name/all labels/annotation accepted; ID of another owned instance, wrong workspace, missing/contradictory marker or name collision rejected by inspect/start/delete/exec; existing expected container returns `Created:false`, fresh create `true`. Assert zero mutating calls in rejection cases. Task 3 adds Stop to this same method matrix.
- [x] **Step 2: Run `go test ./internal/compute/podman -run 'TestRuntimeExpectedIdentity|TestCreateProvenance' -count=1`.** Observed FAIL on old ID-only/provenance behavior, not an unrelated fixture error.
- [x] **Step 3: Implement the exact interfaces above and adapt every caller/fake atomically.** Split metadata validation/discovery from mutation; do not trust a label subset or turn inspection errors into absence.
- [x] **Step 4: Run `go test ./internal/compute/... ./internal/reconcile ./internal/apiserver ./cmd/nephosd -count=1` and `make cross`.** Observed PASS with unchanged console wire behavior and no added platform-only import in shared code.
- [x] **Step 5: Review the diff and commit when authorized:** `git commit -s -m "fix(compute): bind runtime operations to expected instances"` after staging only this task's files (`8a1acd2`).

### Task 2: Recover retained roots and live ENIs without destructive re-create

**Files:** Modify `internal/model/instance.go`, `internal/store/{queries.sql,instances.go,instance_reconcile.go}`,
`internal/reconcile/instance.go`, `cmd/nephosd/instance_network.go`,
`internal/network/topology/{eni_linux,eni_policy_linux}.go`, `images/appliance/entrypoint.sh`.
Create `internal/store/migrations/0005_instance_provisioned.sql` and regression tests;
regenerate sqlc. Extend `tests/image_contract_test.go`.

**Interfaces:** `model.Instance.Provisioned bool` is internal preservation
state, not an API option. `Store.RebindRuntimeID(ctx context.Context,
instanceID string, generation int64, oldID, newID string) error` compares
generation, old cache and deletion intent; `ErrStaleSnapshot` remains a
write-race signal. Set provisioned with first running observation/event in
one transaction; migration backfills running or previously observed rows.
Legacy nonempty cached IDs always require discovery before any fresh create.
Extend `InstanceNetwork` with `EnsureRunning(ctx context.Context,
instance model.Instance, ref compute.Reference, status compute.Status) error`;
wire it to `netns.OpenInstance` and existing `Engine.EnsureENI`.

- [x] **Step 1: Add `TestRediscoveredRootSurvivesStaleRuntimeID`, `TestRuntimeRebindTerminateRace`, `TestProvisionedRootMissingFails`, and `TestRunningInstanceRepairsENI`.** Use runtime/store fakes with a retained marker witness: stale nonempty/empty cache rebinds expected existing container with zero deletes; another instance's cache does not mutate it; concurrent terminate only removes expected identity; missing provisioned container creates zero replacement roots. Include pre-running retry and conservative migration cases. Running state with missing ENI repairs before observed-running success.
- [x] **Step 2: Run `go test ./internal/reconcile ./internal/store -run 'TestRediscovered|TestRuntimeRebind|TestProvisioned|TestRunningInstance|TestMigration' -count=1`.** Observed FAIL demonstrating old destructive stale-cache behavior or missing preservation/repair assertions.
- [x] **Step 3: Implement CAS/provenance-aware reconciliation and migration, regenerate store code.** An ID-write failure alone never deletes a rediscovered root. Add topology tests for obsolete owned inode replacement versus foreign/ambiguous refusal. Add entrypoint fresh private tmpfs at current runroot/libpod tmpdir/crun state before first Podman command, preserving graphroot and never refreshing on daemon-only restart. Keep admission and hook pre-PID-1 behavior unchanged. Healthy resync now observes without link/counter mutation; drift still repairs fail-closed.
- [x] **Step 4: Run `go test ./internal/store ./internal/reconcile ./tests -count=1`, `make generate-check`, and the existing privileged ENI/Podman integration tests inside a test-owned appliance.** Retained-file/IP/ENI/MAC/runtime-identity down/up and running drift scenarios pass in `tests/instance-ping-smoke.sh`. Runtime absence/collision unit tests fail closed. Exact platform/versions and independent review are in the 4A report; native CI remains pending.
- [x] **Step 5: Commit when authorized:** `git commit -s -m "fix(reconcile): preserve instance roots during restart recovery"` with only reviewed task files staged (`81141f1`, plus reviewed non-disruptive-observation fix `25a455c`).

### Task 3: Own and drain daemon effects, consoles, and nested stop

**Files:** Create `internal/lifecycle/{gate,registry}.go` with tests and
`cmd/nephosd/lifecycle.go`; modify `cmd/nephosd/main.go`,
`internal/apiserver/{server,console}.go`, `internal/compute/runtime.go`,
`internal/compute/podman/instance.go`, reconcilers/hook wiring,
`images/appliance/entrypoint.sh`, `internal/appliance/docker.go`.

**Interfaces:** `lifecycle.Gate.Enter(ctx context.Context, workspace string)
(*Lease,error)` accounts for external effects until their completion,
including uncertain completion. `Lease.Close() error` closes that owner's
participation, not its outstanding children. `Gate.EnterChild(ctx
context.Context, parent *Lease) (*Lease,error)` permits proven child work
while drain is pending; a parent/child family remains counted until all its
leases close. Daemon wiring maintains a pending-start map from exact
`compute.Reference` to its parent lease, registers before runtime Start and
resolves a hook child only after existing private-peer/runtime/namespace
identity checks. No hook-provided parent label/token alone grants admission.
`Gate.Exclusive(ctx
context.Context, workspace string, fn func(context.Context) error) error`
blocks new effects, waits existing effects, runs trusted maintenance `fn`
without recursively entering the gate, then releases admission. The
exclusive lease survives through the caller's success commit. Registry
`Track(workspace,kind,identity string, cancel context.CancelFunc)
(release func())`, `Drain(ctx context.Context, workspace string) error`,
`Outstanding(workspace string) []Reference` owns sessions/references;
`lifecycle.Reference{Workspace,Kind,Identity string}` is observational only.
`Runtime.Stop(ctx context.Context, ref compute.Reference, timeout
time.Duration) error` is internal lifecycle, not a new learner command.

- [ ] **Step 1: Add `TestConsoleDaemonShutdownJoins`, `TestDaemonFailureDrainsSessions`, `TestEffectBarrierWaitsForCompletion`, `TestStartHookChildCompletesDuringDrain`, and `TestNestedStopPrecedesRuntimeShutdown`.** Exercise TTY/non-TTY endless commands, blocked output, multiple sessions on one instance, unexpected server/controller failure and cleanup errors. Assert admission is closed, session-only exec close/reap, no store access after closure, and nested stop happens with Podman alive. Gate release must follow deferred cleanup, not early result send. Deterministically admit Start, request exclusivity, then admit its verified hook child; parent and hook finish, unrelated/forged child admission fails, and exclusive work starts only after both close. Extend `TestRuntimeExpectedIdentity` to Stop, including another valid owned instance ID.
- [ ] **Step 2: Run `go test ./internal/lifecycle ./internal/apiserver ./cmd/nephosd -run 'TestConsoleDaemon|TestDaemonFailure|TestEffectBarrier|TestStartHookChild|TestNestedStop' -count=1`.** Expected FAIL before new lifecycle ownership, with a bounded test deadline rather than arbitrary sleeps.
- [ ] **Step 3: Implement registry/gate and bind both HTTP servers and upgraded sessions to a daemon-owned context.** Add the pending-start/verified child-hook lease protocol, with child references retaining their family after parent completion. Provide a console-admission callback; Task 6 installs its transactional fence check when reset storage exists. Release admission outside that transaction after cleanup, never hold SQLite during exec/close. Use one cleanup path for signals and unexpected exits. Add 25-second total daemon budget/15-second nested-stop sub-budget and 35-second Docker stop; entrypoint waits daemon cleanup before stopping Podman. Retain containers on timeout. Keep every effect gate/registry dependency below service/API layers.
- [ ] **Step 4: Run `go test ./internal/lifecycle ./internal/apiserver ./internal/compute/... ./cmd/nephosd ./internal/appliance -count=1` and `make test-race`.** Validate actual long-lived PTY/non-PTY exec shutdown and stopped retained containers in a test appliance when authorized. Expected no stranded pumps/exec and no multiplied per-instance stop budget; systemic shutdown error cannot report clean success.
- [ ] **Step 5: Commit when authorized:** `git commit -s -m "fix(lifecycle): drain consoles and stop nested instances safely"`.

### Task 4: Inventory and collect strictly owned runtime/network orphans

**Files:** Create `internal/compute/podman/inventory.go`,
`internal/network/topology/inventory_linux.go`, `internal/reconcile/orphans.go`
and tests. Modify `internal/compute/runtime.go`, `internal/reconcile/network.go`,
topology/namespace ownership validation and daemon startup/resync wiring.

**Interfaces:** `compute.ObservedContainer{Reference Reference, Name string,
Managed bool, OwnershipError string}` represents marked candidates, including
ambiguous ones. `Runtime.Inventory(ctx context.Context)
([]ObservedContainer,error)` returns complete observations or an error.
Define `topology.Inventory{Namespaces []NamespaceObservation, Findings
[]Finding}` with `NamespaceObservation{Name string, Inode uint64, Owned
bool, OwnershipError string}` and `Finding{Kind,Identity,Reason string}`.
`Engine.Inventory(ctx context.Context) (Inventory,error)` and
`Engine.CollectOrphans(ctx context.Context, desired []model.VPC, subnets
[]model.Subnet, enis []model.ENI) error` keep destructive scope local. Include
subnet ownership to prove each desired ENI belongs to its expected VPC, not
merely that its ID exists elsewhere. Coordinator
`reconcile.OrphanNetwork` exposes the inventory/collection methods above;
`reconcile.CollectOrphans(ctx context.Context, s *store.Store, runtime
compute.Runtime, network OrphanNetwork) error` uses consistent desired
snapshots and exclusive effect coordination; it never deletes a live row's
container merely because the cached ID differs.

- [ ] **Step 1: Add `TestRuntimeOrphanCollection`, `TestENIOrphanCollection`, `TestAmbiguousOwnershipFails`, and `TestNamespaceAliasRejected`.** Rowless fully owned containers/ENIs are removed; pending rows with NULL IDs and desired retained containers survive. Malformed marked objects are reported, generic/foreign unmarked links remain untouched, root/same-other-VPC namespace aliases and symlinks are rejected before any network mutation. Foreign markers never become deletion permission by prefix alone.
- [ ] **Step 2: Run `go test ./internal/compute/podman ./internal/reconcile -run 'TestRuntimeOrphan|TestAmbiguousOwnership' -count=1` plus Linux topology/netns ownership tests.** Expected FAIL on missing runtime inventory and incomplete alias/orphan checks.
- [ ] **Step 3: Implement strict identity proofs and broad candidate observation.** Add marked-child cleanup inside still-live VPCs, preserving desired IP/MAC/ENI identities and cached images. Run startup and periodic collection under the effect barrier; deletion order stays children before parents. Correlate namespace identity to the appliance root and desired namespace set before configuring/unmounting it. Do not import service/API into engines.
- [ ] **Step 4: Run unit/race checks and tagged integration tests in the isolated appliance.** Seed owned rowless runtime/ENI objects plus foreign neighbors and ambiguous aliases; expected owned cleanup, foreign preservation, and visible ambiguity failure. Inspection errors must not return an empty inventory.
- [ ] **Step 5: Commit when authorized:** `git commit -s -m "feat(reconcile): collect proven owned runtime and ENI orphans"`.

### Task 5: Prove complete leak results and namespace lifetime cleanup

**Files:** Create `internal/leakcheck/{types,check}.go`, `internal/model/leak.go` and tests,
`internal/network/netns/lifetime_linux.go` and tagged integration tests,
test-only `tests/fixtures/namespacehold/main.go`; modify namespace/netlink
handle construction/close sites and Task 3/4 observation adapters.

**Interfaces:** define engine-independent `model.LeakFinding{Kind,Identity,
Reason string}` and `model.LeakResult{Complete,Clean bool, Findings
[]LeakFinding, InspectionErrors []string}` in `internal/model/leak.go`;
`leakcheck.Finding` and `leakcheck.Result` are aliases to these model DTOs.
`leakcheck.Checker.Check(ctx context.Context, workspace string)
(Result,error)` consumes runtime/network inventories, product registry,
desired-resource/lease counts and known-identity process/cgroup corroboration
through injected interfaces. Checker is non-destructive; Task 4 owns GC.
Only an exclusive drained caller can authorize a success decision.
Namespace handles/duplicates, sockets and workers register before exposure
and unregister after actual close/reap; registry is never a durable owner map.

- [ ] **Step 1: Add `TestLeakCheckRequiresCompleteInspection`, `TestUnlinkedNamespaceReferenceBlocksClean`, and `TestOutstandingEffectBlocksClean`.** Denied/failed runtime, nftables, netlink or required `/proc` inspection returns incomplete/not clean; remaining cgroup/task/exec/duplicate/socket keeps failure. A worker whose result arrives before deferred handle close still blocks success. Generic foreign unmarked objects are not deleted or misreported as owned leaks.
- [ ] **Step 2: Run `go test ./internal/leakcheck ./internal/lifecycle -count=1`.** Expected FAIL before complete-vs-clean accounting; use test barriers, not sleeps, to expose deferred lifetime.
- [ ] **Step 3: Implement registered reference lifetimes, targeted `/proc` namespace/task/FD corroboration and object-level errors.** Check/remove owned kernel residue while the namespace is pinned before unlink; inspect known owned identities after unlink. Reject uncertainty; never claim `/proc` sees all socket-only namespaces or add unvalidated cross-process socket syscalls.
- [ ] **Step 4: Run unit/race and `integration` namespace fixtures inside the appliance.** Independently pin a namespace FD after unlink with marked link/ruleset residue and require a leak finding. Separately demonstrate a socket-only hold can evade namespace-path/FD enumeration; the equivalent product-registered socket must prevent a clean result until closed. Assert zero references and named/known marked objects before outer Docker teardown; record the independent witnesses.
- [ ] **Step 5: Commit when authorized:** `git commit -s -m "feat(leakcheck): verify owned effects and namespace lifetimes"`.

### Task 6: Persist reset decisions, fences, keyed aliases, and phase order

**Files:** Create `internal/model/reset.go`,
`internal/store/migrations/0006_reset_operations.sql`, `internal/store/reset.go`,
`internal/service/reset.go` and tests. Modify store queries, resource services,
idempotency integration and console admission; regenerate sqlc.

**Interfaces:** define `model.ResetState` (`running`,`failed`,`succeeded`),
`model.ResetPhase` (`instances`,`subnets`,`vpcs`,`verify`,`done`) and
`model.ResetOperation{ID,WorkspaceID string, State ResetState, Phase ResetPhase,
StateReason string, CreatedAt,UpdatedAt time.Time}`. `service.ResetRequest{
IdempotencyKey,ResumeOperationID string}`; `ResetService.Decide(ctx
context.Context, workspace string, req ResetRequest) (model.ResetOperation,
error)`, `Status(ctx context.Context, workspace,id string)
(model.ResetOperation,error)`. Store `Tx.CheckCreateAllowed(ctx,workspace)
error`, `Tx.AdvanceReset(ctx,operationID string, expected ResetPhase,
next ResetPhase, now time.Time) error` and `Store.GetReset(ctx,workspace,id
string) (model.ResetOperation,error)` own SQL; empty status ID means latest.
Store terminal leak result JSON with success in Task 7. Add a partial unique
index for one fenced running/failed operation per workspace; invocation alias
uniqueness is workspace/key and includes canonical request hash/operation ID.

- [ ] **Step 1: Add `TestResetCreateFenceTransaction`, `TestResetPhaseDecisionsAtomic`, `TestResetKeyReplayAndResume`, and `TestSoftResetPreservesHistory`.** Channel-controlled transaction race: pre-fence creates included; post-fence new creates write zero IDs/IPs/indexes/events; harmless create replay still works. Initial intent and first layer deletion/events roll back together on failure; reentry does not bump generations. Concurrent reset keys join; mismatch conflicts; failed replay never implicitly resumes; explicit named resume cannot select a newer operation. Events remain monotonic and create replay/index history survive resource deletion.
- [ ] **Step 2: Run `go test ./internal/store ./internal/service -run 'TestReset|TestSoftReset' -count=1`.** Expected FAIL before persisted reset state/fence. Test a service decision directly so an HTTP-only mutex cannot pass it.
- [ ] **Step 3: Implement migration, atomic service decisions and phase-intent writes.** Do not hold SQLite across runtime/kernel work or apply the fence to generic `WithTx`. Preserve active/failed aliases; retain completed results at least 24 hours after completion. A new unkeyed target request while a failed operation is fenced joins its failure, not fresh deletion. Register console admission in its fence-check decision transaction and reject starts after the fence; existing admitted sessions belong to the drain.
- [ ] **Step 4: Run `go test ./internal/store ./internal/service ./internal/apiserver -count=1`, `make generate-check` and `make test-race`.** Expected ordered, transactional state with unchanged reads/delete/terminate and migration from slice-3 databases.
- [ ] **Step 5: Commit when authorized:** `git commit -s -m "feat(store): persist reset phases and transactional creation fences"`.

### Task 7: Automatically resume reset and commit success only after proof

**Files:** Create `internal/reconcile/reset.go` and tests; modify
`cmd/nephosd/{main,lifecycle}.go`, relevant store reset status/result helpers
and controller scheduling/barrier integration.

**Interfaces:** `reconcile.ResetCoordinator.Run(ctx context.Context) error`
and `ResumeStartup(ctx context.Context) error` own the daemon worker.
`Wait(ctx context.Context, operationID string) (model.ResetResult,error)`
observes SQLite/current notifications only; define `model.ResetResult{
Operation ResetOperation, LeakCheck LeakResult}` in `internal/model/reset.go`
using Task 5's model DTOs, avoiding a model-to-engine import.
`Store.FinishReset(ctx,operationID string,
expectedPhase model.ResetPhase,resultJSON []byte,now time.Time) error`
records verified result/success/fence release atomically. The worker's
exclusive lease encloses orphan GC, checker, success commit and scheduling
revalidation; release does not resurrect stale work.

- [ ] **Step 1: Add `TestResetResumesEveryBoundary`, `TestResetWaiterCancelDoesNotAbort`, `TestResetFailureKeepsFence`, `TestFailedResetResyncDoesNotAdvance`, and `TestResetNoLateEffectsAfterSuccess`.** Inject interruption before/after initial/phase commits, external deletions, drain, proof and final commit; restart converges once. Cancel before decision commits aborts, after commit only waiter detaches. Transient teardown retries with retained leases/parent rows; leak/ownership/incomplete inspection persists failed and keeps fence. Repair the original fault and run startup/resync: already committed deletion and strict orphan GC may progress, but next-layer intents, operation success and fence release remain blocked until explicit retry. A timed-out create/delete and delayed hook may not materialize unobserved residue after success.
- [ ] **Step 2: Run `go test ./internal/reconcile -run 'TestReset|TestFailedReset' -count=1`.** Expected FAIL before daemon-owned resume/barrier behavior. Ensure injected faults preserve the exact committed database/effect boundary.
- [ ] **Step 3: Implement instances→subnets→VPCs→verify, restarting from persisted phase.** Advance only after physical cleanup and row/lease removal. On startup recover running reset before starting ordinary instances; terminal failed operation stays fenced/readable until explicit continuation. Only coordinator advancement/new-layer intents/success/fence release pause; existing deletion intents and strict periodic orphan GC remain idempotently reconcilable, and explicit user teardown stays dependency-checked. Check failed inspection separately from retryable resource errors. Drain effects/sessions, collect only proven orphans, check complete clean inventory and commit success under the same exclusive lease.
- [ ] **Step 4: Run reset/unit/race tests and daemon readiness tests.** Expected healthy API/read access with a visible resource-specific or terminal-reset failure after initial sweep, but systemic store/runtime/inventory failure is non-ready. Add real interruption/resume cases to Task 10 rather than claiming fake-engine coverage proves Podman/kernel cleanup.
- [ ] **Step 5: Commit when authorized:** `git commit -s -m "feat(reconcile): resume durable resets and verify cleanup before success"`.

### Task 8: Specify and expose synchronous keyed reset API and CLI

**Files:** Modify `api/openapi.yaml`, `api/contract_test.go`,
`internal/apiserver/server.go`, `cmd/nephos/main.go`, daemon wiring;
create `internal/apiserver/reset.go`, `cmd/nephos/{reset,reset_client}.go` and
tests; regenerate server/client before implementing generated methods.

**Interfaces:** OpenAPI operation IDs `resetWorkspace`, `getWorkspaceReset`.
POST/GET `/v1/workspaces/{workspace}/reset` exactly as spec §6. Generated
`ResetRequest{resume_operation_id?}`, `ResetOperation` with snake_case DTO
fields, and `ResetResult{operation,leak_check}` map internal types. POST needs
`Idempotency-Key`; GET optional `operation_id`. Implement generated handler
signatures after generation; service interfaces consume Tasks 6–7 only.
CLI `reset [--timeout D] [-o json]`, `reset --status`, `reset --retry`; reject
`--hard` with status/retry. Default wait two minutes; dedicated HTTP transport
uses remaining waiter deadline, not the existing ten-second resource timeout.

- [ ] **Step 1: Add `TestResetContract`, `TestResetAPIWaitAndReplay`, and `TestResetCLI`.** Pin strict schema/auth/workspace/Host/Origin, 200 only complete clean success, 409 fence/mismatch/failure and 504 waiter timeout. Completed key replay after newly created resource performs zero deletion. Concurrent calls join; failed replay does not retry; explicit `resume_operation_id` retry uses same operation. CLI keys survive transport retry, reports known operation ID on detach, preserves exact JSON, and status GET cannot mutate.
- [ ] **Step 2: Run `go test ./api ./internal/apiserver ./cmd/nephos -run 'TestReset' -count=1`.** Expected FAIL on absent schema/routes/CLI before generation/implementation.
- [ ] **Step 3: Edit OpenAPI and generate, then implement handlers/client/wiring.** Keep existing error envelope: `ResetInProgress`/`IdempotentParameterMismatch`/`ResetFailed` 409, `ResetTimeout` 504, `InvalidResetID.NotFound` 404. POST cancellation never cancels the committed worker. GET is reset-specific diagnosis, not an asynchronous 202 workflow. Map operation/error ID without exposing tokens or retaining a second durable client desired-state file.
- [ ] **Step 4: Run `go test ./api ./internal/apiserver ./cmd/nephos ./cmd/nephosd -count=1`, `make generate-check`, `make test-race`, `make cross`, and pinned vulnerability scan from existing CI.** Expected unchanged resource/console contract and no hardcoded ten-second reset deadline. Run real soft-reset/API-auth smoke when authorized; leak assertion must precede outer teardown.
- [ ] **Step 5: Commit when authorized:** `git commit -s -m "feat(api): expose synchronous resumable workspace reset"`.

### Task 9: Purge pinned Docker objects and hard-reset with fresh credentials

**Files:** Modify `internal/appliance/{engine,docker,lifecycle}.go`,
`cmd/nephos/lifecycle.go`; create `internal/appliance/{purge,lock}.go` with
platform-specific lock files where needed and table-driven tests; reuse
credential atomic save. Update lifecycle fakes.

**Interfaces:** add `ContainerState.ID string`, `VolumeState{Name,CreatedAt
string, Owned bool, ConsumerIDs []string}`. `InspectVolume` includes all
container consumers, including stopped ones, or returns an inspection error;
purge permits only the captured old appliance ID before any destructive
step and rechecks consumers before volume removal.
`Engine.Stop(ctx context.Context, containerID string,
timeout time.Duration) error`, `Remove(ctx,containerID string) error` and
`InspectContainerID(ctx,containerID string) (ContainerState,error)` target
the captured ID; volume removal remains fixed-name with immediate reinspection.
`Manager.HardReset(ctx context.Context, limits Limits) error` shares
`Manager.Purge(ctx context.Context) error`. `AcquireLifecycleLock(ctx
context.Context, configDir string) (unlock func() error, err error)` lives
under the CLI config directory; all Up/Down/Purge/HardReset acquire once,
private helpers avoid reentrant lock acquisition. Preserve actual limits per
field except `Limits.Explicit` overrides; absent old container uses defaults.

- [ ] **Step 1: Add `TestHardResetPinsIdentity`, `TestHardResetFailureStages`, `TestHardResetPreservesLimits`, and `TestLifecycleSerialization`.** Foreign/in-use/replaced volume rejects before removal of foreign objects; initial foreign volume or unrelated consumer (running or stopped) causes zero stop/removal of the owned container. Consumer inspection failure also blocks purge. Simulate old name replaced between inspect/remove: replacement untouched. Inject stop/remove/volume remove/preflight/up/readiness/save failures; no Up after uncertain purge, no false success or premature credential replacement. Corrupt DB/unhealthy API/invalid old token must not prevent purge.
- [ ] **Step 2: Run `go test ./internal/appliance ./cmd/nephos -run 'TestHardReset|TestLifecycleSerialization' -count=1`.** Expected FAIL on current name-only purge and missing workflow. Include unchanged ordinary Down/Up reuse behavior.
- [ ] **Step 3: Implement locked prevalidate-both/pinned-ID/reinspect-volume purge.** Verify captured old ID/volume absent before fresh same-name creation; preflight replacement first when possible. No forced foreign consumer cleanup and no atomic-volume-removal promise Docker cannot provide. Fresh ready/empty state/new token precedes atomic credential save; each partial stage error names completed stages without token values. Set Docker outer stop to Task 3's budget.
- [ ] **Step 4: Run unit/race/cross-build tests and an authorized real-Docker hard-reset fixture.** Expected captured old objects gone, empty fresh resources, new token, old token rejected, ordinary restart token retained, actual limits preserved/explicit overrides applied. Keep foreign neighbors and normal credentials untouched; use isolated CLI config.
- [ ] **Step 5: Commit when authorized:** `git commit -s -m "feat(appliance): hard reset pinned owned Docker objects safely"`.

### Task 10: Add replayable demo, real e2e target, and platform evidence

**Files:** Create `docs/demos/M1.md`, `tests/m1-e2e.sh`,
`tests/e2e/m1_test.go` (`e2e` build tag), test-only deterministic fault/residue
fixtures and `docs/tests/M1-slice4-verification.md`. Modify `Makefile`,
`tests/ci_workflow_test.go`, `.github/workflows/ci.yml`, connectivity matrix
and development commands. Never ship fixture controls in production images.

**Interfaces:** `make e2e` runs `go test -tags e2e ./tests/e2e -count=1`
against locally built images and binaries; the wrapper runs the same shell
scenario the demo documents. Script refuses pre-existing fixed Nephos objects,
uses temporary task-specific CLI config, tracks captured object identities,
and cleans only test-owned Docker objects. Planned fault helpers supply
barrier-controlled phases, bounded namespace/exec holders and independent
inventory witnesses; production workers do not expose public fault endpoints.

- [ ] **Step 1: Add a failing `TestNativeDockerWorkflowRunsM1E2E`/Makefile contract assertion.** It requires real e2e after local image/build preparation, host nftables availability, failure artifact collection before cleanup, and `actions/upload-artifact` under `always()` using the existing pinned-action convention. Add actual harness assertions for every scenario in the verification plan, not only row/name counts.
- [ ] **Step 2: Run `go test ./tests -run 'TestNativeDockerWorkflowRunsM1E2E' -count=1` and `bash -n tests/m1-e2e.sh`.** Expected contract FAIL before replacing the placeholder; syntax errors are fixed separately and are not valid red evidence for behavior.
- [ ] **Step 3: Implement demo/harness/CI artifacts and cleanup traps.** Write marker, capture IP/ENI/MAC/runtime identity, down/up, verify marker and real positive/isolated/spoof packet controls; run soft reset, phase interruptions/retry/history and hard reset/new token/limits. Deliberately leave owned residual FD/socket/exec/cgroup/kernel objects to prove the checker fails, and retain foreign objects to prove cleanup scope. Resolve partial external effects. Assert inside the still-running appliance before outer removal. Compare post-Up host baseline around inner actions and whole-lifecycle before/after; snapshots do not prove no transient mutation.
- [ ] **Step 4: Run `make dev-ami`, `make appliance`, `make build`, existing smokes, `make e2e`, `make ci`, `make generate-check`, `go mod tidy -diff`, vulnerability scan, and `git diff --check` when authorized.** Record source commit/image IDs/actual packages/platform/limits and preserve failing logs plus inventories before cleanup. Require native Ubuntu 24.04 CI and matching manual Windows 10/WSL2/Docker Desktop demo; skipped host nftables on WSL is explicitly unavailable, not pass. Publishing CI requires separate push/PR authorization. Expected all acceptance witnesses pass or a precise blocker/ADR is recorded; no artificial test weakening.
- [ ] **Step 5: Commit verified tests/demo/evidence when authorized:** `git commit -s -m "test(m1): prove restart and reset closure on the real appliance"`.

### Task 11: Close M1 only against the evidence ledger

**Files:** Modify `docs/ROADMAP.md`, `docs/ARCHITECTURE.md` §5.5/7/11,
`docs/RISKS.md`, `docs/DEVELOPMENT.md`, `AGENTS.md`, `CHANGELOG.md`,
`docs/tests/M1-connectivity-matrix.md`, slice-4 verification report and this
plan's completed-step record; retain slice-3 checkpoint as historical evidence.

**Interfaces:** no new product interface. Acceptance checklist maps every
roadmap criterion to exact native CI/manual QA witnesses and source/image
identity. This task cannot fabricate or substitute evidence for Task 10.

- [ ] **Step 1: Audit every M1 acceptance checkbox against the verification report.** If required native or WSL evidence is missing, leave affected boxes and M1 open; list the exact unverified condition. Do not report cross-builds as manual console QA or M1 deterministic tests as M8 chaos closure.
- [ ] **Step 2: Review changes independently against the spec and run the final required checks from Task 10 on the final code head.** Documentation-only evidence amendments may retain prior identical-code results with exact commit context; a runtime change requires rerun. Resolve regressions or accepted-ADR conflict before changing milestone status.
- [ ] **Step 3: Update all progress/routing/fidelity/changelog claims with actual results.** Only full acceptance sets M1 complete and M2 active. Broader platform/security risks remain tracked; a real assumption failure needs its superseding ADR. Record any remaining support limitations explicitly.
- [ ] **Step 4: Run `git diff --check`, local Markdown link validation, `git diff --stat`, and `git status --short --branch`; check original checkout user edits remain preserved.** Expected no stale slice-4 status or broken links, truthful checkboxes, no unrelated code/assets, no product changes without tests.
- [ ] **Step 5: Commit when authorized:** `git commit -s -m "docs(m1): record verified milestone closure and next-stage handoff"`. Push/open PR/merge only with explicit direction and verified required checks.

## Self-review and execution handoff

Before execution, review this plan against every spec section and the five
Review Focus rows. Interfaces introduced by early tasks are consumed under
the same names later; concrete helper APIs are planned additions, not claims
they exist today. Engine adapters remain below service/API and generated
contract edits remain spec-first. Broad inventories and strict deletion must
remain distinct; lifetime barriers and SQLite fencing solve different races.

**Documentation self-review, 2026-10-02:** scope, exact interface/type usage,
step clarity, proportionality and all five Review Focus cases were checked.
Spec §2 maps to Tasks 1–2, §3 to Task 3, §4 to Tasks 4–5, §5 to Tasks 6–7,
§6 to Task 8, §7 to Task 9, and §8 to Tasks 10–11. Independent contract/QA
review findings were addressed and rereviewed: verified hook-child admission
during drain, failed coordinator versus committed-deletion semantics,
Inspect/Stop identity regressions and explicit e2e build prerequisites.
All task checkboxes stay open; no future test expectation is execution
evidence. File/API helper names and budgets are covered by the written-plan
approval, not product changes made by this planning pass.

The user approved the written plan and selected native execution on
2026-10-02, with independent 4A–4E boundary reviews and a final whole-branch
review. Begin Task 1 after isolated-worktree/baseline setup; implement the
tasks inline, rather than dispatching implementer agents. Signed-off local
commits and isolated privileged tests are authorized, with cleanup limited
to test-owned Docker objects. Pushes/merges of implementation remain separate
decisions. Preserve these slice-4 approvals rather than asking to repeat them.
