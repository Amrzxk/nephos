# M1 Slice 3: Instances, OCI Hook, Console, and Ping Implementation Plan

**Status:** Approved for native execution, 2026-09-28. The user reviewed this
plan and chose native execution. Signed-off local commits and isolated
privileged Docker tests with cleanup of only test-owned objects are
authorized. Push, PR creation and merge are not authorized by this approval.
Execution is in progress; check task evidence before claiming behavior ships.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Run two real Ubuntu system containers in different subnets of one VPC, manage them through the Nephos API/CLI, reach one from the other by ICMP, and use an authenticated out-of-band console.

**Architecture:** SQLite owns instance and ENI desired state, including the private-IP lease; a separate instance reconciler waits for converged VPC topology and drives a thin client of Podman's rootful Unix-socket REST API. A filtered OCI `createRuntime` hook asks `nephosd` over a private Unix socket to move and configure the ENI before PID 1 starts. The API and CLI expose only the fixed M1 instance shape and an authenticated WebSocket exec path; slice 4 owns full restart recovery and reset.

**Tech Stack:** Go 1.27.1 with `CGO_ENABLED=0`; existing OpenAPI 3.1/oapi-codegen v2.8.0, sqlc v1.31.1, `modernc.org/sqlite` v1.59.0, netlink v1.3.1, netns v0.0.5; Linux network namespaces and nftables; appliance-local Podman libpod REST over `/run/podman/podman.sock`; locally built `nephos-ubuntu:dev` image; `github.com/gorilla/websocket` v1.5.3 for authenticated console streaming (BSD-2-Clause, pure Go).

**Spec:** `docs/superpowers/specs/2026-09-23-m1-two-instances-ping-design.md`

**Base:** Start implementation from `main` after merged PRs #3 and #4
(slice-2 merge commit `def23f11ebe96b27328917369ed61f6f8be2b746`), including
the session handoff at `9c6d8ee` and these finalized planning changes. Use a
dedicated `codex/` branch and open a draft
PR against `main`; the earlier stacked branches no longer need to be bases.
Reuse a suitable free managed worktree. Creating commits, pushing, opening a
PR, merging, running `nephos up`, and destructive test cleanup remain subject
to explicit user authorization. The commit steps below are execution
instructions, not instructions to commit during this planning turn.

## Global Constraints

- One implicit `default` workspace and explicit VPCs only. API paths retain `/v1/workspaces/default/...`; reject unknown workspaces and unknown create fields. No `nx-edge`, default VPC, SG/NACL, SSH/key pairs, DNS, IMDS, user data, AMI or instance-type choice in M1.
- Follow accepted ADR-0003, ADR-0004, ADR-0005, ADR-0007, and ADR-0008. If a validated platform assumption fails, stop and propose a superseding ADR; never mark an unenforced behavior successful.
- Resource names remain case-sensitive trimmed UTF-8, 1–255 code points, without controls. IDs use `i-`/`eni-` plus 17 lowercase hexadecimal characters. One fixed `t3.micro` means 2 vCPU and 1 GiB memory; enforce 512 tasks (processes and threads, matching SP1), default seccomp, `userns=auto`, systemd mode, and default capabilities plus `NET_ADMIN`. The task ceiling is Nephos-specific, not an AWS attribute or a reservation; the appliance's default aggregate ceiling remains 4096, including control-plane tasks. No new configurable instance-limit flag in M1.
- IPAM reserves network, base+1, base+2, base+3, and broadcast in each subnet; assign from base+4 upward. Keep an IP stable while its instance exists. SQLite transaction plus `UNIQUE(subnet_id, private_ip)` prevents duplicate concurrent leases.
- `api/openapi.yaml` precedes generated server/client edits; `internal/store/queries.sql` precedes sqlc generation. Never hand-edit generated files. No cgo, AGPL, TCP Podman service, host bind mount, host network/PID namespace mutation, or product state outside `nephos-data`.
- Only `internal/network/netns` switches network namespaces; a switched thread never returns to the Go scheduler. Linux names derive from database short indexes and fit 15 bytes. Instances are Podman containers with an isolated, Nephos-wired network namespace, never a Podman subnet network.
- Hook applies only to containers annotated `io.nephos.instance-id` at `createRuntime`. The hook socket is appliance-private at `/run/nephos/hook.sock`. If validation or plumbing fails, hook exit is nonzero, instance cannot become `running`, and the error is visible in `state_reason`.
- Instance create is transactional with its ENI, IP, idempotency replay snapshot, and event; termination is asynchronous and blocks subnet deletion until cleanup. `running` means Podman started after successful hook plumbing, not sshd/cloud-init readiness. Console exec is explicitly out of band, while any packet emitted by its command obeys VPC routing.
- Console has one versioned WebSocket protocol with interactive PTY and non-PTY command modes. Command stdout/stderr stay separate and exit status is explicit; interactive output is combined terminal data. The CLI's out-of-band label goes to stderr. Authentication, Host/Origin rejection, and instance-state checks precede Podman exec in both modes.
- Slice 3 tests real packet behavior and isolated failure paths. Slice 4 owns retained-root restart marker, orphan cleanup/leak checker, reset levels, full M1 e2e closure and WSL2 manual QA. Do not check M1 roadmap acceptance boxes yet.
- Apply TDD per task; name the regression tests below and run them before implementation. A planned FAIL/PASS is an expectation, not evidence of a check already run. Use `make generate-check` after generation; the current target compares generated snapshots and does not require a clean Git tree. `make ci` does not include Docker e2e or generated freshness, so run those separately. Keep the six CGO-disabled cross-build targets; Linux-only socket credentials and network code need build tags and explicit unsupported-platform behavior.

