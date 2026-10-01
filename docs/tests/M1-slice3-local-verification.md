# M1 slice-3 local verification checkpoint

Date: 2026-10-01. Branch: `codex/m1-slice3-native`, based on `9c6d8ee`.
This is a local execution record, not native Ubuntu CI or milestone sign-off.
The approved [plan](../superpowers/plans/2026-09-27-m1-slice-3-instances-ping.md)
has nine implemented tasks; do not repeat their implementation checklists.

## Implemented and verified locally

Instance/ENI/IPAM state, generation-aware reconciliation, private Podman REST
and exec, pinned fail-closed OCI plumbing, routed ENIs and source checks,
instance API/CLI, and the two authenticated console modes are implemented.
The [packet matrix](M1-connectivity-matrix.md) identifies exact positive and
negative packet paths and distinguishes them from namespace-only evidence.

Local WSL2/Docker Desktop validation used Docker 28.3.2 with 6,215,159,808
memory bytes. The local development AMI, appliance and CLI builds passed.
After the review fix, these commands passed again:

- `make ci`: formatting, vet, lint (0 issues), unit and race tests (17 tested
  packages each), all six CLI cross-builds, and Linux daemon/hook builds.
- `make generate-check`, `go mod tidy -diff`, and `git diff --check`.
- `make build` and `bash tests/instance-ping-smoke.sh`: configured ENIs
  before PID 1, independent reserved-safe addresses, distinct NUL output
  streams, stdin EOF/trailing output/exit 7, 512-task refusal/survival/release,
  bidirectional cross-subnet ICMP, remote-only overlapping-VPC isolation,
  source-spoof drop/destination witnesses, forced hook failure/no running
  PID 1, API-driven resource teardown, and test-owned object cleanup.
- Actual Linux PTY tests and `TestConsoleStalledOutputCancellation` prove
  terminal restoration, resize, canceled stdout/stderr backpressure, unchanged
  caller output flags/ownership, and remote disconnection.
- `govulncheck` v1.8.0: zero called vulnerabilities; one uncalled vulnerability
  was reported in imported packages. This is not a claim that the dependency
  graph has no known vulnerabilities.

Host namespace, links, routes and policy-rule snapshots matched after cleanup.
The WSL distro has no `nft`, so the host nftables snapshot was unavailable,
not passing. Native CI installs nftables and requires that additional check.
Native Windows/macOS console execution is unverified; cross-build success
does not establish runtime behavior.

## Independent review and fixes

The fresh-context review covered `9c6d8ee..a783eaf`, the approved spec/plan,
all execution rulings, and every Review Focus case. The preferred review
model hit a usage limit before doing work; an available reviewer performed
the complete review. No Critical finding was reported.

The reviewer reproduced one Important defect: a stopped local pipe consumer
blocked console cancellation, worker reaping and terminal restoration.
The regression failed for both stdout and stderr against the reviewed code
through a Go overlay, then passed after cancellable owned output handles and
bounded writes were added. Existing byte-stream/EOF/nonzero-exit and real
terminal tests, the whole Go suite and the privileged smoke passed afterward.
The reviewer did not re-review the fix; RED→GREEN and broader verification
are the fix evidence.

Stale plan/spec implementation status and the proposed WebSocket dependency
were also corrected. Although graded Minor by the reviewer, these were
treated as Important for accurate contributor/agent routing. No minors are
deferred. The reviewer set aside the six explicitly scoped behaviors
recorded in the final rulings below; none is implicitly claimed implemented.

Pending pipe/terminal writes have a ten-second limit; idle interactive
sessions do not. Regular-file output preserves its descriptor offset/append
semantics and uses normal filesystem I/O rather than poller deadlines.
Darwin's best-effort output implementation temporarily changes shared
nonblocking flags and restores them at cleanup; native runtime QA is pending.

## Next gates and authority

Signed-off local commits and isolated privileged tests/owned-object cleanup
were authorized. No branch push, pull-request creation or merge has occurred.
The next integration gate is an authorized branch push and draft PR against
`main`, followed by the native Ubuntu workflow. Do not claim native CI green
or merge without its evidence and user direction.

Slice 4 requires its own reviewed plan: retained-root restart/marker recovery,
automatic orphan collection, whole-appliance active-console shutdown checks,
soft/hard reset, comprehensive kernel/runtime leak checks, the replayable
`docs/demos/M1.md`, `make e2e`, and full native/WSL2 QA. Running-instance
quotas/capacity discovery remain M2. M1 roadmap acceptance boxes remain
unchecked.

The primary checkout's pre-existing planning changes were preserved.
Implementation and review fixes live only on the isolated branch.

## Rulings I made

These are the execution ledger's complete rulings, including the cost if
wrong, retained before plan-scoped scratch cleanup:

