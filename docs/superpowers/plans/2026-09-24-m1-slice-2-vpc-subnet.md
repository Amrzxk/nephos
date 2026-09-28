# M1 Slice 2: VPC and Subnet Resource Path Implementation Plan

**Status:** Implemented and merged in [PR #4](https://github.com/Amrzxk/nephos/pull/4)
on 2026-09-28, including the SQLite reconnect constraint fix. This is the
historical implementation plan, not an open task list to execute again.
Current progress and remaining work are recorded in
[ROADMAP](../../ROADMAP.md#m1-thinnest-end-to-end-slice-two-instances-ping).

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A contributor creates, lists, describes, waits for, and deletes VPCs and subnets through the Nephos CLI/API; SQLite remains authoritative while a reconciler creates real, isolated VPC router namespaces and subnet gateways inside the appliance.

**Architecture:** The spec-first API and generated client lead to a service that validates requests and commits desired state plus durable events. A level-triggered VPC reconciler reads SQLite, applies idempotent namespace topology through a Linux-only engine, and records observed generations. The daemon completes an initial sweep before reporting ready; a 60-second resync repairs missed enqueue calls and drift. No instance ENIs or packet-policy claims belong to this slice.

**Tech Stack:** Go 1.27.1 and `CGO_ENABLED=0`, OpenAPI 3.1 and existing oapi-codegen v2.8.0, `modernc.org/sqlite` v1.59.0, sqlc v1.31.1, `github.com/vishvananda/netlink` v1.3.1, `github.com/vishvananda/netns` v0.0.5, Linux network namespaces, Docker and Podman appliance.

**Spec:** `docs/superpowers/specs/2026-09-23-m1-two-instances-ping-design.md`

**Historical base:** This slice was stacked on `codex/m1-implementation` at `412fe1303d956d71e94a17496e783e2bef6180f3`. PRs #3 and #4 are now merged; the original execution instructions below are retained as history.

## Global Constraints

- M1 has exactly one implicit `default` workspace; unknown workspace paths fail. VPCs are explicit; no default VPC or `nx-edge`.
- Follow accepted ADR-0005, ADR-0007, and ADR-0008 without changing their architecture. A genuine contrary platform result requires a superseding ADR, not a weakened test.
- `api/openapi.yaml` is edited before generated code. Use sqlc-generated queries for normal CRUD; never hand-edit generated files. Pin every new module and generator version.
- The SQLite file, WAL files, and any persistent secret live under `/var/lib/nephos` on `nephos-data`. No product host mounts, host namespace/firewall changes, cgo, or second durable state source.
- IDs have the ADR-0008 prefix and 17 lowercase hex characters. Resource names are case-sensitive, trimmed UTF-8 of 1–255 Unicode code points without control characters, including spaces; this was explicitly chosen by the user on 2026-09-24.
- IPv4 VPC and subnet CIDRs are /16–/28; canonicalize host bits as AWS does. Subnets fit in their VPC and cannot overlap siblings; different VPCs may overlap. AZ is one of `local-1a`, `local-1b`, `local-1c`.
- Creates return a transitional object; replay of one `Idempotency-Key` and identical canonical payload within 24 hours returns the original object; a different payload conflicts. Transactionally write desired row, idempotency row, and event.
- Lists use deterministic ID-order keyset pagination (`limit` default 50, max 100), with opaque, resource-specific page tokens; no arbitrary tags or broader list filters before M3.
- All resource and event routes require bearer auth. Unknown JSON fields and unknown workspaces fail explicitly. `--wait` polls GET until `available`, `failed`, or timeout (two minutes by default), never reports success on a failed resource.
- Deletion is asynchronous after dependency validation. Failed reconciliation sets `failed` with an actionable `state_reason` and retries with backoff. Startup and 60-second resync recover committed-but-not-enqueued work.
- Only `internal/network/netns` enters namespaces. Switched OS threads are never returned to Go's scheduler; all netlink handles are bound to namespace handles. Never delete a foreign namespace or a live Nephos namespace merely to recreate it.
- Keep the API/CLI/store/network layering in ARCHITECTURE §13. Update network fidelity documentation, the connectivity matrix, tests, and changelog in the same slice.

## Review Focus

- Crash between SQLite commit and in-memory enqueue: the initial or periodic sweep must still converge the resource; Task 6 tests this without enqueue.
- Two VPCs with the same CIDR: both must get distinct namespace names and never share gateway addresses or links; Tasks 5 and 9 test this.
- Concurrent/repeated creates with one idempotency key: one row, one ID, one event, and no double-allocated kernel index; Task 4 tests this.
- Namespace or gateway operation failure: resource becomes `failed`, retains desired state and reason, and can recover on retry without a false observed generation; Task 6 tests this.
- Delete versus dependent-create race: one SQLite transaction must decide dependency validity; a VPC cannot disappear while a subnet commits against it; Task 4 tests this.

---

## File map

- `api/openapi.yaml`: canonical VPC/subnet/event schemas and operations. `internal/apiserver/generated/server.gen.go` and `pkg/client/client.gen.go` are generated only.
- `internal/model/network.go`: shared VPC/subnet resource snapshots and states; no I/O.
- `internal/store/migrations/0001_initial.sql`, `internal/store/queries.sql`, `sqlc.yaml`, `internal/store/sqlc/*`: versioned schema and generated CRUD queries. `internal/store/store.go`, `resources.go`, `events.go`: opening, transactions, idempotency, and persistence adapters.
- `internal/service/validate.go`, `ids.go`, `network.go`, `errors.go`: pure input rules and VPC/subnet service orchestration; no kernel calls.
- `internal/network/netns/netns_linux.go` and tests: safe named-namespace lifecycle and per-thread entry. `internal/network/topology/vpc_linux.go` and tests: router dummy, subnet gateways, namespace-local settings and deletion.
- `internal/reconcile/controller.go`, `network.go` and tests: per-VPC work queue, initial sweep, backoff, 60-second resync, observed status and orphan cleanup.
- `internal/apiserver/network.go`, `events.go`, `server.go` and tests: generated-route implementations, strict request decoding, SSE, auth and error mapping.
- `cmd/nephosd/main.go` and tests: migration, controller startup/sweep, readiness, shutdown.
- `cmd/nephos/network.go` and tests: generated-client VPC/subnet commands, name-or-ID lookup, pagination and waiting.
- `tests/vpc-subnet-smoke.sh`, `docs/tests/M1-connectivity-matrix.md`, `docs/DEVELOPMENT.md`, `docs/ARCHITECTURE.md`, `docs/RISKS.md`, `CHANGELOG.md`, `AGENTS.md` and `.github/workflows/ci.yml`: real-Docker validation and contributor routing.

### Task 1: Extend the spec-first HTTP contract

**Files:** Create `api/contract_test.go`; modify `api/openapi.yaml`, `internal/apiserver/server.go`, `Makefile` only if generator freshness must include additional files.

**Interfaces:** Define operation IDs `createVpc`, `listVpcs`, `getVpc`, `deleteVpc`, `createSubnet`, `listSubnets`, `getSubnet`, `deleteSubnet`, and `getEvents`. VPC and subnet resource responses include `id`, `name`, `cidr_block`, `state`, `state_reason`, `generation` and `observed_generation`; subnet adds `vpc_id` and `availability_zone`.

- [ ] **Step 1: Write the failing contract test.** Load the YAML with the existing `kin-openapi` dependency and assert all nine method/path pairs, required fields, `Idempotency-Key` request header, `limit`/`page_token` query parameters, bearer security, and typed AWS-style error responses. Example core assertion:

```go
doc, err := openapi3.NewLoader().LoadFromFile("openapi.yaml")
if err != nil { t.Fatal(err) }
vpcPath := doc.Paths.Find("/v1/workspaces/{workspace}/vpcs")
if vpcPath == nil || vpcPath.Post == nil {
    t.Fatal("createVpc is absent")
}
```

- [ ] **Step 2: Run `go test ./api`.** Expected: FAIL because the VPC path is absent.
- [ ] **Step 3: Add the OpenAPI routes and schemas.** Use `additionalProperties: false` on create bodies; require the agreed fields; specify 201 for creates, 202 for accepted deletes, 200 for reads/lists, 400/401/404/409 for errors, and `text/event-stream` for `getEvents`. Use the default-workspace path parameter even though only `default` is accepted at runtime. Run `make generate`. Add explicit 501 JSON stubs to `server` for newly generated methods until Task 7, so every intermediate commit compiles and never pretends to enforce absent behavior.
- [ ] **Step 4: Run `go test ./api ./internal/apiserver` and `make generate-check`.** Expected: PASS, with deterministic generated Go code and stubs returning 501 for unimplemented resource routes.
- [ ] **Step 5: Commit with `git commit -s -m "feat(api): define M1 VPC and subnet contract"`.**

### Task 2: Embed the SQLite schema and generated queries

**Files:** Create `sqlc.yaml`, `internal/store/migrations/0001_initial.sql`, `internal/store/queries.sql`, `internal/store/store.go`, `internal/store/store_test.go` and generated `internal/store/sqlc/*`; modify `go.mod`, `go.sum` and `Makefile`.

**Interfaces:** `store.Open(ctx context.Context, path string) (*store.Store, error)` applies migrations, enables WAL, foreign keys and busy timeout, and inserts only the `default` workspace. `Store.Close() error` releases the DB. The same-package migration test queries the workspace table directly; no production method exists only for that test. Generated query package is internal to `store`. The first migration creates `workspaces`, `vpcs`, `subnets`, `instances`, `enis`, `events`, `idempotency_requests`, `kernel_indexes`, and `schema_migrations`; resource rows have generation and observed/status columns.

- [ ] **Step 1: Add a failing migration test.** Open a temporary file DB twice, assert the sole workspace is `default`, `PRAGMA journal_mode` is `wal`, `foreign_keys` is 1, and an invalid subnet foreign key is rejected. Assert an existing DB is not recreated on reopen.

```go
s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "state.db"))
if err != nil { t.Fatal(err) }
defer s.Close()
var got int
gotErr := s.db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM workspaces").Scan(&got)
if gotErr != nil || got != 1 { t.Fatalf("workspaces=%d err=%v", got, gotErr) }
```

- [ ] **Step 2: Run `go test ./internal/store`.** Expected: FAIL because `store.Open` does not exist.
- [ ] **Step 3: Pin and implement.** Add `modernc.org/sqlite@v1.59.0` and `github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1` as a Go tool; add `go tool sqlc generate` after oapi generation. Configure sqlc v2 with `engine: sqlite` and `sql_package: database/sql`. Embed the migration with `go:embed`, run it transactionally under a schema version, set `db.SetMaxOpenConns(1)` so per-connection PRAGMAs are reliable, enable WAL/foreign keys/busy timeout, and create the default row. Put normal insert/get/list/update SQL in `queries.sql` and use the generated calls from handwritten transaction wrappers.
- [ ] **Step 4: Run `go mod tidy`, `go test ./internal/store`, `make generate-check`, and `make cross`.** Expected: PASS; all shipped binaries remain cgo-free.
- [ ] **Step 5: Commit with `git commit -s -m "feat(store): embed SQLite migrations and sqlc queries"`.**

### Task 3: Pin pure resource semantics and IDs

**Files:** Create `internal/service/validate.go`, `ids.go`, `errors.go` and `validate_test.go`. Model snapshots are introduced with their first service consumer in Task 4.

**Interfaces:** `service.ParseCIDR(raw string) (netip.Prefix, error)` returns a masked IPv4 /16–/28 prefix; `service.ValidateName(raw string) (string, error)` returns a trimmed, case-preserving UTF-8 name; `service.NewID(prefix string, random io.Reader) (string, error)` returns prefix plus 17 lowercase hex characters. `service.Gateway(prefix netip.Prefix) netip.Addr` returns base+1; `service.Overlaps(a, b netip.Prefix) bool` is pure. Domain errors carry `Code`, `Message`, `ResourceID` and HTTP status without depending on `apiserver`.

- [ ] **Step 1: Write table-driven failing tests.** Cover /15, /29, IPv6, malformed text, host-bit canonicalization (`10.0.1.7/24` → `10.0.1.0/24`), subnet containment and overlap, name spaces/Unicode/case/control characters/256-code-point rejection, and IDs matching `^vpc-[0-9a-f]{17}$`. Assert base, base+1, base+2, base+3 and last subnet addresses are reserved for future IPAM; first assignable is base+4.
- [ ] **Step 2: Run `go test ./internal/service`.** Expected: FAIL because the functions are absent.
- [ ] **Step 3: Implement the minimal pure helpers.** Use `net/netip` and `crypto/rand`, not `math/rand`. Preserve UTF-8 and case; only trim surrounding whitespace and reject control code points. Mask prefixes before comparison and payload hashing. Return AWS-style error codes (`InvalidParameterValue`, `InvalidSubnet.Range`, `InvalidSubnet.Conflict`).
- [ ] **Step 4: Run `go test ./internal/service` and `make test-race`.** Expected: PASS.
- [ ] **Step 5: Commit with `git commit -s -m "feat(service): validate VPC and subnet semantics"`.**

### Task 4: Implement transactional VPC/subnet services and durable events

**Files:** Create `internal/model/network.go`, `internal/store/resources.go`, `events.go`, `resources_test.go`, `internal/store/migrations/0002_idempotency_result.sql`, `internal/service/network.go`, `idempotency.go`, `pagination.go`, `network_test.go`; extend `internal/store/store.go`, `store_test.go`, `queries.sql` and generated code.

**Interfaces:** `service.Network` exposes `CreateVPC(ctx, input, key) (model.VPC, error)`, `ListVPCs(ctx, limit, token)`, `GetVPC(ctx, id)`, `DeleteVPC(ctx, id)`, and matching subnet methods. The service validates and decides dependencies inside `Store.WithTx`; the store owns a single SQLite transaction that commits the desired row, kernel index, original-result idempotency snapshot and event atomically. `store.EventsAfter(ctx, id, limit)` returns monotonically ordered events. Enqueue is an injected `func(string)` taking a VPC ID and may be absent after commit.

- [ ] **Step 1: Write failing tests against a real temporary SQLite DB.** Cover two overlapping-CIDR VPCs; sibling subnet overlap and outside-VPC rejection; duplicate names within a type but not across types; allowed Unicode and case-sensitive lookup; same-key replay with equivalent canonical CIDR; changed-payload `IdempotentParameterMismatch`; 24-hour expiry via injected clock; 40 concurrent identical-key calls yielding exactly one VPC/index/create event; list limit 2 with opaque token and stable ID order; dependency failure; delete/create race; event order. Use SQLite uniqueness constraints as the final concurrency guard, not an in-memory map.
- [ ] **Step 2: Run `go test ./internal/store ./internal/service`.** Expected: FAIL on missing service/store methods.
- [ ] **Step 3: Implement transactional operations.** Reserve ID and short index in the same transaction; hash canonical request JSON with SHA-256; use `(workspace, operation, key)` and expiry for keyed creates; compare payload hash and return the persisted original result on replay, including after status changes or deletion; insert desired row and event before commit; queue VPC ID only after commit. Delete checks dependents and marks `deleting` transactionally before an event. Use keyset `id > last_id ORDER BY id LIMIT limit+1`; tokens encode resource kind plus last ID in base64url and reject malformed/cross-resource tokens.
- [ ] **Step 4: Run `go test ./internal/store ./internal/service -count=1` and `make test-race`.** Expected: PASS with one row/event/index for the concurrent replay case and no data races.
- [ ] **Step 5: Commit with `git commit -s -m "feat(service): persist VPC and subnet desired state"`.**

### Task 5: Build safe VPC namespace and subnet-gateway topology

**Files:** Create `internal/network/netns/netns_linux.go`, `netns_linux_test.go`, `netns_integration_test.go`, `internal/network/topology/vpc_linux.go`, `policy_linux.go`, `vpc_linux_test.go`, `vpc_integration_test.go`; modify `go.mod`/`go.sum`.

**Interfaces:** `netns.Ensure(ctx, name) error` creates only an absent, valid `nx-vpc-<database short index>` named namespace; `netns.Do(ctx, name, func() error) error` runs on a locked disposable OS thread and binds netlink handles to that namespace; `netns.Delete(ctx, name) error` deletes only a validated Nephos name. `topology.Engine` exposes `EnsureVPC(ctx, model.VPC, []model.Subnet) error`, `DeleteVPC(ctx, model.VPC) error` and `ListVPCNames(ctx) ([]string, error)`. It does not import service or apiserver.

- [ ] **Step 1: Write failing pure and privileged-tag tests.** Pure tests assert names `nx-vpc-<short>` are <=15 characters where used for interfaces, stable across restart, and reject traversal/foreign prefixes. A Linux `integration` test records the caller's namespace inode, enters a temporary namespace 100 times concurrently, and verifies the caller inode and root routes remain unchanged. Topology integration tests assert two same-CIDR VPCs have distinct namespaces; dummy `nxr0` carries exactly each active subnet's base+1 `/32` gateway; removing a subnet removes its stale gateway; reapplying is idempotent; missing capability returns error without claiming success.
- [ ] **Step 2: Run `go test ./internal/network/...`.** Expected: FAIL because the packages are absent. Privileged-tag tests are not run outside the appliance and must state that reason when skipped.
- [ ] **Step 3: Implement from M0 SP3 evidence, not by importing spike modules.** Pin netlink v1.3.1 and netns v0.0.5. Namespace creation uses `unshare(CLONE_NEWNET)` and a named bind mount on a locked thread that exits; namespace entry never returns its switched thread to the scheduler. Existing namespaces are preserved and converged, not replaced. Inside each VPC namespace only: bring up loopback/dummy, enable IPv4 forwarding, disable redirects, add/remove gateway /32s, install local VPC rule and unmatched blackhole. Never write a global sysctl or host route. Use context-aware wrappers around blocking work.
- [ ] **Step 4: Run `go test ./internal/network/...`, `make test-race`, and the privileged integration test inside the appliance image.** Expected: PASS; namespace inodes remain stable and no host network object changes.
- [ ] **Step 5: Commit with `git commit -s -m "feat(network): reconcile isolated VPC gateways"`.**

### Task 6: Add level-triggered reconciliation and startup sweep

**Files:** Create `internal/reconcile/controller.go`, `network.go`, `controller_test.go`, `internal/store/reconcile.go`, `internal/store/migrations/0003_deletion_intent.sql`; extend model deletion intent, store status/event methods, migration tests, service delete guards, queries and generated sqlc code.

**Interfaces:** `reconcile.New(store *store.Store, network topology.Engine, interval time.Duration) *Controller`, `Controller.Enqueue(vpcID string)`, `Controller.Sweep(ctx context.Context) error`, and `Controller.Run(ctx context.Context) error`. Only one worker acts on a VPC key at a time. `Sweep` lists desired VPCs/subnets, collects only orphaned `nx-vpc-*` namespaces, converges desired namespaces, then returns. The daemon calls it before readiness becomes true. `Run` performs queued work and a full sweep every 60 seconds, with exponential retry on failures.

- [ ] **Step 1: Write failing tests with a real temp store and fake topology Engine.** A create committed with no enqueue must converge on `Sweep`; a fake first-call gateway error must write `failed` plus reason without advancing observed generation, then recover on retry and emit an ordered event; two queues for one VPC must not overlap Engine calls; a namespace absent from SQLite is deleted only when Nephos-prefixed; a desired namespace deleted behind Nephos's back is restored on resync; VPC/subnet delete removes gateway/namespace before deleting rows.
- [ ] **Step 2: Run `go test ./internal/reconcile`.** Expected: FAIL because the controller does not exist.
- [ ] **Step 3: Implement the worker and sweep.** Read fresh DB state for each key; never treat a process-local snapshot as authority. Keep queue memory bounded and deduplicated; use bounded exponential retry and context cancellation. Persist `state`, `state_reason`, `observed_generation` and status events only after observing Engine effects. A failed individual resource remains visible; startup can still report ready after the initial sweep has recorded those failures.
- [ ] **Step 4: Run `go test ./internal/reconcile -count=1`, `make test-race`, and `make cross`.** Expected: PASS.
- [ ] **Step 5: Commit with `git commit -s -m "feat(reconcile): converge VPC state from SQLite"`.**

### Task 7: Serve VPC/subnet CRUD and resumable events

**Files:** Create `internal/apiserver/network.go`, `events.go`, `network_test.go`; replace `network_stub.go` and its temporary test; modify `internal/apiserver/server.go`, `server_test.go`, `cmd/nephosd/main.go` and daemon tests.

**Interfaces:** `apiserver.New(token string, build version.Info, ready func() bool, resources *service.Network, events *store.Store) http.Handler` replaces the bootstrap constructor. The daemon opens `/var/lib/nephos/state/nephos.db`, starts controller initial sweep, sets readiness, then serves. Generated resource route methods replace Task 1's 501 stubs. Event handler resumes after a validated `Last-Event-ID`.

- [ ] **Step 1: Write failing `httptest` and daemon tests.** Assert unauthorized CRUD/SSE 401, unknown workspace 404, unknown JSON member 400, valid create 201 with transitional state, duplicate keyed POST returns same ID, same-key different body 409, list page ordering/token, missing IDs 404, dependent delete 409, and event stream IDs strictly increasing after reconnect. Assert `/v1/health` returns 503 before the injected initial sweep finishes, 200 afterwards.
- [ ] **Step 2: Run `go test ./internal/apiserver ./cmd/nephosd`.** Expected: FAIL on stub 501 or missing dependency wiring.
- [ ] **Step 3: Implement generated handlers and SSE.** Decode one JSON object with `DisallowUnknownFields` and reject trailing bytes. Require `workspace == "default"`. Map service errors into ADR-0008 JSON envelope and non-2xx status. Stream durable events as `id: <integer>` and `data: <JSON>` with flushing; poll the DB after disconnect gaps and respect request context. Bind auth/localhost checks to all new routes. Wire store/controller startup and bounded shutdown in `cmd/nephosd`.
- [ ] **Step 4: Run `go test ./internal/apiserver ./cmd/nephosd -count=1`, `make generate-check`, and `make test-race`.** Expected: PASS.
- [ ] **Step 5: Commit with `git commit -s -m "feat(api): serve VPC subnet and event endpoints"`.**

### Task 8: Add generated-client VPC/subnet CLI commands

**Files:** Create `cmd/nephos/network.go`, `network_client.go`, `network_wait.go`, `network_test.go`; modify `cmd/nephos/main.go` and `docs/DEVELOPMENT.md`.

**Interfaces:** `nephos vpc create|list|describe|delete` and `nephos subnet create|list|describe|delete` accept the roadmap grammar; references resolve exact ID or unique case-sensitive name through the generated client. `-o json` emits exact API objects. `--wait` polls GET to `available`, reports `state_reason` on `failed`, and times out nonzero after two minutes unless `--timeout` overrides it. A create generates one cryptographic idempotency key and reuses it on retries.

- [ ] **Step 1: Write failing CLI tests with `httptest.Server` and a temp credential file.** Cover UTF-8 names with spaces, name versus ID lookup, human versus JSON output, reused key on retry, 401 handling without token leakage, list pagination, terminal failure, cancellation and short timeout. Example:

```go
code, out, errOut := runCLIWithAPI(t, server.URL, tokenPath,
    "vpc", "create", "Lab East", "--cidr-block", "10.0.0.0/16", "--wait")
if code != 0 || !strings.Contains(out, "available") || errOut != "" {
    t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
}
```

- [ ] **Step 2: Run `go test ./cmd/nephos`.** Expected: FAIL because resource verbs are absent.
- [ ] **Step 3: Implement the CLI through `pkg/client` only.** The CLI reads `~/.nephos/credentials`, never prints the token, uses localhost endpoint, and does not reach into Podman or the Docker Engine for resource CRUD. Resolve names by fetching paginated lists. Generate a 128-bit+ request key once per create call, retry transient transport failures with that key, and implement bounded GET polling even if SSE is unavailable.
- [ ] **Step 4: Run `go test ./cmd/nephos -count=1`, `make cross`, and `make test-race`.** Expected: PASS.
- [ ] **Step 5: Commit with `git commit -s -m "feat(cli): manage VPCs and subnets through API"`.**

### Task 9: Validate the real appliance path and update project guidance

**Files:** Create `tests/vpc-subnet-smoke.sh` and `docs/tests/M1-connectivity-matrix.md`; modify `.github/workflows/ci.yml`, `docs/ARCHITECTURE.md`, `docs/RISKS.md`, `docs/DEVELOPMENT.md`, `AGENTS.md` and `CHANGELOG.md`.

**Interfaces:** The smoke script refuses a pre-existing `nephos` container or `nephos-data` volume, uses a temporary HOME, creates only its own labeled objects, and cleans only those exact objects. Add it after the bootstrap smoke in the native Ubuntu CI job; preserve logs on failure.

- [ ] **Step 1: Write a failing CI wiring test that parses the native Ubuntu workflow and asserts it invokes the new smoke script; observe its failure before editing CI. Then write the real-Docker smoke script. Do not add tests that merely grep human prose.** Build the committed image and CLI, `nephos up`, create two same-CIDR VPCs and two distinct subnets, wait for `available`, inspect `ip netns` and gateway /32s *inside* the appliance, verify list pagination/events/auth errors, down/up and recheck stable IDs/short indexes/gateways, delete subnet then VPC and assert namespace removal. Snapshot host namespace inode, links, routes, and nftables before/after; fail if altered. Assert both VPCs are isolated. The script runs after Tasks 1–8 and should pass only when the integrated appliance behavior works.
- [ ] **Step 2: Run `bash -n tests/vpc-subnet-smoke.sh` and then the smoke script.** Expected: syntax PASS; any runtime failure identifies missing product behavior, not an artificially absent command.
- [ ] **Step 3: Add matrix and docs.** In `docs/tests/M1-connectivity-matrix.md` record topology/isolation cases proven now and mark instance-packet cases owned by slice 3, without implying SG/NACL enforcement. Update ARCHITECTURE §5.5 fidelity status, RISKS T4/T8 evidence, contributor commands, AGENTS.md active-slice pointer, and CHANGELOG. Do not check M1 roadmap acceptance boxes before the complete two-instance demo passes.
- [ ] **Step 4: Run `make dev-ami`, `make appliance`, `make build`, `bash tests/vpc-subnet-smoke.sh`, `make ci`, and `make generate-check` locally. Then push the dedicated stacked branch and open a draft PR targeting `codex/m1-implementation` for native-Ubuntu CI only after the user-authorized stacked-PR choice; do not merge either PR. Expected: all commands and native CI pass, no test-owned Docker objects remain.
- [ ] **Step 5: Commit docs/CI/tests with `git commit -s -m "test(m1): verify VPC subnet slice on native Docker"` before pushing.**

## Self-review and handoff

Check every approved M1 slice-2 requirement against one task above, including the five Review Focus conditions. Verify generated interfaces against their consumers after Task 1; if generator names differ, record the smallest ruling before downstream edits. Run `git diff --check` and `git status --short --branch` before handing off. Slice 3 alone adds ENIs, Podman instances, console, and ping. Slice 4 owns restart marker/instance recovery, reset, leak checker, and full end-to-end closure.