## Review Focus

- A `/28` subnet has only eleven allocatable addresses (base+4 through base+14): the next create must return `AddressLimitExceeded` without a partial instance, ENI, index, or event (Task 2).
- Two concurrent launches, including same-key retries, must not share an IP or router interface or produce two containers for one instance (Tasks 2, 4, 6).
- A forged, malformed, stale, or unauthorized hook request must not move a foreign interface or start an instance; a failed hook must leave `observed_generation` behind desired generation (Tasks 5 and 6).
- Same-CIDR VPCs must remain disconnected even if each contains an instance at the same private IP; probe a remote-only target address with a positive control, never self-ping the duplicate address. Spoofing another source IP must be dropped, not merely documented (Tasks 3 and 9).
- A browser-origin WebSocket attempt, absent/invalid bearer token, or console against a non-running instance must fail without opening Podman exec. One-shot console must preserve stdout/stderr, stdin EOF, and a nonzero exit status; a truncated session cannot be success (Tasks 4, 7 and 8).

---

## File map

- `api/openapi.yaml`, `api/contract_test.go`: instance CRUD schema and console upgrade contract. `internal/apiserver/generated/*` and `pkg/client/*` remain generated.
- `internal/model/instance.go`: immutable instance/ENI snapshots and lifecycle states shared below the API.
- `internal/store/migrations/0004_instance_deletion.sql`, `internal/store/store.go`, `internal/store/queries.sql`, `internal/store/instances.go`, `internal/store/instance_reconcile.go`: additive deletion intent, observed Podman container ID, IP/ENI persistence, transactional status/event changes. Embed/apply migration 4 and retain connection-local PRAGMAs. Keep SQL confined to the store.
- `internal/service/instance.go`, `internal/service/ipam.go`: validation, allocation, idempotency, pagination, dependency and terminate decisions. No Podman or kernel calls.
- `internal/network/netns/instance_linux.go`, `internal/network/topology/eni_linux.go`, `internal/network/topology/eni_policy_linux.go`: namespace entry, veth/route/proxy-ARP convergence and source anti-spoof. No service/API imports.
- `internal/compute/runtime.go`, `internal/compute/podman/{client,instance,exec}.go`: small runtime interface and local REST adapter; no CLI invocation or durable runtime registry.
- `internal/reconcile/instance.go`: keyed instance worker, prerequisites, status and cleanup; existing VPC controller remains authoritative for VPCs/subnets.
- `internal/hook/{state,client,server_linux,server_unsupported}.go`, `cmd/nephos-hook/main.go`, `images/appliance/nephos-hook.json`: filtered, fail-closed createRuntime request and private daemon endpoint; platform-specific peer credentials must not break CLI cross-builds.
- `internal/tunnel/console/protocol.go`, `api/console-v1.md`: shared, transport-independent console frame types/limits and the documented versioned protocol. No Podman, store, or HTTP-server dependency in the protocol package.
- `internal/apiserver/{instance,console,server}.go`, `cmd/nephosd/main.go`: generated HTTP methods, authenticated WebSocket and daemon wiring/readiness.
- `cmd/nephos/{instance,console}.go`, `cmd/nephos/main.go`: generated-client instance commands and streaming console; `tests/instance-ping-smoke.sh` validates the real appliance path.
- `docs/tests/M1-connectivity-matrix.md`, `docs/ARCHITECTURE.md`, `docs/DEVELOPMENT.md`, `docs/RISKS.md`, `CHANGELOG.md`, `AGENTS.md`, `.github/workflows/ci.yml`: proof and contributor routing for this slice.

### Task 1: Specify instance CRUD before generating handlers

**Files:** Modify `api/openapi.yaml`, `api/contract_test.go`, `internal/apiserver/server.go`; regenerate `internal/apiserver/generated/server.gen.go`, `pkg/client/client.gen.go`.

**Interfaces:** Operation IDs `runInstance`, `listInstances`, `getInstance`, `terminateInstance`; `RunInstanceRequest{name,subnet_id}` only; `Instance{id,name,subnet_id,private_ip,eni_id,instance_type,state,state_reason,generation,observed_generation}`; `InstancesPage{items,next_page_token}`. Console path `/v1/workspaces/{workspace}/instances/{id}/console` requires bearer auth and a WebSocket upgrade, documented in OpenAPI as HTTP 101 with a prose wire-protocol reference added in Task 7.

**Named tests:** `TestInstanceContract` and `TestConsoleContract`. Pin the
exact fields/statuses listed below; no selectable type/image or stale PTY-only
description may appear in the generated contract.

