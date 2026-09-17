# CLAUDE.md: working on Nephos

Context and conventions for AI coding sessions (and new contributors). Read this before changing anything.

## What Nephos is

A local, open source cloud simulator **for learning** AWS-style infrastructure. Instances are real Linux systems you can SSH into. VPC networking (subnets, route tables, internet and NAT gateways, security groups, network ACLs) is enforced on real packets, so a missing route or a wrong rule breaks connectivity the way it would in AWS. It ships a console with a live topology map, guided labs with automatic checks, and `nephos explain`, which names the rule that blocked a flow.

**Nephos is not** an AWS API emulator. `aws --endpoint-url` will never work. It also has no IAM, no managed databases, no multi-host clustering, and no billing simulation. See [docs/VISION.md](docs/VISION.md).

## Current phase

Planning is complete (documents written 2026-09-17). **There is no application code yet.**

The next step is **M0** in [docs/ROADMAP.md](docs/ROADMAP.md): repository scaffold, CI, and spikes SP1–SP4. Don't start M1 before the spike reports land in `docs/spikes/`, and don't write code outside the current milestone's scope.

## Read before you change things

| Area you're touching | Read first |
|---|---|
| Anything architectural | [ARCHITECTURE.md](docs/ARCHITECTURE.md) and the relevant ADR |
| Networking, firewalls, gateways | [ADR-0005](docs/adr/0005-nephos-owned-routed-network-plane.md), [ADR-0006](docs/adr/0006-learner-access-through-simulated-internet.md), ARCHITECTURE §5 |
| Instances, runtime, AMIs | [ADR-0004](docs/adr/0004-instances-as-system-containers.md) |
| Appliance, packaging, `nephos up` | [ADR-0003](docs/adr/0003-appliance-container-packaging.md) |
| API or CLI shape | [ADR-0008](docs/adr/0008-rest-openapi-api-not-aws-compatible.md), ARCHITECTURE §6 |
| State, reconcilers, reset | [ADR-0007](docs/adr/0007-sqlite-state-and-reconciliation.md) |
| Web console | [ADR-0009](docs/adr/0009-web-console-react-typescript.md) |
| Labs | [ADR-0010](docs/adr/0010-lab-format-yaml-cel-probes.md), [LABS.md](docs/LABS.md) |
| Scope and priorities | [ROADMAP.md](docs/ROADMAP.md) |
| Security or reliability implications | [RISKS.md](docs/RISKS.md) |

## Non-negotiable principles

These outrank convenience, cleverness, and speed.

1. **Real behavior over API fidelity.** Reproduce what AWS *does*, not what its API returns.
2. **Never fake silently.** If a rule can't be enforced, the resource goes to `failed` with a reason, or the appliance refuses to start. Nothing is ever "stored but not enforced".
3. **The database is the only source of truth.** Kernel and runtime state must be rebuildable from it. Every engine operation is idempotent.
4. **Never touch the host.** No host network namespace, no host firewall rules, no host mounts, no global sysctls, no kernel module loading, and never `--network host` or `--pid host`.
5. **All learner traffic enters through `nx-edge`.** Never add port publishing, exec shortcuts, or anything else that bypasses security groups, NACLs, and routes. The serial console is the single, clearly labeled exception.
6. **AWS concepts and names, not the AWS API.** JSON and CLI field names follow the Terraform AWS provider's attribute names.
7. **Features ship with tests.** Network behavior also ships with connectivity-matrix cases and an update to the fidelity table (ARCHITECTURE §5.5). Learning features ship with a lab.
8. **No AWS logos or trade dress, no telemetry, no account requirement.**
9. **Decisions change only through a superseding ADR.** If code and an Accepted ADR disagree, that's a bug in one of them; say so rather than quietly deviating.

## Go conventions

