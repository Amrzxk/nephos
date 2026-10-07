# M1 slice 4B: ownership, orphan collection, and lifetime proof

Date: 2026-10-07. Tasks 4–5 finish the locally implemented 4B boundary on
`codex/m1-slice4-native`, following the published Task-3 lifecycle code
`1e8abe3` and local inventory foundations `44d21b1`. This report supersedes
the continuation section of the [foundation checkpoint](M1-slice4b-inventory-foundations.md),
not its historical evidence. The candidate is the pending implementation
change containing this report; full-4B review covers `6c696ce` through that
candidate. New changes are **local, not pushed or merged**. Native Ubuntu CI,
4C–4E, reset integration, and M1 closure are not claimed.

## Implemented boundary

- One SQLite transaction snapshots every desired VPC, subnet, instance and
  primary ENI, including pending/deleting owners and null or stale runtime-ID
  caches. All pages finish before external inspection or mutation begins.
- Broad runtime and network inventory observes malformed Nephos candidates,
  valid foreign namespace mounts, and markers misplaced in the appliance's
  private root. A complete-set ambiguity or required inspection failure blocks
  collection; prefix-only legacy VPC sweeping no longer deletes orphans.
- Startup GC runs before readiness. Periodic GC uses exclusive effect admission,
  raw runtime access, and separate bounded wait/work budgets. A busy console
  causes a periodic skip, not cancellation; admitted cleanup failure seals
  admission and enters common daemon shutdown. Rowless owned roots also take
  part in nested stop, which retains their files rather than deleting them.
- Desired ENIs survive only in their expected workspace/subnet/VPC scope with
  matching index/IP/MAC. An orphan veth requires both full aliases, the actual
  pinned peer namespace, reciprocal peer indexes, peer MAC/IP and strictly
  derived routes. Generic unmarked neighbors are preserved. Parent deletion
  rejects foreign links, addresses, IPv4/IPv6 rules, route attributes and tables.
- Normal VPC deletion and orphan deletion clean owned kernel children while
  pinned, then detach the identity-revalidated named mount. A retained external
  FD can survive that detach; unlink is never destruction proof. Owned nft
  cleanup uses exact marked table/chain/set/rule structure. Canonical historical
  source-set values may outlive a vanished veth, but never authorize link deletion.
  Desired policy convergence still compares actual assigned source values.
- `model.LeakResult` distinguishes `Complete` from `Clean`. Injected
  `leakcheck.Checker` is non-destructive and requires all observation adapters,
  empty desired resources/ENI leases, no remaining marked runtime/kernel objects,
  no outstanding effects/references, no cleanup errors, and the current caller's
  live exclusive lease token. Another caller's diagnostic exclusivity bit, or
  an expired token, grants no success authority. Reset consumes this foundation
  in 4C; there is no reset endpoint or CLI feature in this change.
- Namespace FDs/duplicates, netlink sockets and locked workers register before
  exposure and release only after real close and return to the appliance root.
  Workers do not unlock switched OS threads. Temporary validation handles are
  accounted too. Close/restore failures remain visible. Ephemeral remembered
  namespace/runtime identities support task/FD/cgroup/supervisor observation
  after unlink/removal; this is not durable resource intent or deletion authority.

## Regression and independent witnesses

Observed RED then GREEN for missing snapshot/GC coordination, misplaced markers,
missing lifetime registration, exclusive-token transfer, foreign routes/addresses/
rule selectors, and stale owned policy after independent veth loss. The external
FD fixture also reproduced a false-clean bug: bind-mounted namespace FD symlinks
show a pathname (possibly deleted), not necessarily `net:[inode]`. Observation
now correlates known inode plus actual NSFS filesystem instead of filtering the
symlink text.

`tests/fixtures/namespacehold` is a separate, test-only process, never included in
product images. In FD mode it retains an independently opened namespace across
unlink. Checker reports a namespace-FD finding; independent `nsenter` plus
`ip -j link` and `nft -j list ruleset` assert the retained `held0` link and
`nephos:test-residue` table. Actual helper close and process reap precede a clean
result. The kernel-cleanup test separately holds a VPC FD and confirms no router
link or owned policy rule survives product deletion.

In socket-only mode the helper returns to the appliance namespace, closes every
namespace FD, and retains only a UDP socket. Known task/NS-FD observation finds
zero references while the helper is alive: **this is a demonstrated blind spot,
not a universal absence proof**. The equivalent registered netlink socket test
closes all explicit namespace pins, independently reads its live kernel links,
and requires `Complete=true, Clean=false` until actual socket close and registry
release. Deferred-close barrier tests likewise forbid worker completion before
real cleanup. No cross-process socket namespace syscall is assumed.

The full topology regression first failed because old fixture teardown left
explicitly foreign objects and authentic stale source-set entries. Fixtures now
remove only their own foreign corruptions after refusal assertions and tear down
ENIs before their containers. The stale-policy product regression was reproduced
in a fresh appliance and fixed using separate exact ownership-shape proof; foreign
policy/rule/route tests were not relaxed. The failed first appliance was discarded
as test-owned remediation, **not** recorded as passing an inner leak check.