- [ ] **Step 1: Write a failing contract test.** Assert all four method/path pairs, exact request fields with `additionalProperties:false`, required response fields, `Idempotency-Key`, bearer security, 201/202 success, 400/401/404/409 errors, `limit`/`page_token`, and the authenticated console path.
- [ ] **Step 2: Run `go test ./api -run TestInstanceContract -count=1`.** Expected: FAIL because instance paths are absent.
- [ ] **Step 3: Add the OpenAPI contract and run `make generate`.** Add explicit 501 JSON stubs for new generated methods so the intermediate commit compiles. Do not add selectable AMI, type, tags, or filters.
- [ ] **Step 4: Run `go test ./api ./internal/apiserver -count=1` and `make generate-check`.** Expected: PASS; unimplemented routes still clearly return 501.
- [ ] **Step 5: Commit with `git commit -s -m "feat(api): define M1 instance contract"`.**

### Task 2: Persist instances, ENIs, and collision-free leases

**Files:** Create `internal/model/instance.go`, `internal/store/migrations/0004_instance_deletion.sql`, `internal/store/instances.go`, `internal/service/ipam.go`, `internal/service/instance.go` and corresponding tests; modify `internal/store/store.go`, `internal/store/queries.sql`, `internal/store/resources.go`, `internal/store/store_test.go`, `internal/service/errors.go`, `internal/service/network.go`; regenerate `internal/store/sqlc/*`.

**Interfaces:** `service.NewInstances(s *store.Store, enqueue func(string), now func() time.Time) *service.Instances`; `Run(ctx context.Context, input RunInstanceInput, key string) (model.Instance, error)`, `Get(ctx context.Context, id string) (model.Instance,error)`, `List(ctx context.Context, limit int, token string) (InstancePage,error)`, and `Terminate(ctx context.Context, id string) error`. `RunInstanceInput{Name,SubnetID string}`; `InstancePage{Items []model.Instance; NextPageToken string}`. `model.Instance` carries its `ENI model.ENI` snapshot and an internal observed `RuntimeID string` that is never desired state or returned by the API. Store exposes `Tx.InsertInstanceWithENI(ctx context.Context, instance model.Instance, eni model.ENI, now time.Time) error`, `Store.GetInstance(ctx context.Context, workspaceID,id string) (model.Instance,error)`, `Store.ListInstancePage(ctx context.Context, workspaceID,afterID string, limit int) ([]model.Instance,error)`, `Tx.MarkInstanceTerminating(ctx context.Context, workspaceID,id string, now time.Time) error`, and `Store.RecordRuntimeID(ctx context.Context, instanceID string, generation int64, runtimeID string) error`; exact SQL remains private to store. Pure `nextPrivateIP(prefix netip.Prefix, used map[netip.Addr]struct{}) (netip.Addr,error)` chooses the first free nonreserved address; the service maps exhaustion to `AddressLimitExceeded`.

**Named tests:** `TestIPAMReservedAndExhausted`,
`TestInstanceConcurrentAllocation`, `TestInstanceIdempotency`,
`TestInstancePagination`, `TestInstanceDependency`, and
`TestInstanceMigrationUpgrade`. Upgrade an actual schema-3 database containing
VPC/subnet data, rather than only testing a fresh schema-4 database. Persist
one stable, unique, locally administered unicast MAC per ENI as required by
the existing schema. Initial instance states are `pending`, `running`,
`shutting-down`, and `failed`; stop/start/reboot commands remain M2.

The first allocator test starts with this exact oracle, then fills `.4`
through `.14` and asserts exhaustion, not wraparound to a reserved address:

```go
func TestIPAMReservedAndExhausted(t *testing.T) {
    prefix := netip.MustParsePrefix("10.0.1.0/28")
    used := make(map[netip.Addr]struct{})
    for last := 4; last <= 14; last++ {
        got, err := nextPrivateIP(prefix, used)
        want := netip.AddrFrom4([4]byte{10, 0, 1, byte(last)})
        if err != nil || got != want { t.Fatalf("IP=%v error=%v want=%v", got, err, want) }
        used[got] = struct{}{}
    }
    if _, err := nextPrivateIP(prefix, used); err == nil { t.Fatal("expected exhaustion") }
}
```

- [ ] **Step 1: Write failing table-driven tests.** Assert `/28` first lease `.4`, last `.14`, twelfth run `AddressLimitExceeded` without partial rows/events; no network/broadcast/.1–.3 lease; concurrent unique runs receive unique IPs; 40 same-key calls yield one instance/ENI/index pair/create event and original response; same key with changed body conflicts; names obey slice-2 UTF-8/case rules; missing/deleting subnet errors; pagination is ID ordered; subnet deletion returns `DependencyViolation` while an instance exists.
- [ ] **Step 2: Run `go test ./internal/store ./internal/service -run 'TestInstance|TestIPAM' -count=1`.** Expected: FAIL on missing instance methods.
- [ ] **Step 3: Add migration and sqlc queries; implement pure IPv4 allocator and service.** Allocate both `i-` and `eni-` IDs and monotonic short indexes in the same DB transaction. Hash canonical name/subnet payload for 24-hour keyed replay, save the original response snapshot, write the ENI/IP lease and event before commit, enqueue only after commit. Add deletion intent and a nullable observed Podman container ID without changing the immutable first migration; preserve foreign-key enforcement on replacement SQLite connections. `RecordRuntimeID` must compare the desired generation so a late create cannot attach to a changed/deleting instance.
- [ ] **Step 4: Run `make generate`, `go test ./internal/store ./internal/service -count=1`, `make generate-check`, and `make test-race`.** Expected: PASS, including concurrency and reconnect tests.
- [ ] **Step 5: Commit with `git commit -s -m "feat(store): allocate instance ENIs and private IPs"`.**