- Pin the toolchain in `go.mod`. `gofmt` and `golangci-lint run` must pass.
- **`CGO_ENABLED=0` always.** Use the pure-Go SQLite driver; no cgo dependencies.
- Wrap errors with `%w` and context. No panics outside `main` and genuinely impossible states. Every user-visible error carries an AWS-style code (ADR-0008).
- Log with `log/slog`, structured, including resource IDs and generations. No `fmt.Println` in library code.
- `context.Context` is the first parameter and its cancellation is respected. `context.Background()` appears only in `main` and tests.
- **Only `internal/network/netns` may switch network namespaces.** A thread that switched namespace is never returned to the scheduler. Bind netlink handles to namespace handles.
- **Keep the pure packages pure:** `internal/semantics`, `internal/explain`, and the renderer in `internal/network/firewall` do no I/O and use no clock or randomness. They are covered by table and golden tests.
- Respect the layering in ARCHITECTURE §13. In particular, `internal/network` and `internal/compute` never import `internal/service` or `internal/apiserver`.
- Edit `api/openapi.yaml` first, then run `make generate`. Never hand-edit generated files; CI checks they're current.
- Linux interface names are at most 15 characters and come from database-allocated short indexes (ARCHITECTURE §4).
- Tests are table-driven. Golden files update with `-update`. Integration and e2e tests use the `integration` and `e2e` build tags. A skipped test must say why; never let a skip look like a pass.
- Keep files focused. A file past roughly 500 lines, or with two responsibilities, wants splitting.

## Web conventions

- TypeScript in strict mode, with API types generated by `make generate`. No `any`.
- TanStack Query owns server state, invalidated by server-sent events. No ad hoc `fetch` in components.
- Status is shown with an icon and text, never color alone.
- Nephos's own design tokens. No AWS icons or branding.
- Vitest for unit tests, Playwright for smoke tests.

## Development environment

- The maintainer works on **Windows with WSL2 (Ubuntu) and Docker Desktop**. Run every build and test **inside WSL**, not in PowerShell.
- Keep the working copy on the **WSL ext4 filesystem** (for example `~/src/nephos`), not under `/mnt/f`. The Windows mount is slow and mangles permissions and line endings.
- Native Linux with Docker Engine behaves the same and is the primary CI target.
- Commands (from M0 onward): `make build`, `make test`, `make lint`, `make generate`, `make web`, `make appliance`, `make e2e`.
- Integration and e2e tests need Docker and a privileged container.

## Git and pull requests

- Conventional Commits: `feat(network): …`, `fix:`, `docs:`, `test:`, `refactor:`, `chore:`.
- **DCO sign-off is required:** `git commit -s`.
- Work on a branch. Don't commit or push unless asked.
- A pull request states what changed, which milestone it serves, which acceptance criteria it advances, and which tests cover it.
- Update the ROADMAP checkboxes and the CHANGELOG in the same pull request.

## Before adding a feature, answer these

1. **Which milestone does it belong to?** If none, it belongs in the ROADMAP backlog. Say so instead of building it.
2. **Does it need an ADR?** New resource types, new runtime dependencies, changed interfaces, and changed host implementations do.
3. **What AWS behavior does it reproduce, and how does it deviate?** Update ARCHITECTURE §5.5.
4. **Which tests pin it down?** Unit, golden, a matrix case, a lab.
5. **Does it cross a security boundary** (ARCHITECTURE §9)? Then update RISKS.md.

## Glossary: Nephos, AWS, Linux

| Nephos | AWS equivalent | On the host |
|---|---|---|
| Appliance | (the region's infrastructure) | One privileged container with its own namespaces |
| Workspace | Account | A database scope with its own default VPC and quotas |
| `nx-edge` | The internet | A network namespace with the learner vantage point, test endpoints, and the uplink |
| My IP (198.51.100.10) | Your home IP address | The source address of every learner connection |
| VPC | VPC | A network namespace acting as the implicit router |
| Subnet | Subnet | Logical: a gateway address plus an nftables interface set |
| ENI | Elastic network interface | A veth pair with proxy ARP and a /32 route |
| Route table | Route table | A Linux routing table plus `ip rule` entries per interface |
| Security group | Security group | Stateful nftables chains per interface |
| Network ACL | Network ACL | Stateless nftables chains per subnet, at subnet boundaries |
| Instance | EC2 instance | A Podman system container (systemd, sshd, cloud-init) in its own user namespace |
| AMI | AMI | An OCI image |
| Serial console | EC2 Serial Console | A container exec session that bypasses the network |

## Don't

- Don't implement AWS API endpoints, SigV4, or SDK compatibility.
- Don't use Docker or Podman networks to model VPCs and subnets.
- Don't add a second source of truth (config files, caches that outlive a request).
- Don't add access paths that bypass the simulated network.
- Don't introduce cgo, AGPL dependencies, or telemetry.
- Don't write outside `~/.nephos` (CLI) and the `nephos-data` volume (appliance).
- Don't run `nephos up`, `git commit`, or `git push` on the user's machine unless asked.