## Commands and results

```sh
export PATH=/usr/local/go/bin:$PATH
make ci
make generate-check
go mod tidy -diff
git diff --check
CGO_ENABLED=0 go build -o /tmp/nephos-task45-namespacehold ./tests/fixtures/namespacehold
CGO_ENABLED=0 go test -tags integration -c -o /tmp/nephos-task45-topology.test ./internal/network/topology
CGO_ENABLED=0 go test -tags integration -c -o /tmp/nephos-task45-netns.test ./internal/network/netns
CGO_ENABLED=0 go test -tags integration -c -o /tmp/nephos-task45-leakcheck.test ./internal/leakcheck
CGO_ENABLED=0 go test -tags integration -c -o /tmp/nephos-task45-daemon.test ./cmd/nephosd
CGO_ENABLED=0 go test -tags integration -c -o /tmp/nephos-task45-runtime.test ./internal/compute/podman
CGO_ENABLED=0 go test -tags integration -c -o /tmp/nephos-task45-reconcile.test ./internal/reconcile
```

All pass. `make ci` covers format/vet/lint (zero issues), unit/race tests and
Linux/macOS/Windows cross-builds; it is not native runtime QA. Tagged binaries
are copied to `/tmp/{topology,netns,leakcheck,daemon,runtime,reconcile}.test`,
and the helper to `/tmp/namespacehold`, in the exact appliance below. Commands
run via `docker exec -e NEPHOS_IN_APPLIANCE=1 <full-container-ID>`:

```sh
/tmp/topology.test -test.v -test.timeout 180s
/tmp/netns.test -test.v -test.timeout 90s
# Also pass -e NEPHOS_NAMESPACEHOLD=/tmp/namespacehold to docker exec:
/tmp/leakcheck.test -test.v -test.timeout 90s
/tmp/daemon.test -test.run 'TestConsoleRealShutdownRetainsStoppedRoots|TestServeCollectsRowlessRootBeforeReadyReal' -test.v -test.timeout 90s
/tmp/runtime.test -test.run TestRuntimeOrphanInventoryReal -test.v -test.timeout 90s
/tmp/reconcile.test -test.run TestRuntimeOrphanCollectionReal -test.v -test.timeout 90s
```

All selected tests actually run and pass, without skips: real packets/spoof/
overlapping-VPC controls, desired policy repair, orphan/foreign negatives,
namespace alias/type/pin guards, registered lifetime barriers, external holds,
startup rowless GC, and retained-root console shutdown. Privileged tests exercise
candidate Go binaries with real kernel/Podman adapters; the image itself is the
preceding product image, **not** a rebuilt final candidate product image.

Final inner proof, while Podman is still alive:
`TestApplianceOwnedStateEmptyBeforeOuterTeardown` reports `Complete=true`,
`Clean=true`, zero findings/errors/registered references. Known-identity tests
assert their retired references separately; this final checker is not claimed
to remember every identity from earlier independent test processes. Independent
`ip netns list`, `podman ps -a --no-trunc` and root `nft -j list ruleset` are
empty of owned objects. `ps -eo pid,ppid,stat,args` shows only entrypoint,
catatonit, private Podman service and fixture sleep processes: no nested init,
conmon, namespacehold, or zombies. All precede outer teardown.

Platform: WSL2/Docker Desktop, Docker 28.3.2, memory 6,215,159,808 bytes.
Image `sha256:c5882859e93c6138932cf56b2fd67c44d63f733c9108408ed171ca9ff92218ba`.
Production entrypoint/catatonit, `/bin/sleep infinity` command, 2 CPUs, 4 GiB,
4096 tasks, private cgroups/network/PID, no host binds or published ports.
Final container `bd8b2b326b04794fbd2ade38e5ed9a6f4829f988cd979b692f32c3b62a78434e`,
volume `nephos-task45-final-20261007-data`, created `2026-10-07T11:29:37Z`,
both labelled `io.nephos.test=task45-final-20261007`. Cleanup validates full ID,
labels, volume creation time and all consumers before removing only these objects.
Both final outer objects were removed after the last inner proof; label-scoped
container and volume listings are empty. The earlier failed fixture and its
volume were also removed as remediation, not passing leak evidence. Temporary
test data was discarded; no user container/volume or primary-checkout work changed.
Raw stdout remains conversation evidence; no published CI artifact is claimed.

## Review and remaining work

Independent full-4B review inspects Tasks 3–5 together, not merely this checkpoint's
new files. Earlier ownership, route/address/rule, token and lifetime findings were
fixed with tests. Final independent re-review approves the complete 4B code
boundary with no unresolved Critical, Important or Minor findings, including
the post-regression separate desired/ownership policy predicates. The reviewer
independently ran unit tests and diff checks; privileged evidence above belongs
to the implementer, not an independently repeated reviewer run.

Next is **4C, Tasks 6–8**: durable SQLite reset operation/fence, continuation and
leak-result persistence, then synchronous API/CLI waiting and keyed replay.
4D/4E, native Ubuntu CI, full WSL demo, publication/merge and all open roadmap
acceptance criteria remain separate work. Do not mark M1 complete.
