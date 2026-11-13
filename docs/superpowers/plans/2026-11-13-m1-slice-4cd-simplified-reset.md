# M1 Slice 4C/4D: Simplified Reset

**Status:** Agreed with the maintainer on 2026-11-13. This plan supersedes Tasks 6–9 of the
[2026-10-02 slice-4 plan](2026-10-02-m1-slice-4-recovery-reset.md) and the matching
soft-reset and hard-reset sections of the [slice-4 design](../specs/2026-10-02-m1-slice-4-recovery-reset-design.md).
The durable reset phase machine, the creation fence that survives a daemon restart, and
keyed idempotency aliases move to M8 (reliability). 4A and 4B are unchanged.

**Why:** M1 is the thinnest end-to-end slice. Its reset needs to leave the workspace
empty and prove there are no leaks. It does not need to survive a crash in the middle of
a reset, because rerunning a reset gives the same result.

## 4C: soft reset (`nephos reset`)

**Contract**
- `POST /v1/workspaces/default/reset`, optionally with `?timeout=<seconds>`.
  - The default timeout is 300 s and the maximum is 1800 s.
  - The call is synchronous and needs authentication.
- **Success:** `200` with `{workspace, leak_check}`, where `leak_check` is a `LeakResult` with `clean: true`.
- **Failures:** each carries an AWS-style code.

  | Status | Code | When |
  |---|---|---|
  | `409` | `ResetInProgress` | Another reset is already running. |
  | `409` | `LeakDetected` | The leak check found residue or could not observe everything. The body includes the `leak_check`. |
  | `504` | `ResetTimeout` | Resources are still being removed when the timeout runs out. The resources stay marked for deletion and the reconcilers keep removing them, so running the reset again continues the work. |
  | `503` | `ServiceUnavailable` | The daemon is not ready. |
- **During a reset:**
  - Creating VPCs, subnets or instances returns `409 ResetInProgress`.
  - This in-memory guard lasts only as long as the request. If the daemon restarts, the guard is gone, and rerunning the reset is the recovery path.
- **Idempotent:** a reset of an empty workspace succeeds with a clean leak check.
- **Order of work:**
  1. Terminate all instances, then wait until none remain.
  2. Delete all subnets, then wait.
  3. Delete all VPCs, then wait.

  It uses the existing `service` delete methods, so the reconcilers stay the only code that touches the kernel and Podman.
- **Verification:**
  - Under `Gate.Exclusive("default")`, run `reconcile.CollectOrphans`, then `leakcheck.Checker.Check`.
  - The reset succeeds only when `leak_check.clean` is true.
  - A cleanup failure after the gate admits the reset seals the gate, the same as orphan maintenance does.
- Events, the 24-hour idempotency window and the kernel-index history all survive a soft reset, as before.

**Code**
- `api/openapi.yaml` gets a `resetWorkspace` operation and `ResetResult`/`LeakCheck` schemas; then run `make generate`.
- `internal/service/reset.go` adds a `Reset` coordinator with:
  - the in-memory guard,
  - the ordered deletes,
  - a store-polling wait (250 ms),
  - a `Verifier` interface for the exclusive orphan collection and leak check.
- `Network` and `Instances` creates check the guard.
- `cmd/nephosd` wires `leakcheck.Checker` with the Podman, topology, registry, gate and `LinuxProcesses` adapters, which already exist.
- `internal/apiserver` adds the handler and the error mapping.
- `cmd/nephos reset [--timeout 5m]` prints a summary and exits non-zero on `LeakDetected`, `ResetTimeout` or any other error.

**Tests**
- Service tests:
  - the order of the deletes,
  - the guard rejecting creates,
  - idempotency on an empty workspace,
  - timeout reporting,
  - leak findings mapping to `LeakDetected`.
- An apiserver test for the status and code mapping.
- A CLI test for the exit codes.
- `tests/reset-smoke.sh` runs on a real appliance:
  1. Create a VPC, two subnets and two instances.
  2. Run `nephos reset`.
  3. Check that the lists are empty and that there are no `nx*` netns, no Nephos-marked Podman containers and no Nephos nftables tables.
  4. Run `nephos reset` a second time and check it is still clean.

## 4D: hard reset (`nephos reset --hard`)

**Contract**
- This path deliberately bypasses the API and SQLite, so that it still works when the appliance state is damaged.
- It also delivers ADR-0003's `nephos down --purge`, which the CLI currently rejects while waiting for this slice. Hard reset is purge followed by `up`.
- Purge inspects the `nephos` container and the `nephos-data` volume.
  - It refuses unless both carry `io.nephos.appliance=true`. Both labels are already set at creation.
  - It never removes anything it cannot prove belongs to Nephos.
- `reset --hard` reads the container's actual CPU, memory and pids limits before deleting. The new appliance reuses them unless flags override them.
- Order:
  1. Stop the container.
  2. Remove the container.
  3. Check the volume's label again, then remove the volume (no force).
  4. Remove the credential file.
  5. For `reset --hard` only, run `up`, which creates a fresh token, an empty database and new AMI storage.
- **Confirmation:** an interactive terminal asks before deleting. A non-interactive run must pass `--yes`, because this deletes every instance's root filesystem.
- **Idempotent:** if the container or volume is already gone, that step is skipped, and `reset --hard` still runs `up`.

**Code**
- `internal/appliance` gets `Manager.Purge(ctx) (Limits, error)`.
  - It reuses `Engine.Inspect`, `InspectVolume`, `Stop`, `Remove` and `RemoveVolume`, which already exist.
  - It returns the limits it observed, so `reset --hard` can pass them to `Up`.
- `cmd/nephos down --purge [--yes]` and `cmd/nephos reset --hard [--yes] [--cpus --memory --pids-limit]`.

**Tests**
- Manager unit tests with a fake engine:
  - It refuses a foreign or unlabeled container or volume.
  - It returns the observed limits.
  - It skips objects that are already missing.
  - It runs the steps in order.
- A CLI test for the confirmation and `--yes`. It replaces `TestDownRejectsPurgeUntilResetSlice`.
- `tests/reset-smoke.sh` extends to the hard reset:
  - A file written in an instance disappears.
  - The volume's creation time changes.
  - The new token differs.

## Docs to update in the same PRs

- The ROADMAP slice-4 delivery order and its reset paragraph. M8 reliability gains "crash-safe resumable reset".
- The M1 connectivity matrix reset rows.
- ARCHITECTURE: the reset description.
- CHANGELOG.
- ADR-0007 does not change. The database is still the only source of truth, and the reset guard does not outlive a request.
