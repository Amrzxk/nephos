# M1 Slice 3: Instances, OCI Hook, Console, and Ping Implementation Plan

**Status:** Draft handoff, 2026-09-28. No slice-3 product code has been
implemented. The approved M1 design governs scope, but this detailed plan
still needs review before execution. In particular, the proposed 512-process
limit and single-PTY console framing have not been approved.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Run two real Ubuntu system containers in different subnets of one VPC, manage them through the Nephos API/CLI, reach one from the other by ICMP, and use an authenticated out-of-band console.

**Architecture:** SQLite owns instance and ENI desired state, including the private-IP lease; a separate instance reconciler waits for converged VPC topology and drives a thin client of Podman's rootful Unix-socket REST API. A filtered OCI `createRuntime` hook asks `nephosd` over a private Unix socket to move and configure the ENI before PID 1 starts. The API and CLI expose only the fixed M1 instance shape and an authenticated WebSocket exec path; slice 4 owns full restart recovery and reset.

**Tech Stack:** Go 1.27.1 with `CGO_ENABLED=0`; existing OpenAPI 3.1/oapi-codegen v2.8.0, sqlc v1.31.1, `modernc.org/sqlite` v1.59.0, netlink v1.3.1, netns v0.0.5; Linux network namespaces and nftables; appliance-local Podman libpod REST over `/run/podman/podman.sock`; locally built `nephos-ubuntu:dev` image; `github.com/gorilla/websocket` v1.5.3 for authenticated console streaming (BSD-2-Clause, pure Go).

**Spec:** `docs/superpowers/specs/2026-09-23-m1-two-instances-ping-design.md`

**Base:** Start implementation from `main` after merged PRs #3 and #4
(slice-2 merge commit `def23f11ebe96b27328917369ed61f6f8be2b746`), including
this documentation handoff. Use a dedicated `codex/` branch and open a draft
PR against `main`; the earlier stacked branches no longer need to be bases.

## Global Constraints

- One implicit `default` workspace and explicit VPCs only. API paths retain `/v1/workspaces/default/...`; reject unknown workspaces and unknown create fields. No `nx-edge`, default VPC, SG/NACL, SSH/key pairs, DNS, IMDS, user data, AMI or instance-type choice in M1.
- Follow accepted ADR-0003, ADR-0004, ADR-0005, ADR-0007, and ADR-0008. If a validated platform assumption fails, stop and propose a superseding ADR; never mark an unenforced behavior successful.
- Resource names remain case-sensitive trimmed UTF-8, 1–255 code points, without controls. IDs use `i-`/`eni-` plus 17 lowercase hexadecimal characters. One fixed `t3.micro` means 2 vCPU and 1 GiB memory; use a bounded pids limit (512, matching SP1), default seccomp, `userns=auto`, systemd mode, and default capabilities plus `NET_ADMIN`.
- IPAM reserves network, base+1, base+2, base+3, and broadcast in each subnet; assign from base+4 upward. Keep an IP stable while its instance exists. SQLite transaction plus `UNIQUE(subnet_id, private_ip)` prevents duplicate concurrent leases.
- `api/openapi.yaml` precedes generated server/client edits; `internal/store/queries.sql` precedes sqlc generation. Never hand-edit generated files. No cgo, AGPL, TCP Podman service, host bind mount, host network/PID namespace mutation, or product state outside `nephos-data`.
- Only `internal/network/netns` switches network namespaces; a switched thread never returns to the Go scheduler. Linux names derive from database short indexes and fit 15 bytes. Instances are Podman containers with an isolated, Nephos-wired network namespace, never a Podman subnet network.
- Hook applies only to containers annotated `io.nephos.instance-id` at `createRuntime`. The hook socket is appliance-private at `/run/nephos/hook.sock`. If validation or plumbing fails, hook exit is nonzero, instance cannot become `running`, and the error is visible in `state_reason`.
- Instance create is transactional with its ENI, IP, idempotency replay snapshot, and event; termination is asynchronous and blocks subnet deletion until cleanup. `running` means Podman started after successful hook plumbing, not sshd/cloud-init readiness. Console exec is explicitly out of band, while any packet emitted by its command obeys VPC routing.
- Slice 3 tests real packet behavior and isolated failure paths. Slice 4 owns retained-root restart marker, orphan cleanup/leak checker, reset levels, full M1 e2e closure and WSL2 manual QA. Do not check M1 roadmap acceptance boxes yet.