### Task 3: Converge the ENI and enforce source-address checks

**Files:** Create `internal/network/netns/instance_linux.go`, `internal/network/topology/eni_linux.go`, `internal/network/topology/eni_policy_linux.go` and tests; modify `internal/network/topology/vpc_linux.go` only for shared router prerequisites.

**Interfaces:** `netns.OpenInstance(ctx context.Context, pid int, runtimeID string) (*netns.InstanceTarget,error)` validates runtime cgroup membership and namespace ownership, refuses the appliance/foreign target, and pins the network-namespace descriptor for that identity; `InstanceTarget.Close() error` releases it. `netns.WithInstanceHandle(ctx context.Context, target *netns.InstanceTarget, fn func(*netlink.Handle) error) error` enters the already-pinned target on a disposable locked thread and binds a netlink handle there. `topology.Engine.EnsureENI(ctx context.Context, vpc model.VPC, subnet model.Subnet, eni model.ENI, target *netns.InstanceTarget) error` creates/converges `ve<eni.ShortIndex>` and `eth0`; `DeleteENI(ctx context.Context, vpc model.VPC, eni model.ENI) error` removes only its named router end and policy. Move the peer using that pinned namespace descriptor, never by reopening a caller-supplied PID after validation. Neither method accepts an arbitrary interface name from the hook request.

**Named tests:** `TestInstanceTarget`, `TestWithInstanceHandle`, `TestENIConvergence`,
`TestENIForeignLink`, `TestENIIsolation`, and `TestENISourceCheck`.
Atomic source-check updates must preserve rules for other ENIs; two instance
hooks in the same VPC must not race a VPC topology sweep or each other when
updating the shared ruleset. Serialize shared VPC mutations and test concurrent
plumbing, plus rejection of a failure that would leave an ENI unprotected.

- [ ] **Step 1: Write failing pure and `integration` tests.** Derive valid <=15-byte `ve<index>` names and refuse foreign/oversized names. In a privileged appliance, assert `eth0` has lease address/subnet prefix and default route through `.1` with on-link gateway reachability; router peer has proxy ARP and `/32` route; reapply leaves one pair/route; deletion removes only that pair; two VPCs with same CIDR remain isolated; a forged source IP packet is dropped. Verify caller and host namespace inode/routes/nftables do not change.
- [ ] **Step 2: Run `go test ./internal/network/... -run 'TestENI|TestInstanceTarget|TestWithInstance' -count=1`.** Expected: FAIL on missing interface. Run `-tags integration` only in the privileged appliance and state the skip reason outside it.
- [ ] **Step 3: Implement the ENI engine from SP2/SP3 evidence without importing spike modules.** Keep PID/namespace identity validation and descriptor pinning inside `netns`; never switch in topology outside it. Refuse a pre-existing foreign-named/type link rather than overwrite it. In the VPC namespace add `ve<short>`, proxy ARP, `/32` route and per-ENI nftables source check; move peer using the pinned descriptor, rename `eth0`, set its persisted MAC/address/default route there. Re-run safely after partial failure; remove owned partial objects on rollback where possible.
- [ ] **Step 4: Run unit and privileged integration tests plus `make test-race`.** Expected: PASS; no host object changes and spoofed traffic fails.
- [ ] **Step 5: Commit with `git commit -s -m "feat(network): plumb isolated ENIs with source checks"`.**

### Task 4: Drive rootful Podman through its local REST socket

**Files:** Create `internal/compute/runtime.go`, `internal/compute/podman/{client,instance,exec}.go` and tests; modify `images/appliance/entrypoint.sh`, `images/appliance/Dockerfile` and appliance smoke tests.

**Interfaces:** `compute.Runtime` has `EnsureImage(ctx context.Context, ref string) error`, `Create(ctx context.Context, instance model.Instance) (compute.RuntimeID,error)`, `Start(ctx context.Context, id compute.RuntimeID) error`, `Delete(ctx context.Context, id compute.RuntimeID) error`, `Inspect(ctx context.Context, id compute.RuntimeID) (compute.Status,error)`, and `Exec(ctx context.Context, id compute.RuntimeID, req compute.ExecRequest) (compute.ExecSession,error)`. `compute.RuntimeID` is a string-backed observed Podman ID; `compute.Status` includes `Running bool`. `compute.ExecRequest{Command []string; TTY bool; Rows,Cols uint16}` selects the mode. `compute.ExecSession` exposes `Stdin() io.WriteCloser`, `Stdout() io.Reader`, `Stderr() io.Reader`, `Resize(ctx context.Context, rows,cols uint16) error`, `Wait(ctx context.Context) (int,error)`, and `Close() error`. In TTY mode, stdout is terminal output and stderr is empty; non-TTY mode preserves separate byte streams, and closing stdin must not close output. `podman.New(socketPath string) *podman.Client` dials Unix only. Container name equals instance ID and carries labels plus `io.nephos.instance-id` annotation. Define smaller consumer interfaces for hook/API tests rather than requiring every fake to implement unrelated runtime methods.

