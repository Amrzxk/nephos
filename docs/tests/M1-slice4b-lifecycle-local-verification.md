# M1 slice 4B: local lifecycle checkpoint

Date: 2026-10-06. This records **Task 3 only**, not full 4B, native Ubuntu
CI, reset/leak completion or M1 acceptance. The approved
[plan](../superpowers/plans/2026-10-02-m1-slice-4-recovery-reset.md) assigns
strict inventory/orphan collection to Task 4 and complete leak accounting to
Task 5; both and the fresh full-4B boundary review remain required.

## Source, authority and platform

Task 3 is signed off in `1e8abe326f164ee66578270cd7809603aa13cf0e`
and published on `codex/m1-slice4-native` at the user's request on 2026-10-06.
It is not merged, and native Ubuntu CI remains pending. Prior 4A is
published through `6c696ce`; main is unchanged. The primary checkout's eight
preexisting documentation modifications remain untouched.

Execution is native inline with read-only independent review, not delegated
implementation. Privileged tests use only isolated, test-owned appliances and
volumes, private network/PID/cgroup namespaces and no host bind mounts.
Local Linux amd64: WSL2 kernel `6.6.87.2-microsoft-standard-WSL2`, Docker
Desktop Engine 28.3.2, cgroup v2, Docker memory 6,215,155,712 bytes.
Appliances use 2 CPUs, 4 GiB and 4096 tasks; nested instances remain the fixed
`t3.micro` limits. Measured userspace: Podman 5.4.2, conmon 2.1.12, crun
1.21 (`10269840aa07fb7e6b7e1acff6198692d8ff5c88`), nftables 1.1.3.
This is not evidence of reproducible apt inputs or native Ubuntu behavior.

The final privileged lifecycle fixture image was
`sha256:f59c3045b42fdf833a6952c558be78682c8f81e26ce3c2372d0a60842b8e8845`.
It contained the final candidate later committed as `1e8abe3`, but build
metadata still named its base `6c696ce`; it was **not** a clean 6c696ce build.
The subsequent appliance/CLI rebuild from product commit `1e8abe3` produced
image `sha256:c5882859e93c6138932cf56b2fd67c44d63f733c9108408ed171ca9ff92218ba`;
the full instance smoke passed on that rebuild.

## Implemented contract

- Effect leases count through runtime completion, deferred cleanup and child
  closure. Exact pending Start identities admit hook children only after
  existing private peer/runtime/namespace validation; independent or forged
  effects cannot cross pending exclusivity. Closing a parent does not erase
  its live children.
- Daemon contexts own both HTTP servers and upgraded consoles. Registry
  cancellation is not release: handlers join stream/resize workers and actual
  session cleanup before releasing references. SQLite never stays locked
  during runtime or console operations.
- Signals, unexpected worker exit and systemic inspection failure use the same
  bounded cleanup path. Required inspection failures suppress readiness;
  genuinely individual resource/ownership errors remain visible individually.
  Submitted uncertain mutations and failed session cleanup retain participation
  and fail closed; provably unsubmitted cancellation/dial failures do not.
- One total daemon budget is 25 seconds. All nested stops share at most 15
  within it, using complete expected instance identity and preserving roots.
  Docker stop allows 35 seconds. The entrypoint joins the daemon before stopping
  Podman. No new learner stop/start endpoint or console wire protocol exists.
- Console cleanup binds the complete container and exec identity, pins exact
  supervisors before runtime metadata disappears, and authorizes signals only
  after authoritative session removal. PID reuse, executable changes and
  namespace spoofing cannot grant signaling authority. A proven delayed child
  is stopped/revalidated/terminated; its parent naturally reaps it. Clean
  closure requires original process disappearance, not only pidfd readability.
  Failed inspection, identity binding or join cannot report clean success.

## Verification

All commands ran in Linux/WSL with `/usr/local/go/bin` on PATH and
`CGO_ENABLED=0`; `make ci` deliberately uses CGO for its race-only suite.