## Review Focus

- A `/28` subnet has only eleven allocatable addresses (base+4 through base+14): the next create must return `AddressLimitExceeded` without a partial instance, ENI, index, or event (Task 2).
- Two concurrent launches, including same-key retries, must not share an IP or router interface or produce two containers for one instance (Tasks 2, 4, 6).
- A forged, malformed, stale, or unauthorized hook request must not move a foreign interface or start an instance; a failed hook must leave `observed_generation` behind desired generation (Tasks 5 and 6).
- Same-CIDR VPCs must remain disconnected even if each contains an instance at the same private IP; spoofing another source IP must be dropped, not merely documented (Tasks 3 and 9).
- A browser-origin WebSocket attempt, absent/invalid bearer token, or console against a non-running instance must fail without opening Podman exec; one-shot console must propagate the command's nonzero exit status (Tasks 7 and 8).

---

## File map

- `api/openapi.yaml`, `api/contract_test.go`: instance CRUD schema and console upgrade contract. `internal/apiserver/generated/*` and `pkg/client/*` remain generated.
- `internal/model/instance.go`: immutable instance/ENI snapshots and lifecycle states shared below the API.
- `internal/store/migrations/0004_instance_deletion.sql`, `internal/store/queries.sql`, `internal/store/instances.go`, `internal/store/instance_reconcile.go`: additive deletion intent, observed Podman container ID, IP/ENI persistence, transactional status/event changes. Keep SQL confined to the store.
- `internal/service/instance.go`, `internal/service/ipam.go`: validation, allocation, idempotency, pagination, dependency and terminate decisions. No Podman or kernel calls.
- `internal/network/netns/instance_linux.go`, `internal/network/topology/eni_linux.go`, `internal/network/topology/eni_policy_linux.go`: namespace entry, veth/route/proxy-ARP convergence and source anti-spoof. No service/API imports.
- `internal/compute/runtime.go`, `internal/compute/podman/{client,instance,exec}.go`: small runtime interface and local REST adapter; no CLI invocation or durable runtime registry.
- `internal/reconcile/instance.go`: keyed instance worker, prerequisites, status and cleanup; existing VPC controller remains authoritative for VPCs/subnets.
- `internal/hook/{state,client,server}.go`, `cmd/nephos-hook/main.go`, `images/appliance/nephos-hook.json`: filtered, fail-closed createRuntime request and private daemon endpoint.
- `internal/apiserver/{instance,console,server}.go`, `cmd/nephosd/main.go`: generated HTTP methods, authenticated WebSocket and daemon wiring/readiness.
- `cmd/nephos/{instance,console}.go`, `cmd/nephos/main.go`: generated-client instance commands and streaming console; `tests/instance-ping-smoke.sh` validates the real appliance path.
- `docs/tests/M1-connectivity-matrix.md`, `docs/ARCHITECTURE.md`, `docs/DEVELOPMENT.md`, `docs/RISKS.md`, `CHANGELOG.md`, `AGENTS.md`, `.github/workflows/ci.yml`: proof and contributor routing for this slice.

### Task 1: Specify instance CRUD before generating handlers

**Files:** Modify `api/openapi.yaml`, `api/contract_test.go`, `internal/apiserver/server.go`; regenerate `internal/apiserver/generated/server.gen.go`, `pkg/client/client.gen.go`.

**Interfaces:** Operation IDs `runInstance`, `listInstances`, `getInstance`, `terminateInstance`; `RunInstanceRequest{name,subnet_id}` only; `Instance{id,name,subnet_id,private_ip,eni_id,instance_type,state,state_reason,generation,observed_generation}`; `InstancesPage{items,next_page_token}`. Console path `/v1/workspaces/{workspace}/instances/{id}/console` requires bearer auth and a WebSocket upgrade, documented in OpenAPI as HTTP 101 with a prose wire-protocol reference added in Task 7.