**Named tests:** `TestPodmanCreateContract`, `TestPodmanUnixOnly`,
`TestPodmanExecStreams`, `TestPodmanExecExit`, and privileged
`TestInstanceTaskLimit`. The latter reads `pids.max=512`, records baseline
tasks, and uses a bounded helper that holds children/threads until additional
creation is refused. It verifies existing tasks survive, releases/reaps all
children, and checks the daemon stays responsive. Use a deadline and cleanup;
never an unbounded recursive fork bomb. The aggregate limit must have enough
headroom for this one test-owned instance. Also inspect actual memory and CPU
cgroup values instead of trusting the create JSON alone.

- [ ] **Step 1: Write failing `httptest`-over-Unix adapter tests.** Inspect request methods/paths/bodies: fixed image `nephos-ubuntu:dev`, `systemd=always`, `userns=auto`, `network=none`, `NET_ADMIN`, 1 GiB memory, 2 vCPU quota, 512 pids, no devices, default seccomp and Nephos label/annotation. Assert connection cannot fall back to TCP; repeat create/start discovers the correctly labeled named container instead of duplicating it or adopting a foreign name collision; API errors retain response context. Exec tests cover TTY true/false, non-TTY stdout/stderr demultiplexing across fragmented reads, stdin EOF without output loss, resize, exit 7, cancellation and transport failure without a fabricated zero exit.
- [ ] **Step 2: Run `go test ./internal/compute/... -count=1`.** Expected: FAIL because adapter is absent.
- [ ] **Step 3: Implement only the libpod operations needed by slice 3.** Start `podman system service --time 0 unix:///run/podman/podman.sock` inside the appliance, wait for its private socket before starting `nephosd`, and terminate it on appliance exit. Keep image/container storage on `nephos-data`; do not publish or mount the socket to the host or instances. Record the actual image's Podman version and confirm exact REST fields and exec framing against that version with a privileged adapter smoke before relying on them. The current Dockerfile installs distribution packages, not an individually pinned Podman version; do not report a pin that does not exist.
- [ ] **Step 4: Run adapter unit tests, `make appliance`, and the privileged Podman adapter smoke.** Expected: PASS with real per-container limits and user namespace, without any host Docker object beyond the test-owned appliance.
- [ ] **Step 5: Commit with `git commit -s -m "feat(compute): add local Podman runtime adapter"`.**

### Task 5: Make createRuntime plumbing private and fail closed

**Files:** Create `internal/hook/{state,client,server_linux,server_unsupported}.go`, tests and `images/appliance/nephos-hook.json`; replace `cmd/nephos-hook/main.go`; modify `cmd/nephosd/main.go`, `images/appliance/Dockerfile`.

**Interfaces:** Hook reads a bounded OCI state object `{id,pid,bundle,annotations}` from stdin and rejects an absent/mismatched `io.nephos.instance-id` annotation. `hook.Request{InstanceID,ContainerID string; PID int}` is posted to `/plumb` over `/run/nephos/hook.sock` with a 10-second timeout. `hook.NewHandler(s *store.Store, network ENIPlumber) http.Handler` accepts only a request whose instance/ENI/subnet/VPC desired rows exist, whose instance is not terminating, whose `ContainerID` equals SQLite's generation-checked observed `RuntimeID`, and whose PID resolves through Task 3's `netns.OpenInstance` to that runtime's namespace. `ENIPlumber` exposes Task 3's exact `EnsureENI` signature with `*netns.InstanceTarget`, not an unchecked PID. The handler closes the target after plumbing. Hook endpoint is not exposed through the public API.

**Named tests:** `TestHookState`, `TestHookIdentity`, `TestHookPeer`, and
`TestHookFailureBeforePID1`. Root Unix peer credentials do not themselves
prove that a supplied PID belongs to the claimed container: validate its
runtime cgroup association and reject the appliance's own network namespace
or another instance's PID. Pin the namespace handle used during plumbing so
a disappearing/reused PID cannot redirect the operation. Validate these
checks against a real createRuntime invocation; do not inspect Podman from
inside its start hook. If runtime evidence cannot establish that association,
stop and report it rather than weaken the identity test.

- [ ] **Step 1: Write failing parser/handler tests.** Empty/invalid/oversized state, missing/mismatched annotation, zero/stale PID, wrong container ID, absent/deleting instance, missing ENI, failed Unix dial and network error must return nonzero without calling `EnsureENI`; valid state calls it exactly once. Verify a failed plumbing attempt prevents PID 1 startup in privileged integration and leaves no live partial veth.
- [ ] **Step 2: Run `go test ./internal/hook ./cmd/nephos-hook -count=1`.** Expected: FAIL on the stub.
- [ ] **Step 3: Implement bounded decoder, Unix peer/credential checks, handler and filtered hook JSON.** Accept only root peer credentials on a mode-0600 socket, compare the OCI state ID with the generation-checked Podman ID recorded in SQLite before `Start`, and re-read committed desired state before plumbing. Do not call Podman inspect from inside its start-time hook or rely on a process-local registry. Ensure daemon socket is listening before any instance start; reject all malformed input and propagate errors to hook stderr/status.
- [ ] **Step 4: Run unit tests and privileged hook integration, including forced failure.** Expected: PASS; `eth0` exists before PID 1 on success, no false running state on failure.
- [ ] **Step 5: Commit with `git commit -s -m "feat(hook): plumb ENIs before instance PID 1"`.**

