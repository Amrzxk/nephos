# Changelog

All notable changes to Nephos are recorded here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and
Nephos aims to follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html)
from v0.1.0 onward. Until then, `/v1` of the API may change incompatibly
([ADR-0008](docs/adr/0008-rest-openapi-api-not-aws-compatible.md)).

Each entry names the milestone it belongs to; see [docs/ROADMAP.md](docs/ROADMAP.md).

## [Unreleased]

### Added

- **M0 — repository scaffold.** Go module pinned to go1.27.1, the `nephos`,
  `nephosd`, and `nephos-hook` binaries, `internal/version` with linker-injected
  build identity, and a `Makefile` covering build, test, lint, and the
  cross-build matrix.
- **M0 — linting that enforces the conventions.** `.golangci.yml` mechanises the
  rules in `CLAUDE.md`: `fmt.Print*` and `context.Background()` are banned
  outside `cmd/` and tests, and `depguard` encodes the
  [ARCHITECTURE §13](docs/ARCHITECTURE.md#13-repository-structure) layering and
  the purity of `explain`, `semantics`, and the firewall renderer.
- **M0 — CI skeleton.** Lint, unit and race-detector tests, generated-code
  freshness, a `govulncheck` scan, cross-builds for linux, darwin, and windows
  on amd64 and arm64, and a DCO sign-off check.
- **M0 — contributor documentation.** `CONTRIBUTING.md`, `SECURITY.md`,
  `CODE_OF_CONDUCT.md`, issue and pull request templates including a fidelity-bug
  form, and `docs/DEVELOPMENT.md`.

### Notes

- Nephos is still pre-code in every functional sense: no API, no CLI commands
  beyond `version`, no database, and no networking. M1 is the first milestone
  that does something ([docs/ROADMAP.md](docs/ROADMAP.md#m1-thinnest-end-to-end-slice-two-instances-ping)).

[Unreleased]: https://github.com/Amrzxk/nephos/commits/main