- [ ] **Step 1: Write a failing contract test.** Assert all four method/path pairs, exact request fields with `additionalProperties:false`, required response fields, `Idempotency-Key`, bearer security, 201/202 success, 400/401/404/409 errors, `limit`/`page_token`, and the authenticated console path.
- [ ] **Step 2: Run `go test ./api -run TestInstanceContract -count=1`.** Expected: FAIL because instance paths are absent.
- [ ] **Step 3: Add the OpenAPI contract and run `make generate`.** Add explicit 501 JSON stubs for new generated methods so the intermediate commit compiles. Do not add selectable AMI, type, tags, or filters.
- [ ] **Step 4: Run `go test ./api ./internal/apiserver -count=1` and `make generate-check`.** Expected: PASS; unimplemented routes still clearly return 501.
- [ ] **Step 5: Commit with `git commit -s -m "feat(api): define M1 instance contract"`.**

### Task 2: Persist instances, ENIs, and collision-free leases

**Files:** Create `internal/model/instance.go`, `internal/store/migrations/0004_instance_deletion.sql`, `internal/store/instances.go`, `internal/service/ipam.go`, `internal/service/instance.go` and corresponding tests; modify `internal/store/queries.sql`, `internal/store/resources.go`, `internal/store/store_test.go`, `internal/service/errors.go`, `internal/service/network.go`; regenerate `internal/store/sqlc/*`.

**Interfaces:** `service.NewInstances(s *store.Store, enqueue func(string), now func() time.Time) *service.Instances`; `Run(ctx context.Context, input RunInstanceInput, key string) (model.Instance, error)`, `Get(ctx,id)`, `List(ctx,limit,token)`, `Terminate(ctx,id)`. `RunInstanceInput{Name,SubnetID string}`. `model.Instance` carries its `model.ENI` snapshot and an internal observed `RuntimeID` that is never desired state or returned by the API. Store exposes `Tx.InsertInstanceWithENI(ctx, instance, eni, now)`, `Store.GetInstance(ctx, workspaceID,id)`, `Store.ListInstancePage(ctx, workspaceID,afterID,limit)`, `Tx.MarkInstanceTerminating(ctx,workspaceID,id,now)`, and `Store.RecordRuntimeID(ctx, instanceID, generation, runtimeID)`; exact SQL remains private to store.

- [ ] **Step 1: Write failing table-driven tests.** Assert `/28` first lease `.4`, last `.14`, twelfth run `AddressLimitExceeded` without partial rows/events; no network/broadcast/.1–.3 lease; concurrent unique runs receive unique IPs; 40 same-key calls yield one instance/ENI/index pair/create event and original response; same key with changed body conflicts; names obey slice-2 UTF-8/case rules; missing/deleting subnet errors; pagination is ID ordered; subnet deletion returns `DependencyViolation` while an instance exists.
- [ ] **Step 2: Run `go test ./internal/store ./internal/service -run 'TestInstance|TestIPAM' -count=1`.** Expected: FAIL on missing instance methods.
- [ ] **Step 3: Add migration and sqlc queries; implement pure IPv4 allocator and service.** Allocate both `i-` and `eni-` IDs and monotonic short indexes in the same DB transaction. Hash canonical name/subnet payload for 24-hour keyed replay, save the original response snapshot, write the ENI/IP lease and event before commit, enqueue only after commit. Add deletion intent and a nullable observed Podman container ID without changing the immutable first migration; preserve foreign-key enforcement on replacement SQLite connections. `RecordRuntimeID` must compare the desired generation so a late create cannot attach to a changed/deleting instance.
- [ ] **Step 4: Run `make generate`, `go test ./internal/store ./internal/service -count=1`, `make generate-check`, and `make test-race`.** Expected: PASS, including concurrency and reconnect tests.
- [ ] **Step 5: Commit with `git commit -s -m "feat(store): allocate instance ENIs and private IPs"`.**