### Task 6: Reconcile instance lifecycle after network prerequisites

**Files:** Create `internal/reconcile/instance.go`, `internal/reconcile/instance_test.go`, `internal/store/instance_reconcile.go` and tests; modify `cmd/nephosd/main.go`, `internal/service/network.go` only as required for dependency wakeups.

**Interfaces:** `reconcile.NewInstances(s *store.Store, network InstanceNetwork, runtime compute.Runtime, interval time.Duration) *InstanceController`; `InstanceController.Enqueue(id string)`, `Sweep(ctx context.Context) error`, `Run(ctx context.Context) error`. `InstanceNetwork` exposes `Ready(ctx context.Context, instance model.Instance) (bool,error)` for observed VPC/subnet readiness, plus Task 3's exact `DeleteENI` signature for termination. `Store.RecordInstance(ctx context.Context, snapshot model.Instance, status model.InstanceState, reason string, observed bool) error` rejects stale generations and appends a status event. Convert the model's observed ID to `compute.RuntimeID` only at the runtime boundary. Instance worker creates/starts only after its VPC/subnet are `available`; termination deletes the Podman object and owned ENI before releasing the SQLite lease/rows.

**Named tests:** `TestInstancePrerequisites`, `TestInstanceHookFailure`,
`TestInstanceGenerationRace`, `TestInstanceTermination`, and
`TestInstanceResync`. Generation changes, terminate during create/start, and
runtime failures retain the IP lease until owned-object teardown succeeds.
Durable instance status events must support the existing SSE resume path.

- [ ] **Step 1: Write failing fake-runtime/network tests.** Commit without enqueue then sweep; unavailable VPC never calls Podman; successful hook/start becomes `running` only after observed start; start/hook error yields `failed`, reason, unchanged observed generation and retry; two queued starts for one ID cannot overlap; terminate clears runtime/ENI before row/IP deletion, and a new run can reuse the freed IP; cancellation stops work. Include same-key concurrent create followed by one runtime container.
- [ ] **Step 2: Run `go test ./internal/reconcile ./internal/store -run 'TestInstance' -count=1`.** Expected: FAIL on missing controller/status adapter.
- [ ] **Step 3: Implement keyed, bounded queue and 60-second resync.** SQLite remains authority; runtime IDs are observations. After `Create` and before `Start`, persist the returned Podman ID with `RecordRuntimeID`; abort start if that write fails, so the hook has an identity to verify. Coordinate initial instance sweep after VPC sweep and before readiness, but do not claim slice-4 restart-marker or orphan-collection coverage. Keep retries bounded/backed off, preserve failed resources visibly, and never delete a foreign Podman container.
- [ ] **Step 4: Run `go test ./internal/reconcile ./cmd/nephosd -count=1`, `make test-race`, `make cross`.** Expected: PASS; tests explicitly prove no false running state.
- [ ] **Step 5: Commit with `git commit -s -m "feat(reconcile): converge instance lifecycle"`.**

### Task 7: Serve instance CRUD and authenticated console WebSocket

**Files:** Create `internal/apiserver/instance.go`, `internal/apiserver/console.go`, `internal/tunnel/console/protocol.go`, `api/console-v1.md` and tests; modify `internal/apiserver/server.go`, `api/openapi.yaml`, `api/contract_test.go`, `cmd/nephosd/main.go`; regenerate API files if console documentation changes.

**Interfaces:** Extend `apiserver.New` with `instances *service.Instances` and a narrow runtime console dependency; implement generated run/list/get/terminate methods. Console handshake uses the same bearer token and localhost/Host/Origin protections as REST, rejects every nonempty browser Origin in M1, and negotiates WebSocket subprotocol `nephos.console.v1`. Shared `internal/tunnel/console` types encode/decode Task 7's wire contract below; neither client nor server invents separate frame definitions.

**Console v1 wire contract:**

| Direction / frame | Meaning |
|---|---|
| Client first text frame | `{"type":"start","command":["/bin/bash"],"tty":true,"rows":24,"cols":80}` for interactive mode; command mode sends its exact argv with `tty:false` and no dimensions. Reject unknown fields, empty argv or NUL arguments; no implicit shell interpolation. |
| Client binary frame | One channel byte `0` followed by raw stdin bytes. |
| Server binary frame | Channel byte `1` plus stdout bytes; byte `2` plus stderr bytes only in non-TTY mode. PTY output uses channel `1`. |
| Client text control | `{"type":"stdin_eof"}` is non-TTY-only and closes exec stdin, not the session; PTY EOF is terminal input, not a stream half-close. `{"type":"resize","rows":40,"cols":120}` is valid only for TTY mode with nonzero uint16 dimensions. |
| Server final text frame | `{"type":"exit","exit_code":N}` after both output streams drain and runtime wait returns a status in 0–255, followed by normal closure. |
| Server error text frame | `{"type":"error","message":"..."}` for a runtime/protocol error; it never implies command success. |