| Check | Observed result |
|---|---|
| `make ci` | Pass: formatting, vet, zero lint findings, root unit/race suites and all supported cross-builds. |
| `make generate-check` and `git diff --check` | Pass; no OpenAPI/dependency/schema change. |
| `go test ./internal/lifecycle ./internal/apiserver ./internal/compute/... ./cmd/nephosd ./internal/appliance -count=1` | Pass: drain/barrier, verified child, cancellation, delayed cleanup, common failure path, shared stop deadline, expected Stop identity, uncertainty and inspection classification. |
| Privileged `TestConsoleRealShutdownRetainsStoppedRoots` | Pass, 1.19 s on final fixture: two instances in separate subnets; three same-instance sessions (idle PTY, idle non-PTY, blocked output), resize, joined shutdown, exact retained full runtime IDs, stopped PID=0 and zero exec inventory while Podman still answers. Independent initial supervisor/init process paths disappear, owned instance cgroups are absent/empty and no fixture zombies remain before outer teardown. |
| Privileged `TestPodmanAdapterReal`, `TestPodmanAdapterRealResize`, `TestPodmanAdapterDelayedOutputExit`, `TestPodmanExecCloseIsSessionScoped` | Pass: exact byte streams/trailing NUL output and exit 7, real PTY resize, exit status after delayed output consumption, and closing one exec leaves the other session and instance running. |
| Privileged `TestVerifiedHookHTTPChildCompletesDuringDrain` | Pass, 0.24 s: the real private hook HTTP path verifies identity/namespace first, then completes its admitted Start child during exclusive drain. |
| `make appliance`, `make build` | Pass; subsequent rebuild uses product commit `1e8abe3`. |
| `bash tests/instance-ping-smoke.sh` on rebuilt image/CLI | Pass: prior byte/exit/task/hook/packet controls, overlapping VPC and spoof-drop witnesses, four retained file/ID/IP/ENI/MAC tuples through down/up, live ENI repair, resource teardown and available host snapshots. |

Tagged binaries are compiled with `go test -tags=integration -c`, copied into
the private fixture, and invoked with `NEPHOS_IN_APPLIANCE=1`, exact
`-test.run` expressions, `-test.count=1`, `-test.v`, and a bounded timeout.
The exact final privileged commands were (after creating the isolated fixture
described below; it is now removed):

```sh
CGO_ENABLED=0 go test -tags=integration -c -o /tmp/nephos-4b-lifecycle-tests ./cmd/nephosd
CGO_ENABLED=0 go test -tags=integration -c -o /tmp/nephos-4b-podman-tests ./internal/compute/podman
CGO_ENABLED=0 go test -tags=integration -c -o /tmp/nephos-4b-hook-tests ./internal/hook
docker cp /tmp/nephos-4b-lifecycle-tests nephos-4b-final-20261006:/tmp/lifecycle-tests
docker cp /tmp/nephos-4b-podman-tests nephos-4b-final-20261006:/tmp/podman-tests
docker cp /tmp/nephos-4b-hook-tests nephos-4b-final-20261006:/tmp/hook-tests
docker exec -e NEPHOS_IN_APPLIANCE=1 nephos-4b-final-20261006 /tmp/lifecycle-tests -test.run '^TestConsoleRealShutdownRetainsStoppedRoots$' -test.v -test.count=1 -test.timeout 120s
docker exec -e NEPHOS_IN_APPLIANCE=1 nephos-4b-final-20261006 /tmp/podman-tests -test.run '^TestPodmanAdapter(Real|DelayedOutputExit)|^TestPodmanExecCloseIsSessionScoped$' -test.v -test.count=1 -test.timeout 120s
docker exec -e NEPHOS_IN_APPLIANCE=1 nephos-4b-final-20261006 /tmp/hook-tests -test.run '^TestVerifiedHookHTTPChildCompletesDuringDrain$' -test.v -test.count=1 -test.timeout 120s
```