### Task 3: Converge the ENI and enforce source-address checks

**Files:** Create `internal/network/netns/instance_linux.go`, `internal/network/topology/eni_linux.go`, `internal/network/topology/eni_policy_linux.go` and tests; modify `internal/network/topology/vpc_linux.go` only for shared router prerequisites.

**Interfaces:** `netns.WithPIDHandle(ctx context.Context, pid int, fn func(*netlink.Handle) error) error` validates an appliance-local target PID/network-namespace handle, enters it on a disposable locked thread and binds a netlink handle there. `topology.Engine.EnsureENI(ctx context.Context, vpc model.VPC, subnet model.Subnet, eni model.ENI, pid int) error` creates/converges `ve<eni.ShortIndex>` and `eth0`; `DeleteENI(ctx context.Context, vpc model.VPC, eni model.ENI) error` removes only its named router end and policy. Neither method accepts an arbitrary interface name from the hook request.

- [ ] **Step 1: Write failing pure and `integration` tests.** Derive valid <=15-byte `ve<index>` names and refuse foreign/oversized names. In a privileged appliance, assert `eth0` has lease address/subnet prefix and default route through `.1` with on-link gateway reachability; router peer has proxy ARP and `/32` route; reapply leaves one pair/route; deletion removes only that pair; two VPCs with same CIDR remain isolated; a forged source IP packet is dropped. Verify caller and host namespace inode/routes/nftables do not change.
- [ ] **Step 2: Run `go test ./internal/network/... -run 'TestENI|TestWithPID' -count=1`.** Expected: FAIL on missing interface. Run `-tags integration` only in the privileged appliance and state the skip reason outside it.
- [ ] **Step 3: Implement the ENI engine from SP2/SP3 evidence without importing spike modules.** Use `/proc/<pid>/ns/net` only after PID validation; never switch in topology outside `netns`. Refuse a pre-existing foreign-named/type link rather than overwrite it. In the VPC namespace add `ve<short>`, proxy ARP, `/32` route and per-ENI nftables source check; move peer into instance namespace, rename `eth0`, set address/default route there. Re-run safely after partial failure; remove owned partial objects on rollback where possible.
- [ ] **Step 4: Run unit and privileged integration tests plus `make test-race`.** Expected: PASS; no host object changes and spoofed traffic fails.
- [ ] **Step 5: Commit with `git commit -s -m "feat(network): plumb isolated ENIs with source checks"`.**

### Task 4: Drive rootful Podman through its local REST socket

**Files:** Create `internal/compute/runtime.go`, `internal/compute/podman/{client,instance,exec}.go` and tests; modify `images/appliance/entrypoint.sh`, `images/appliance/Dockerfile` and appliance smoke tests.

**Interfaces:** `compute.Runtime` has `EnsureImage(ctx,ref)`, `Create(ctx,model.Instance) (compute.RuntimeID,error)`, `Start(ctx,id) error`, `Delete(ctx,id) error`, `Inspect(ctx,id) (compute.Status,error)`, and `Exec(ctx,id,compute.ExecRequest) (compute.ExecSession,error)`; `compute.ExecSession` carries bidirectional streams, terminal resize where supported, and `Wait(ctx) (int,error)`. `podman.New(socketPath string) *podman.Client` dials Unix only. Container name equals instance ID and carries labels plus `io.nephos.instance-id` annotation.

