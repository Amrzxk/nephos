# AGENTS.md: working on Nephos

Repository guidance for Codex and contributors. Keep this file focused on
always-applicable workflow and safety rules; use the linked documents as the
canonical source for detailed design and project context.

## Purpose and scope

Nephos is a local, open source cloud simulator for learning AWS-style
infrastructure. Instances are real Linux systems, and VPC networking is
enforced on real packets. Nephos reproduces AWS behavior and concepts; it is
not an AWS API emulator and does not implement IAM, managed databases,
multi-host clustering, or billing simulation. Read [docs/VISION.md](docs/VISION.md)
for the product boundary.

## Current milestone

M0 is complete. The next implementation milestone is M1 in
[docs/ROADMAP.md](docs/ROADMAP.md): `nephos up`/`down`, the SQLite store and
reconcile framework, the OpenAPI skeleton, and two instances in different
subnets that can ping each other. The roadmap and its checkboxes are
authoritative; update this summary if they move ahead.

The M0 spikes pass on WSL2 with Docker Desktop. Pull request #1 is merged and
the standard GitHub CI workflow passes; the manually dispatched native-Docker
`Spikes` workflow failed, so native Docker validation remains outstanding. Do
not implement work from a later milestone unless the roadmap is updated first.

## Start-of-task workflow

1. Run `git status --short --branch`. Treat existing modifications and
   untracked files as user work; do not overwrite, delete, stage, or reformat
   unrelated changes.
2. Confirm the active milestone and acceptance criteria in
   [docs/ROADMAP.md](docs/ROADMAP.md). Keep the task within that scope.
3. Use the routing table below and read the documents relevant to the area
   before editing it. Do not load unrelated design documents by default.
4. Before adding a feature, determine:
   - which milestone owns it;
   - whether it needs a new or superseding ADR;
   - which AWS behavior it reproduces and how Nephos deviates;
   - which unit, golden, integration, e2e, matrix, or lab tests pin it down;
   - whether it crosses a security boundary and requires a `RISKS.md` update.
5. Implement the smallest complete change. Update tests and required docs in
   the same change.
6. Run verification proportional to the change. Report commands that could
   not run and the exact reason; never present a skipped check as passing.
7. Review `git diff` and `git status` before handing work back. Confirm that
   unrelated pre-existing changes remain intact.

## Documentation routing

| Work area | Read first |
|---|---|
| Product scope and non-goals | [VISION.md](docs/VISION.md) |
| Milestone scope and priorities | [ROADMAP.md](docs/ROADMAP.md) |
| Any architectural change | [ARCHITECTURE.md](docs/ARCHITECTURE.md) and the relevant [ADR](docs/adr/README.md) |
| Appliance, packaging, `nephos up` | [ADR-0003](docs/adr/0003-appliance-container-packaging.md) |
| Instances, runtime, AMIs | [ADR-0004](docs/adr/0004-instances-as-system-containers.md) |
| Networking, firewalls, gateways | [ADR-0005](docs/adr/0005-nephos-owned-routed-network-plane.md), [ADR-0006](docs/adr/0006-learner-access-through-simulated-internet.md), ARCHITECTURE §5 |
| State, reconcilers, reset | [ADR-0007](docs/adr/0007-sqlite-state-and-reconciliation.md) |
| API or CLI shape | [ADR-0008](docs/adr/0008-rest-openapi-api-not-aws-compatible.md), ARCHITECTURE §6 |
| Web console | [ADR-0009](docs/adr/0009-web-console-react-typescript.md) |
| Labs | [ADR-0010](docs/adr/0010-lab-format-yaml-cel-probes.md), [LABS.md](docs/LABS.md) |
| Security or reliability implications | [RISKS.md](docs/RISKS.md), ARCHITECTURE §9 |
| Local setup and build commands | [DEVELOPMENT.md](docs/DEVELOPMENT.md) |
| Contribution and review process | [CONTRIBUTING.md](CONTRIBUTING.md) |

## Non-negotiable principles

1. **Real behavior over API fidelity.** Reproduce what AWS does, not what its
   API returns.
2. **Never fake silently.** If a rule cannot be enforced, the resource goes to
   `failed` with a reason, or the appliance refuses to start.
3. **The database is the only source of truth.** Kernel and runtime state must
   be rebuildable from it. Every engine operation is idempotent.
4. **Never touch the host.** No host network namespace, host firewall rules,
   host mounts, global sysctls, kernel module loading, `--network host`, or
   `--pid host`.
5. **All learner traffic enters through `nx-edge`.** Do not add port publishing,
   exec shortcuts, or another path that bypasses security groups, NACLs, and
   routes. The serial console is the single labeled exception.
6. **Use AWS concepts and names, not the AWS API.** JSON and CLI fields follow
   Terraform AWS provider attribute names.
7. **Features ship with tests.** Network behavior also needs connectivity-matrix
   coverage and an ARCHITECTURE §5.5 fidelity update. Learning features need a
   lab.