One initial hook expression matched no tests; it is not counted as evidence.
The corrected HTTP-child expression in this block ran and passed. Raw
privileged, full-CI and smoke stdout remains in the execution conversation,
not retained as repository artifact files; no artifact link or native CI log
is claimed. The task-done helper retains its targeted Go-suite output locally
at `.superpowers/sdd/2026-10-02-m1-slice-4-recovery-reset/task-3-tests.log`
(self-ignored). Task 10 still owns saved/replayable final QA artifacts.

The final fixture container was
`4054ce296b13378db21dab6fda19a2427f0d17f44837a49a2de7117a3cca7774`,
label `io.nephos.test=4b-final-20261006`, using its dedicated volume created
`2026-10-06T09:18:38Z`. Final proofs run before Docker teardown; removing the
outer appliance is never a substitute for product cleanup. Test-owned object
cleanup validates exact container ID/labels and volume timestamp/labels/no
other consumers. Both the final fixture and earlier controlled-probe
appliances/volumes have been removed after those checks; the smoke cleaned
its captured objects. These test volumes are not retained for recovery.
Unrelated user Docker objects are outside scope and remain intact.

## Controlled failure and review evidence

The original daemon returned with live PTY/non-PTY execs. An unexpected API
exit also lost the console cleanup failure. Deterministic regressions were
red for those behaviors before lifecycle ownership was added.

An independent early review requested three Important fixes: established exec
cleanup erased unknown participation; required runtime inspection could become
a resource-only failure and allow ready; and pre-submission request errors were
incorrectly uncertain. Each was reproduced and repaired test-first. Follow-up
review extended inspection regression coverage across Lookup, Inspect,
EnsureImage, Start, Create, Delete and Exec, including indirect adapter reads.

The strengthened process witness then found six remaining exec supervisors
despite an empty exec inventory. Podman's API intentionally sets delayed exec
cleanup; conmon forks a delayed cleanup child and waits for it. See the pinned
[Podman handler](https://raw.githubusercontent.com/containers/podman/v5.4.2/pkg/api/handlers/compat/exec.go)
and [conmon exit implementation](https://raw.githubusercontent.com/containers/conmon/v2.1.12/src/ctr_exit.c).
A one-variable `exit_command_delay=0` probe removed supervisors but failed
both real pipe and PTY exit-7 tests with missing exec status. That option was
rejected; shipped configuration and the remote-exit contract remain unchanged.
The original 300-second configuration now passes supervisor closure and
normal/delayed-consumer console contracts with scoped pinned cleanup.

Review then found an exit-during-pinning race. Identity-bound exited pins now
remain until reaping; candidates exiting before safe binding, or losing exe
entries while stat remains, fail closed. Deterministic models cover this race,
fork-between-scans, executable transition, denied inspection and pidfd failure;
real fixture witnesses cover actual process/cgroup disappearance. An optional
retrospective restoration of the old pinning bug was rejected by automatic
safety review and **did not execute**; it is not claimed as behavior-red
evidence. The regression runs and final inspected fix pass.

An extra Docker `--init` in one final fixture produced a different PID1 and
was correctly refused by the ownership guard. The fixture was recreated with
the image's own catatonit, matching production; no guard was relaxed. This is
fixture correction, not a failed accepted platform assumption or ADR reversal.

Final independent Task-3 re-review reports no unresolved Critical or Important
findings. It independently ran the targeted Go suites and diff check; privileged
and full CI results above are implementing-agent evidence. This review does
not replace the required fresh full-4B boundary review after Tasks 4–5.

## Remaining gates

- Strict broad runtime/network inventory, rowless orphan GC, namespace-alias
  rejection and rowless shutdown coverage: Task 4, not implemented here.
- Complete registered namespace/duplicate/socket/worker lifetime accounting
  and targeted process/FD leak result: Task 5, not implied by this fixture.
- Durable reset/fence/phase/replay and API/CLI behavior, hard purge and complete
  demo/e2e: Tasks 6–11 / 4C–4E, still pending.
- Native Ubuntu CI has not run on this code. Local WSL host nftables inspection
  was unavailable and is not reported passing; native CI must require it.
- No full native Windows/macOS manual console QA, merge, full 4B or M1 closure.
  Every M1 acceptance checkbox remains unchecked.