- [ ] **Step 1: Write failing `httptest`-over-Unix adapter tests.** Inspect request methods/paths/bodies: fixed image `nephos-ubuntu:dev`, `systemd=always`, `userns=auto`, `network=none`, `NET_ADMIN`, 1 GiB memory, 2 vCPU quota, 512 pids, no devices, default seccomp and Nephos label/annotation. Assert connection cannot fall back to TCP; repeat create/start discovers the named container instead of duplicating it; API errors retain response context.
- [ ] **Step 2: Run `go test ./internal/compute/... -count=1`.** Expected: FAIL because adapter is absent.
- [ ] **Step 3: Implement only the libpod operations needed by slice 3.** Start `podman system service --time 0 unix:///run/podman/podman.sock` inside the appliance, wait for its private socket before starting `nephosd`, and terminate it on appliance exit. Keep image/container storage on `nephos-data`; do not publish or mount the socket to the host or instances. Confirm exact REST fields against the image's pinned Podman version with a privileged adapter smoke before relying on them.
- [ ] **Step 4: Run adapter unit tests, `make appliance`, and the privileged Podman adapter smoke.** Expected: PASS with real per-container limits and user namespace, without any host Docker object beyond the test-owned appliance.
- [ ] **Step 5: Commit with `git commit -s -m "feat(compute): add local Podman runtime adapter"`.**

### Task 5: Make createRuntime plumbing private and fail closed

**Files:** Create `internal/hook/{state,client,server}.go`, tests and `images/appliance/nephos-hook.json`; replace `cmd/nephos-hook/main.go`; modify `cmd/nephosd/main.go`, `images/appliance/Dockerfile`.

**Interfaces:** Hook reads a bounded OCI state object `{id,pid,bundle,annotations}` from stdin and rejects an absent/mismatched `io.nephos.instance-id` annotation. `hook.Request{InstanceID,ContainerID string; PID int}` is posted to `/plumb` over `/run/nephos/hook.sock` with a 10-second timeout. `hook.NewHandler(s *store.Store, network ENIPlumber) http.Handler` accepts only a request whose instance/ENI/subnet/VPC desired rows exist, whose instance is not terminating, whose `ContainerID` equals SQLite's generation-checked observed `RuntimeID`, and whose PID names a live local namespace. Hook endpoint is not exposed through the public API.

- [ ] **Step 1: Write failing parser/handler tests.** Empty/invalid/oversized state, missing/mismatched annotation, zero/stale PID, wrong container ID, absent/deleting instance, missing ENI, failed Unix dial and network error must return nonzero without calling `EnsureENI`; valid state calls it exactly once. Verify a failed plumbing attempt prevents PID 1 startup in privileged integration and leaves no live partial veth.
- [ ] **Step 2: Run `go test ./internal/hook ./cmd/nephos-hook -count=1`.** Expected: FAIL on the stub.
- [ ] **Step 3: Implement bounded decoder, Unix peer/credential checks, handler and filtered hook JSON.** Accept only root peer credentials on a mode-0600 socket, compare the OCI state ID with the generation-checked Podman ID recorded in SQLite before `Start`, and re-read committed desired state before plumbing. Do not call Podman inspect from inside its start-time hook or rely on a process-local registry. Ensure daemon socket is listening before any instance start; reject all malformed input and propagate errors to hook stderr/status.
- [ ] **Step 4: Run unit tests and privileged hook integration, including forced failure.** Expected: PASS; `eth0` exists before PID 1 on success, no false running state on failure.
- [ ] **Step 5: Commit with `git commit -s -m "feat(hook): plumb ENIs before instance PID 1"`.**

### Task 6: Reconcile instance lifecycle after network prerequisites

**Files:** Create `internal/reconcile/instance.go`, `internal/reconcile/instance_test.go`, `internal/store/instance_reconcile.go` and tests; modify `cmd/nephosd/main.go`, `internal/service/network.go` only as required for dependency wakeups.

**Interfaces:** `reconcile.NewInstances(s *store.Store, network NetworkReadiness, runtime compute.Runtime, interval time.Duration) *InstanceController`; `InstanceController.Enqueue(id string)`, `Sweep(ctx) error`, `Run(ctx) error`. `Store.RecordInstance(ctx, snapshot model.Instance, status model.InstanceState, reason string, observed bool) error` rejects stale generations and appends a status event. Instance worker creates/starts only after its VPC/subnet are `available`; termination deletes the Podman object and owned ENI before releasing the SQLite lease/rows.