8. **No AWS logos or trade dress, telemetry, or account requirement.**
9. **Accepted decisions change only through a superseding ADR.** If code and an
   accepted ADR disagree, identify the conflict instead of silently deviating.

## M0 constraints carried into M1 and M2

The spike reports contain the evidence; preserve these implementation
constraints:

- The appliance entrypoint delegates cgroup v2 controllers to a leaf cgroup
  and fails closed if `memory`, `pids`, or `cpu` cannot be delegated.
- Instances require `--cap-add NET_ADMIN`; Podman's default capability set
  omits it.
- AMIs require `APT::Sandbox::User "root"` under `userns=auto`.
- sshd host keys are generated by an `ExecStartPre` that resets the list first;
  socket activation remains disabled.
- IMDS serves dated API versions such as `/2009-04-04/`, not only `/latest`.
- Force the `ds-identify` container policy. Removing a cloud-init module
  requires editing `/etc/cloud/cloud.cfg` because config lists append.

The `spikes/` tree is throwaway and uses separate Go modules. Root
`go test ./...` does not cover those modules.

## Go conventions

- Pin the toolchain in `go.mod`. Shipped artifacts and normal builds use
  `CGO_ENABLED=0`; `make test-race` is the deliberate test-only exception.
- Use the pure-Go SQLite driver. Do not add cgo dependencies.
- Wrap errors with `%w` and context. Do not panic outside `main` and genuinely
  impossible states. User-visible errors carry AWS-style codes (ADR-0008).
- Use structured `log/slog` logging with resource IDs and generations. Do not
  use `fmt.Print*` in library code.
- `context.Context` is the first parameter and cancellation is respected.
  `context.Background()` appears only in `main` and tests.
- Only `internal/network/netns` may switch network namespaces. A thread that
  switched namespace is never returned to the scheduler; bind netlink handles
  to namespace handles.
- Keep `internal/semantics`, `internal/explain`, and the renderer in
  `internal/network/firewall` pure: no I/O, clock, or randomness.
- Respect ARCHITECTURE §13 layering. In particular, `internal/network` and
  `internal/compute` never import `internal/service` or `internal/apiserver`.
- Edit `api/openapi.yaml` first, then run `make generate`. Never hand-edit
  generated files.
- Linux interface names are at most 15 characters and derive from
  database-allocated short indexes.
- Use table-driven tests. Update golden files with `-update`. Integration and
  e2e tests use the `integration` and `e2e` build tags. Every skip states why.
- Split files that exceed roughly 500 lines or combine responsibilities.

## Web conventions

- Use strict TypeScript and API types generated by `make generate`; no `any`.
- TanStack Query owns server state and is invalidated by server-sent events. No
  ad hoc `fetch` calls in components.
- Show status with an icon and text, never color alone.
- Use Nephos design tokens; do not use AWS icons or branding.
- Use Vitest for unit tests and Playwright for smoke tests.

## Development and verification

Development is supported on native Linux and Windows with WSL2 plus Docker
Desktop; macOS is best effort. Run project commands inside Linux or WSL, not
PowerShell. Keep WSL working copies on its ext4 filesystem. See
[docs/DEVELOPMENT.md](docs/DEVELOPMENT.md) for setup and platform details.

Use the narrowest relevant checks while iterating, then the broader checks
required by the changed area:

- `make build` — build all binaries.
- `make test` — unit and golden tests.
- `make test-race` — tests under the race detector.
- `make lint` — `golangci-lint` and formatting checks.
- `make generate` — regenerate API-derived code.
- `make ci` — the full pull-request suite. Its generated-code freshness step
  expects a clean candidate tree; do not misreport an expected dirty-tree
  failure as a product failure.
- `make appliance`, `make e2e`, and spike scripts — require Docker and, where
  documented, a privileged container. Some targets intentionally remain
  unavailable until their owning milestone implements them.

## Git and pull requests

- Use Conventional Commits such as `feat(network): ...`, `fix(store): ...`,
  `docs: ...`, `test: ...`, `refactor: ...`, and `chore: ...`.
- Every commit requires DCO sign-off with `git commit -s`.
- Work on a branch. Do not commit or push unless the user explicitly asks.
- A pull request explains the milestone and acceptance criteria advanced, the
  change, and the tests that cover it.
- Update ROADMAP checkboxes and `CHANGELOG.md` in the same pull request when
  milestone progress changes.

## Prohibited actions

- Do not implement AWS API endpoints, SigV4, or SDK compatibility.
- Do not use Docker or Podman networks to model VPCs and subnets.
- Do not create a second source of truth that outlives a request.
- Do not introduce cgo, AGPL dependencies, or telemetry.
- Do not write outside `~/.nephos` for the CLI or the `nephos-data` appliance
  volume for product behavior.
- Do not run `nephos up`, destructive cleanup, `git commit`, or `git push`
  unless explicitly requested.