Control frames are at most 4 KiB; data frames are at most 64 KiB including
the channel byte. Here a frame means a complete application message, not an
individual WebSocket fragment; enforce limits on assembled messages.
Require the start frame within 5 seconds of upgrade and
allow it exactly once; bounded single-reader/single-writer pumps preserve
per-stream byte order. Do not impose an arbitrary short idle timeout on a
healthy interactive shell. Bound pending writes with a 10-second deadline,
respect cancellation, and close/reap session I/O workers on disconnect.
Output streams have no total-order guarantee relative to each other.
Malformed/oversized/wrong-direction frames, duplicate start/exit, or closure
without a valid final exit frame are errors. A final exit cannot hide an
earlier stream/runtime error. Tokens stay in the Authorization header only.

**Named tests:** `TestInstanceAPI`, `TestInstanceEvents`,
`TestConsoleAuthentication`, `TestConsoleProtocol`,
`TestConsoleCommandStreams`, and `TestConsoleTerminalResize`. Use fake
sessions to prove exact bytes on distinct streams, stdin EOF followed by
trailing output, output-before-exit ordering, exit 7, malformed/oversized
frames, missing/duplicate start and missing exit, rejected non-TTY resize,
and cancellation with no stranded workers. Unauthorized/state-invalid
requests must create zero exec sessions; a valid upgrade still requires a
valid start frame before exec is created. Reject a TTY `stdin_eof` control
instead of silently closing the PTY output stream.

- [ ] **Step 1: Write failing `httptest` tests.** Assert 401 for absent/invalid token; 404 for unknown workspace or instance; 400 for extra JSON fields; 201 transitional run, 202 terminate, keyed replay/conflict, ID-ordered list page; console rejects wrong Origin/Host and non-running instance before exec creation; valid one-shot exec streams bytes and a nonzero exit code without leaking the token.
- [ ] **Step 2: Run `go test ./internal/apiserver -run 'TestInstance|TestConsole' -count=1`.** Expected: FAIL on 501 stubs/missing console.
- [ ] **Step 3: Implement strict JSON, error mapping and WebSocket bridge.** Pin a permissively licensed pure-Go WebSocket dependency; implement the two modes and exact limits above. No Podman socket is exposed to the host; only authenticated, instance-scoped exec is mediated by the daemon. Document the protocol in `api/console-v1.md` and link it from OpenAPI before generation. Keep command control argv out of logs and bearer tokens out of all payloads. Re-check running state/observed runtime identity when the start frame arrives, not just at upgrade.
- [ ] **Step 4: Run `go test ./api ./internal/apiserver -count=1`, `make generate-check`, `make test-race`, and `go run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...`.** Expected: PASS, with no called vulnerabilities.
- [ ] **Step 5: Commit with `git commit -s -m "feat(api): serve instances and console sessions"`.**

### Task 8: Add instance CLI and console command

**Files:** Create `cmd/nephos/instance.go`, `cmd/nephos/console.go` and tests; modify `cmd/nephos/main.go`, `cmd/nephos/network.go` only for shared presentation helpers.

**Interfaces:** `nephos instance run <name> --subnet <name-or-id> [--wait] [-o json]`, `list [--limit N --page-token TOKEN]`, `describe <name-or-id>`, `terminate <name-or-id> [--wait]`; `nephos console <name-or-id> [-- command ...]`. CLI resolves names by complete paginated lists, uses generated instance client methods, and reads the existing credential file. Wait polls GET until `running`, `failed`, deletion, or default two-minute timeout, with `--timeout` override.

**Named tests:** `TestInstanceCLI`, `TestInstanceWait`,
`TestConsoleCommandStreams`, `TestConsoleExitStatus`, and
`TestConsoleInteractive`. No-command mode requires a local terminal, launches
`/bin/bash` with a PTY, passes initial size/resize, and restores local terminal
settings on completion, interruption and error. Command mode sends exact
argv without a PTY, streams stdout/stderr to the corresponding CLI writers,
sends stdin EOF without closing output, and propagates remote exit 7 as 7.
Test exact byte output with redirected stdout, including embedded NUL bytes;
out-of-band labeling must never alter stdout. Invalid/truncated sessions
return nonzero even if their connection closes normally. No implicit
terminal detection may turn command mode into a PTY.

- [ ] **Step 1: Write failing CLI tests with `httptest.Server`.** Cover case-sensitive UTF-8 names, name/ID lookup across pages, exact JSON object output, stable key on retries, failed state reason, wait timeout/cancellation, unauthorized response without token disclosure, interactive console launching a shell and transferring bytes, one-shot exit 7 propagated as process status, and interruption closing the WebSocket.
- [ ] **Step 2: Run `go test ./cmd/nephos -run 'TestInstance|TestConsole' -count=1`.** Expected: FAIL because commands are absent.
- [ ] **Step 3: Implement commands without host-side Podman access.** Keep resource calls on `pkg/client`; for console use the authenticated WebSocket path and shared Task 7 framing. Label the console on stderr. Pin the pure-Go `golang.org/x/term` dependency at a version compatible with the project's Go toolchain and test terminal restore/resize with platform build tags as needed. Generate one idempotency key per run invocation and retain it across retries.
- [ ] **Step 4: Run `go test ./cmd/nephos -count=1`, `make cross`, and `make test-race`.** Expected: PASS.
- [ ] **Step 5: Commit with `git commit -s -m "feat(cli): run instances and open console sessions"`.**

