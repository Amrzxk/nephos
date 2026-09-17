# Contributing to Nephos

Thank you for considering it. Nephos is a learning tool, so the bar is slightly
unusual: **correct beats complete**. A feature that teaches a wrong mental model
is worse than a missing one.

Start with [CLAUDE.md](CLAUDE.md). It is written for AI coding sessions but it is
the shortest accurate description of the conventions, and it applies to humans
unchanged.

## Developer Certificate of Origin

Nephos requires a [DCO](https://developercertificate.org/) sign-off on every
commit. There is no CLA ([ADR-0011](docs/adr/0011-apache-2-license-with-dco.md)).

```bash
git commit -s
```

That appends `Signed-off-by: Your Name <your@email>`, which certifies that you
wrote the patch or have the right to submit it. The name and address must match
the commit author. If you forget on a branch you have already written:

```bash
git rebase --signoff main
```

CI checks this on every commit in a pull request.

## Before you write code

Nephos is planned in milestones, and scope discipline is the main thing keeping a
solo-maintained project alive. Before adding a feature, answer these — the same
five questions CLAUDE.md asks:

1. **Which milestone does it belong to?** If none, it belongs in the
   [ROADMAP](docs/ROADMAP.md) backlog. Say so in an issue instead of building it.
2. **Does it need an ADR?** New resource types, new runtime dependencies,
   changed interfaces, and changed host implementations do.
3. **What AWS behaviour does it reproduce, and how does it deviate?** Update the
   fidelity table in [ARCHITECTURE §5.5](docs/ARCHITECTURE.md#55-aws-fidelity-and-known-deviations)
   in the same pull request.
4. **Which tests pin it down?** Unit, golden, a connectivity-matrix case, a lab.
5. **Does it cross a security boundary** ([ARCHITECTURE §9](docs/ARCHITECTURE.md#9-security-model))?
   Then update [RISKS.md](docs/RISKS.md).

Decisions change only through a superseding ADR. If the code and an Accepted ADR
disagree, that is a bug in one of them — say which, rather than quietly
deviating.

## Setting up

See [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md). The short version:

- Linux, or Windows with WSL2 and Docker Desktop. Run everything **inside** WSL.
- Keep the working copy on the **WSL ext4 filesystem** (`~/src/nephos`), not
  under `/mnt/c` or `/mnt/f`. The Windows mount is slow and mangles permissions
  and line endings.
- Go as pinned in `go.mod`, plus Docker. `make lint` needs `golangci-lint`.

```bash
make build     # all three binaries
make test      # unit and golden tests
make test-race # the same, under the race detector
make lint      # golangci-lint
make ci        # everything a pull request runs
```

## The rules that are not negotiable

These outrank convenience, cleverness, and speed. The full list is in
[CLAUDE.md](CLAUDE.md); the ones people trip over:

- **Never fake silently.** If a rule cannot be enforced, the resource goes to
  `failed` with a reason, or the appliance refuses to start. Nothing is ever
  "stored but not enforced".
- **The database is the only source of truth.** Kernel and runtime state must be
  rebuildable from it, and every engine operation is idempotent.
- **Never touch the host.** No host network namespace, no host firewall rules,
  no host mounts, no global sysctls, no kernel module loading, and never
  `--network host` or `--pid host`.
- **All learner traffic enters through `nx-edge`.** Never add port publishing or
  exec shortcuts that bypass security groups, NACLs, and routes. The serial
  console is the single, clearly labelled exception.
- **`CGO_ENABLED=0`** for anything shipped. (`make test-race` sets `CGO_ENABLED=1`
  deliberately, because the race detector needs it and a test run is not an
  artifact.)
- **Only `internal/network/netns` may switch network namespaces**, and a thread
  that switched namespace is never returned to the scheduler.
- **Keep the pure packages pure:** `internal/semantics`, `internal/explain`, and
  the renderer in `internal/network/firewall` do no I/O and use no clock or
  randomness.
- **Edit `api/openapi.yaml` first**, then run `make generate`. Never hand-edit
  generated files.

## Commits and pull requests

[Conventional Commits](https://www.conventionalcommits.org/):

```
feat(network): enforce NACL rules at subnet boundaries
fix(store): retry the migration on a locked database
docs: explain the NAT gateway route dependency
test(e2e): add the stateless-return matrix cases
refactor(service): extract CIDR validation
chore(m0): pin the golangci-lint version
```

Work on a branch. A pull request states:

- what changed;
- which milestone it serves;
- which acceptance criteria it advances;
- which tests cover it.

Update the [ROADMAP](docs/ROADMAP.md) checkboxes and the
[CHANGELOG](CHANGELOG.md) in the same pull request.

## Tests

- Table-driven, with golden files updated by `make test-update`.
- Integration and end-to-end tests use the `integration` and `e2e` build tags and
  need Docker and a privileged container.
- Network behaviour also ships with connectivity-matrix cases and an update to
  the fidelity table.
- Learning features ship with a lab.
- **A skipped test must say why.** Never let a skip look like a pass.

## The easiest ways to help

- **Fidelity bugs.** If you know AWS well and Nephos gets something wrong, that
  is the single most valuable report there is. There is an issue template for it.
- **Labs.** Authoring a lab needs no Go ([docs/LABS.md](docs/LABS.md)).
- **Documentation**, especially anything that confused you on your first run.

## Reporting security problems

Privately, never as a public issue. See [SECURITY.md](SECURITY.md).

## Code of conduct

By participating you agree to the [Code of Conduct](CODE_OF_CONDUCT.md).

## Licence

Nephos is Apache-2.0. Your contributions are licensed under it.
