# M1 slice 4B Task 4: inventory foundations checkpoint

Date: 2026-10-07. Task 4 is **in progress**, not complete. Task 3 is already
published at `1e8abe3`; this continuation starts from `0b1cc4e` on
`codex/m1-slice4-native`. These new changes are local, not pushed or merged.
No new independent full-4B review or native Ubuntu CI result is claimed.

## Implemented and tested

- `Runtime.Inventory` lists all containers, including stopped roots, then
  inspects immutable full IDs without a managed-label filter. Candidates
  include reserved instance names and any Nephos label/annotation, including
  markers seen in the list but lost in inspect. Only complete ownership
  proof sets `Managed`; ambiguous candidates retain identity and reason.
- Null/invalid/duplicate lists, failed/denied/disappearing inspections and
  changed IDs fail with `ErrInspectionUnavailable`, never usable partial
  observations. The daemon-owned runtime observes this classification.
- The runtime-only collector preserves owners by workspace/instance identity,
  not cached runtime ID. Pending owners with no ID and retained owners with
  stale IDs survive. The full candidate set is prevalidated before any root
  removal; ambiguous or duplicate owners block all deletion. The adapter
  re-inspects ownership immediately before each removal.
- Namespace mount checks pin an `O_NOFOLLOW` descriptor, require network NSFS
  type, and compare device/inode against appliance PID 1 and other reserved
  namespace paths. Root/cross-VPC aliases, symlinks, non-network NSFS mounts
  and failed required neighbor inspection cannot authorize callbacks or
  deletion. This is an ownership prerequisite, not complete kernel cleanup.

## Evidence and limitations

Behavior RED preceded implementation for missing runtime inventory, hidden
list-only markers, absent runtime orphan removal/ambiguity rejection, and
real-kernel root/cross-VPC/user-namespace alias acceptance. An initial test
compiler type mismatch was corrected before the valid inventory RED; it is
not behavior evidence. Existing symlink rejection was a negative control.
Tests use independently specified identities, not production-built expected
outputs. The new real-runtime test corroborates list/inspect against Podman.

Verification commands:

```sh
CGO_ENABLED=0 go test ./... -count=1
make ci
make generate-check
go mod tidy -diff
git diff --check
CGO_ENABLED=0 go test -tags integration -c -o /tmp/nephos-4b-netns-20261007 ./internal/network/netns
CGO_ENABLED=0 go test -tags integration -c -o /tmp/nephos-4b-podman-20261007 ./internal/compute/podman
CGO_ENABLED=0 go test -tags integration -c -o /tmp/nephos-4b-reconcile-20261007 ./internal/reconcile
CGO_ENABLED=0 go test -tags integration -c -o /tmp/nephos-4b-topology-20261007 ./internal/network/topology
```

Copied tagged binaries run in `nephos-4b-inventory-20261007` with
`NEPHOS_IN_APPLIANCE=1`:

- Complete netns test binary: pass, including pinned instance handles and all
  alias/type negatives. The first broad run omitted the appliance environment
  and skipped the pinned-instance case; the corrected full run has no skips.
- `TestRuntimeOrphanInventoryReal`: pass, with an owned running container and
  a stopped partially marked neighbor, both unchanged by observation.
- `TestRuntimeOrphanCollectionReal`: pass, removing a stopped owned rowless
  root while preserving the exact pending and stale-cache roots.
- Complete topology test binary: pass, including real ENI convergence,
  packets/source checks, overlapping-VPC isolation, healthy resync and foreign
  policy refusal. This is regression evidence, not new ENI orphan GC proof.

`make ci` initially reported a large range-value copy, then a test-only
tagged-switch suggestion; both were corrected and the full suite passed.
Local CI covers formatting/vet/lint, unit/race
tests and cross-builds; it does not prove native Docker behavior.

Platform: WSL2/Docker Desktop, Engine 28.3.2, memory 6,215,159,808 bytes.
The isolated appliance uses 2 CPUs, 4 GiB, 4096 tasks, private cgroup/PID/network
namespaces, no published ports or host bind mounts. It uses the prior image
`sha256:c5882859e93c6138932cf56b2fd67c44d63f733c9108408ed171ca9ff92218ba`
with candidate test binaries copied in; this is **not** a rebuilt product-image
or final daemon startup test. The production entrypoint prepares the private
runtime, but `/bin/sleep infinity` replaces the daemon for these engine tests.

Test-owned outer container ID:
`90c90d2e99f7d53626d742dfdde8e19fd0d83078856531d20ba7fd3b0bf55e69`.
Dedicated volume: `nephos-4b-inventory-20261007-data`, created
`2026-10-07T09:55:30Z`; both carry `io.nephos.test=4b-inventory-20261007`.
Cleanup is restricted to those captured identities after label/time/consumer
validation; both outer objects have been removed. Inner Podman container
records and named namespaces were empty before outer removal, not a complete
namespace-reference/process/socket leak proof. Raw outputs are in the conversation and self-ignored execution
ledger; no repository artifact publication is claimed.

## Required continuation

The runtime-only helper is not called by daemon startup/resync yet. Finish
the consistent SQLite desired snapshot and exclusive maintenance wiring
together with strict network/ENI inventory and child-before-parent GC. Include
foreign neighbors, malformed namespace candidates and cross-VPC ENI ownership
negatives before enabling collection. Extend nested shutdown to observe
rowless owned runtime containers. Do not treat existing prefix-only VPC
cleanup as complete Task-4 proof.

Task 5 still owns registered handles/duplicates/sockets/workers, known-identity
process/cgroup corroboration and complete-versus-clean leak results. The
fresh full-4B delivery-boundary review follows both tasks. Reset, native CI,
matching WSL demo and all M1 acceptance criteria remain open.