- Use codex/m1-slice3-native rather than codex/m1-instances-ping — the latter already exists at slice-2 merge; preserve it — cost if wrong: branch naming only.
- Create the veth directly from the VPC into the pinned instance namespace, then rename its peer to eth0 — avoid transient links in the appliance root namespace while preserving ADR-0005's topology — cost if wrong: kernel support is gated by real integration tests.
- Validate netns ownership by comparing its NS_GET_USERNS inode with the process user namespace, not by requiring NS_GET_OWNER_UID nonzero — actual createRuntime gives owner_uid=0 despite correct ownership and mapped root=200000 — cost if wrong: identity tests must catch mismatched namespaces.
- Use auto:size=65536 rather than Podman's unsized auto — accepted ADR-0004 requires a unique 65536-ID range but this Podman defaults to 1024 — cost if wrong: each instance consumes more of the configured subordinate-ID pool.
- User approved unprivileged ICMP on 2026-09-28: strip ping's CAP_NET_RAW file capability from the development AMI and set net.ipv4.ping_group_range=0 65535 only inside the instance namespace — actual ping exec failed with EPERM under approved capabilities; a controlled run proved this approach works without adding capabilities — cost if wrong: ping fails, pinned by real packet tests. Include this sysctl in Task 4's create contract.
- Run the bounded pids probe in a test-only systemd scope with TasksMax=infinity, leaving the container's pids.max=512 and production systemd defaults unchanged — systemd's init.scope is independently limited to 76 and masked the aggregate boundary — cost if wrong: fixture might not reach the aggregate boundary, explicitly asserted at 512.
- The plan's phrase "network error ... without calling EnsureENI" cannot describe a plumber error; malformed identity/dial errors make zero calls, while a forced EnsureENI failure makes exactly one call and blocks PID 1 — otherwise a real network failure cannot be observed — cost if wrong: an error path may falsely mark an instance started, covered by the real forced-failure test.
- Runtime-only Podman adapter integration tests use a test-owned private service with an explicit empty hooks directory — the production global hook correctly requires SQLite state those tests do not create — cost if wrong: tests could mask production hook behavior; separate real createRuntime hook tests cover it against the production service.
- When Terminate advances a generation during Create, retain the returned owned Podman ID only on the already-terminating row before immediate teardown — otherwise a failed Delete would leave an untracked object and no safe retry path — cost if wrong: teardown could target a wrong runtime; the adapter's ownership guard and create-race retry test constrain it.
- Instance readiness reads committed VPC/subnet available states and matching observed generations; the createRuntime hook independently checks those rows again before ENI plumbing — this keeps network package free of a store import — cost if wrong: a changed topology between reads makes the hook fail visibly, not boot an unplumbed instance.
- No extra network-to-instance acceleration callback is needed in this slice; the required initial ordering and 60-second durable resync cover dependency changes — cost if wrong: a newly created instance can wait up to one resync period after its subnet becomes available.
- crun's returned hook error omits hook stderr; assert a nonempty hook-specific state_reason plus an instance-scoped forced-failure proof, failed generation, and runtime not running — preserves the specified fail-closed check without assuming arbitrary stderr is exposed by Podman — cost if wrong: unrelated errors could pass, constrained by the explicit forced-failure marker for the same instance.
- Copy a test-only observer over the hook executable in the isolated appliance, delegate to the unchanged production hook, then inspect pinned eth0 before returning to OCI — proves pre-PID-1 timing through the real product chain and supports a bounded forced hook fault without adding a public API or shipping test hooks — cost if wrong: the wrapper could change boot behavior; it runs only in smoke fixtures and production hook integration tests remain independent.
- Keep resource lookup/wait and platform input helpers in separate small files; detect terminal size changes with a 250 ms local poll instead of Unix-only SIGWINCH — preserves portable resize and cancellation without changing stdin flags — cost if wrong: a resize may take up to one poll interval to propagate.
- Use gpt-6.1-sol for the independent whole-branch review after gpt-6-astra failed immediately with its model usage limit — the preferred reviewer did no review work; an available fresh-context reviewer preserves the required independent gate — cost if wrong: the fallback may miss issues a stronger reviewer would find, mitigated by the same complete review package, requirements and Review Focus.
- Retained-root recovery and marker persistence remain slice 4 — the accepted delivery boundary explicitly reserves them; no instance restart claim is made — cost if wrong: an appliance restart can lose running-instance usability until slice 4 lands.
- Automatic orphan collection after interrupted creation/teardown remains slice 4 — slice 3 retains cleanup IDs and proves normal termination, not crash recovery — cost if wrong: interrupted work can retain owned runtime objects until recovery/leak closure is implemented.
- Appliance-wide shutdown of active console execs remains a slice-4 validation item — slice 3 proves per-session disconnect/cancellation, not whole-appliance shutdown — cost if wrong: shutdown may leave active exec cleanup incomplete until the appliance stops; no clean-shutdown claim is made.
- Soft/hard reset and the comprehensive leak checker remain slice 4 — current smoke teardown removes its known resources, not arbitrary interrupted-state leaks — cost if wrong: users must not treat current cleanup as M1 reset support.
- Native Windows/macOS console runtime QA remains unverified — the supported contributor flow runs Linux/WSL and all six cross-builds pass, but that is not runtime evidence — cost if wrong: native non-Linux console behavior may fail until platform QA catches it.
- Running-instance quotas/capacity discovery remain M2 — slice 3 enforces fixed per-instance and appliance safety limits only — cost if wrong: admission can fail at runtime rather than be predicted by capacity checks.
- Replace the proposed gorilla dependency with the already implemented coder/websocket v1.8.15 context-aware API — the shared protocol, pure-Go requirement and permissive-license policy remain unchanged; correct the stale plan rather than mislead future agents — cost if wrong: transport cancellation/close behavior needs continued regression coverage; existing stream, protocol and close tests cover it.
- Local pipe/terminal writes have a ten-second pending-write deadline and obey cancellation, with private nonblocking stream handles on Linux and private cancelable handles on Windows — matches bounded transport writes and fixes demonstrated blocked output without an idle timeout or stranded workers — cost if wrong: a consumer stalled for over ten seconds fails the session rather than receiving eventual output; documented in the protocol.
- Darwin duplicates output handles, temporarily enables shared nonblocking flags and restores them after writers are reaped; regular-file output keeps the original descriptor/offset on Unix — Darwin lacks Linux procfs independent reopens; do not silently reset redirected file offsets — cost if wrong: another concurrent writer sharing a Darwin description can see nonblocking mode during the session, and blocking regular-filesystem I/O is not poller-cancellable; native runtime QA remains pending and limitations are documented.

## Deferred minors

None. The stale documentation finding was re-graded and addressed.