- [ ] **Step 1: Write failing fake-runtime/network tests.** Commit without enqueue then sweep; unavailable VPC never calls Podman; successful hook/start becomes `running` only after observed start; start/hook error yields `failed`, reason, unchanged observed generation and retry; two queued starts for one ID cannot overlap; terminate clears runtime/ENI before row/IP deletion, and a new run can reuse the freed IP; cancellation stops work. Include same-key concurrent create followed by one runtime container.
- [ ] **Step 2: Run `go test ./internal/reconcile ./internal/store -run 'TestInstance' -count=1`.** Expected: FAIL on missing controller/status adapter.
- [ ] **Step 3: Implement keyed, bounded queue and 60-second resync.** SQLite remains authority; runtime IDs are observations. After `Create` and before `Start`, persist the returned Podman ID with `RecordRuntimeID`; abort start if that write fails, so the hook has an identity to verify. Coordinate initial instance sweep after VPC sweep and before readiness, but do not claim slice-4 restart-marker or orphan-collection coverage. Keep retries bounded/backed off, preserve failed resources visibly, and never delete a foreign Podman container.
- [ ] **Step 4: Run `go test ./internal/reconcile ./cmd/nephosd -count=1`, `make test-race`, `make cross`.** Expected: PASS; tests explicitly prove no false running state.
- [ ] **Step 5: Commit with `git commit -s -m "feat(reconcile): converge instance lifecycle"`.**

### Task 7: Serve instance CRUD and authenticated console WebSocket

**Files:** Create `internal/apiserver/instance.go`, `internal/apiserver/console.go` and tests; modify `internal/apiserver/server.go`, `api/openapi.yaml`, `api/contract_test.go`, `cmd/nephosd/main.go`; regenerate API files if console documentation changes.

**Interfaces:** Extend `apiserver.New` with `instances *service.Instances` and a narrow `compute.Runtime` console dependency; implement generated run/list/get/terminate methods. Console handshake uses the same bearer token and localhost/Host/Origin protections as REST. The v1 console protocol uses one PTY: client binary frames are stdin, server binary frames are combined stdout/stderr, and one final server text frame `{"type":"exit","exit_code":N}` precedes normal closure. Close/error without that frame is a CLI failure; the API and CLI agree on this framing.

- [ ] **Step 1: Write failing `httptest` tests.** Assert 401 for absent/invalid token; 404 for unknown workspace or instance; 400 for extra JSON fields; 201 transitional run, 202 terminate, keyed replay/conflict, ID-ordered list page; console rejects wrong Origin/Host and non-running instance before exec creation; valid one-shot exec streams bytes and a nonzero exit code without leaking the token.
- [ ] **Step 2: Run `go test ./internal/apiserver -run 'TestInstance|TestConsole' -count=1`.** Expected: FAIL on 501 stubs/missing console.
- [ ] **Step 3: Implement strict JSON, error mapping and WebSocket bridge.** Pin a permissively licensed pure-Go WebSocket dependency; give both server and client bounded frame sizes, deadlines and cancellation. No Podman socket or privileged command is exposed to host; the daemon mediates exec against the exact running instance. Document the chosen protocol in OpenAPI descriptions or adjacent `api/` docs before generating the client.
- [ ] **Step 4: Run `go test ./api ./internal/apiserver -count=1`, `make generate-check`, `make test-race`, and `go run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...`.** Expected: PASS, with no called vulnerabilities.
- [ ] **Step 5: Commit with `git commit -s -m "feat(api): serve instances and console sessions"`.**

### Task 8: Add instance CLI and console command

**Files:** Create `cmd/nephos/instance.go`, `cmd/nephos/console.go` and tests; modify `cmd/nephos/main.go`, `cmd/nephos/network.go` only for shared presentation helpers.

**Interfaces:** `nephos instance run <name> --subnet <name-or-id> [--wait] [-o json]`, `list [--limit N --page-token TOKEN]`, `describe <name-or-id>`, `terminate <name-or-id> [--wait]`; `nephos console <name-or-id> [-- command ...]`. CLI resolves names by complete paginated lists, uses generated instance client methods, and reads the existing credential file. Wait polls GET until `running`, `failed`, deletion, or default two-minute timeout, with `--timeout` override.

