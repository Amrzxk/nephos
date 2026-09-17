# ADR-0002: Go for the control plane and CLI

- **Status:** Accepted
- **Date:** 2026-09-17

## Context

The Nephos backend must:

- manipulate Linux networking through netlink, network namespaces, and nftables;
- drive a container runtime and handle OCI hooks;
- run many concurrent small servers (API, per-VPC DNS and instance metadata, probes, terminals);
- ship a CLI that runs natively on Linux, macOS, and Windows;
- later power a Terraform provider.

The project starts with one maintainer and wants contributors from the cloud-native world.

## Options considered

### Option A: Go
- Pros:
  - The ecosystem this project needs is written in Go: `vishvananda/netlink` and `netns` (used by CNI plugins), Podman, containerd, and the Docker SDK.
  - Other needed libraries exist in Go: `cel-go`, `miekg/dns`, `cobra`, and `terraform-plugin-framework`, which is Go-only.
  - Static binaries and trivial cross-compilation (with a pure-Go SQLite driver).
  - Goroutines fit the many-small-servers shape.
  - Familiar to cloud and DevOps contributors, and matches the maintainer's leaning.
- Cons:
  - Network namespaces are per OS thread, so goroutines can migrate across namespaces unless threads are locked carefully.
  - Verbose error handling.

### Option B: Rust
- Pros: memory safety, performance, good netlink and nftables crates (fakecloud is written in Rust).
- Cons:
  - Slower iteration for a solo maintainer.
  - Smaller pool of cloud-ops contributors.
  - The Terraform provider would still need Go.
  - The container runtime client ecosystem is less mature.

### Option C: Python
- Pros: fastest prototyping; `pyroute2` is excellent; many learners read Python.
- Cons:
  - Distributing a CLI to three operating systems is painful.
  - Weaker fit for many long-running concurrent servers.
  - The Terraform provider would still need Go.

### Option D: TypeScript (Node.js)
- Pros: the same language as the web console.
- Cons: weak systems libraries for netlink and namespaces; unusual for infrastructure tooling.

## Decision

Write `nephosd` (control plane), `nephos` (CLI), `nephos-hook` (OCI hook), and the future Terraform provider in **Go**. Only the web console uses TypeScript ([ADR-0009](0009-web-console-react-typescript.md)).

Build rules:

- **CGO-free builds.** Use a pure-Go SQLite driver so release tooling can cross-compile every target.
- **Pinned toolchain.** Pin the Go toolchain version in `go.mod` and in CI.
- **One place for namespace switching.** All network namespace switching goes through `internal/network/netns`, which locks OS threads and restores the original namespace. No other package calls `setns`.

## Consequences

- Positive: one backend language; direct use of the libraries Podman and CNI plugins rely on; single-binary distribution.
- Negative / costs: namespace/thread bugs are subtle, so that package needs dedicated tests (run with the race detector) and a documented usage pattern.
- Follow-ups: M0 sets up `golangci-lint`, a CGO-disabled build check, and `log/slog` structured logging conventions.

## Validation

- M0 spikes are written in Go and must plumb interfaces and open sockets inside other network namespaces without leaking threads into the wrong namespace (verified by a stress test).
- Revisit only if a required capability has no workable Go implementation.