### Task 9: Prove real cross-subnet packets and document the slice

**Files:** Create `tests/instance-ping-smoke.sh`; modify `.github/workflows/ci.yml`, `tests/ci_workflow_test.go`, `docs/tests/M1-connectivity-matrix.md`, `docs/ARCHITECTURE.md`, `docs/DEVELOPMENT.md`, `docs/RISKS.md`, `AGENTS.md`, `CHANGELOG.md`.

**Interfaces:** Smoke refuses pre-existing `nephos`/`nephos-data`, uses an isolated temporary HOME, owns and cleans only its labeled test container/volume, snapshots host namespace/routes/links/nftables, and retains appliance logs on failure. CI runs it on native Ubuntu after the existing slice-1/2 smokes.

**Named tests:** extend `TestNativeDockerWorkflowRunsVPCSubnetSmokeAfterBootstrap`
to assert that the instance smoke follows the VPC/subnet smoke; implement
`tests/instance-ping-smoke.sh` with named assertions for positive connectivity,
cross-VPC isolation, source spoofing, hook failure, task limits, console byte
separation/exit status, and teardown/host hygiene. Do not create a second
redundant workflow test.

**Isolation fixture:** VPC A and B both use `10.0.0.0/16`. A has `one` at
`10.0.1.4` and `two` at `10.0.2.4`. B has `overlap-source` at `10.0.1.4`
and `remote-only` at `10.0.1.5`, with no `.5` instance in A. First prove B's
source can ping `.5`; then prove A's `one` cannot reach `.5` and B receives
no corresponding request. The duplicate `.4` assignment proves independent
IP spaces, not isolation by itself. Add a bounded destination packet counter
or capture alongside ping status so a broken return path cannot conceal
cross-VPC delivery; keep the witness test-only, not a new public probe API.
Source-spoof tests likewise require a correct-source positive control,
anti-spoof drop evidence, and no forged packet at the destination.

- [ ] **Step 1: Add a failing CI wiring test, then a real-Docker smoke.** From locally built images, `nephos up`; create one VPC and two subnets, run `one` and `two` with `--wait`; assert different reserved-safe IPs and `eth0` before PID 1; `nephos console one -- ping -c 3 <two-ip>` succeeds. Exercise the overlapping-VPC fixture and source-spoof controls above; force hook failure and assert `failed` plus reason, not `running`. Run the bounded task-limit fixture and non-PTY exec checks (distinct stdout/stderr and exit 7), releasing its temporary workers before other scenarios. Terminate instances, delete subnets/VPCs, and check host network snapshot unchanged. Do not use an unimplemented reset as cleanup or weaken a failed packet assertion.
- [ ] **Step 2: Run `bash -n tests/instance-ping-smoke.sh` and `bash tests/instance-ping-smoke.sh`.** Expected: PASS on the actual privileged appliance, with only test-owned Docker objects removed.
- [ ] **Step 3: Update the connectivity matrix and docs.** Record the exact successful and blocked packet paths in ARCHITECTURE §5.5; distinguish present M1 source check from later SG/NACL behavior and mark restart/reset cases for slice 4. Update development commands, risks, changelog, AGENTS routing, and native CI. Keep roadmap M1 boxes unchecked until slice 4 passes.
- [ ] **Step 4: Run `make dev-ami`, `make appliance`, `make build`, `bash tests/instance-ping-smoke.sh`, `make ci`, `make generate-check`, `go mod tidy -diff`, `git diff --check`, and `git status --short --branch`.** Expected: PASS; state each skipped privileged/WSL check and why. Push the dedicated implementation branch and open a draft PR targeting `main` only after local verification and user authorization; wait for native CI and fix any real failure. Do not merge without direction.
- [ ] **Step 5: Commit with `git commit -s -m "test(m1): prove cross-subnet instance packets"` before pushing.**

## Self-review and handoff

Before implementation, compare every slice-3 claim to the approved spec and
the five Review Focus tests. The two console modes and 512-task ceiling are
accepted design details, not remaining user choices. The exact Podman
adapter, hook identity proof and stream handling still require real-runtime
verification; do not replace those checks with a silent trust shortcut. If
the built image cannot support the specified hook, user namespace, limits,
or exec stream through its local REST API, report the controlled result and
resolve a genuine architectural conflict under the ADR process.

The nine tasks implement only slice 3. Slice 4 still needs its own reviewed
execution plan for retained-root restart recovery, orphan collection, soft
and hard reset, leak checks, the replayable `docs/demos/M1.md`, a real
`make e2e` target, full native-Docker CI and WSL2 manual QA. The roadmap
acceptance checkboxes stay unchecked until that evidence exists.

**Execution handoff:** Native execution approved: one implementer in this
session, followed by independent whole-branch review. Use the ignored
plan-scoped ledger to resume after interruption; do not redo completed tasks.