- [ ] **Step 1: Write failing CLI tests with `httptest.Server`.** Cover case-sensitive UTF-8 names, name/ID lookup across pages, exact JSON object output, stable key on retries, failed state reason, wait timeout/cancellation, unauthorized response without token disclosure, interactive console launching a shell and transferring bytes, one-shot exit 7 propagated as process status, and interruption closing the WebSocket.
- [ ] **Step 2: Run `go test ./cmd/nephos -run 'TestInstance|TestConsole' -count=1`.** Expected: FAIL because commands are absent.
- [ ] **Step 3: Implement commands without host-side Podman access.** Keep resource calls on `pkg/client`; for console use the authenticated WebSocket path and Task 7's documented framing. Label console output as out of band. Generate one idempotency key per run invocation and retain it across retries.
- [ ] **Step 4: Run `go test ./cmd/nephos -count=1`, `make cross`, and `make test-race`.** Expected: PASS.
- [ ] **Step 5: Commit with `git commit -s -m "feat(cli): run instances and open console sessions"`.**

### Task 9: Prove real cross-subnet packets and document the slice

**Files:** Create `tests/instance-ping-smoke.sh`; modify `.github/workflows/ci.yml`, `tests/ci_workflow_test.go`, `docs/tests/M1-connectivity-matrix.md`, `docs/ARCHITECTURE.md`, `docs/DEVELOPMENT.md`, `docs/RISKS.md`, `AGENTS.md`, `CHANGELOG.md`.

**Interfaces:** Smoke refuses pre-existing `nephos`/`nephos-data`, uses an isolated temporary HOME, owns and cleans only its labeled test container/volume, snapshots host namespace/routes/links/nftables, and retains appliance logs on failure. CI runs it on native Ubuntu after the existing slice-1/2 smokes.

- [ ] **Step 1: Add a failing CI wiring test, then a real-Docker smoke.** From locally built images, `nephos up`; create one VPC and two subnets, run `one` and `two` with `--wait`; assert different reserved-safe IPs and `eth0` before PID 1; `nephos console one -- ping -c 3 <two-ip>` succeeds; create overlapping second VPC/instance and assert same-IP cross-VPC ping fails; attempt source spoof and assert drop; force hook failure and assert `failed` plus reason, not `running`; terminate instances, delete subnets/VPCs, and check host network snapshot unchanged. Do not use an unimplemented reset as cleanup or weaken a failed packet assertion.
- [ ] **Step 2: Run `bash -n tests/instance-ping-smoke.sh` and `bash tests/instance-ping-smoke.sh`.** Expected: PASS on the actual privileged appliance, with only test-owned Docker objects removed.
- [ ] **Step 3: Update the connectivity matrix and docs.** Record the exact successful and blocked packet paths in ARCHITECTURE §5.5; distinguish present M1 source check from later SG/NACL behavior and mark restart/reset cases for slice 4. Update development commands, risks, changelog, AGENTS routing, and native CI. Keep roadmap M1 boxes unchecked until slice 4 passes.
- [ ] **Step 4: Run `make dev-ami`, `make appliance`, `make build`, `bash tests/instance-ping-smoke.sh`, `make ci`, `make generate-check`, `go mod tidy -diff`, `git diff --check`, and `git status --short --branch`.** Expected: PASS; state each skipped privileged/WSL check and why. Push the dedicated implementation branch and open a draft PR targeting `main` only after local verification and user authorization; wait for native CI and fix any real failure. Do not merge without direction.
- [ ] **Step 5: Commit with `git commit -s -m "test(m1): prove cross-subnet instance packets"` before pushing.**

## Self-review and handoff

Before implementation, compare every slice-3 claim to the approved spec and the five Review Focus tests. In particular, review the chosen PTY framing and SQLite-backed Podman identity proof; neither may be improvised into a silent trust shortcut. If the pinned Podman image cannot support the specified hook, user namespace, limits, or exec stream through its local REST API, report the exact controlled result and resolve it under the ADR process. Slice 4 still owns full instance restart recovery, reset, leak checks, and M1 milestone closure.
