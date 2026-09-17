# Architecture Decision Records

This folder records the significant decisions behind Nephos: what we chose, what we rejected, and why. The format is a lightweight version of [MADR](https://adr.github.io/madr/).

## Index

| ADR | Title | Status |
|---|---|---|
| [0001](0001-record-architecture-decisions.md) | Record architecture decisions | Accepted |
| [0002](0002-go-for-control-plane-and-cli.md) | Go for the control plane and CLI | Accepted |
| [0003](0003-appliance-container-packaging.md) | Package Nephos as an appliance container | Accepted |
| [0004](0004-instances-as-system-containers.md) | Instances are system containers run by Podman | Accepted; engine validated in M0 |
| [0005](0005-nephos-owned-routed-network-plane.md) | A Nephos-owned, routed network plane | Accepted; validated in M0 |
| [0006](0006-learner-access-through-simulated-internet.md) | Learner access through a simulated internet edge | Accepted |
| [0007](0007-sqlite-state-and-reconciliation.md) | SQLite desired state with reconcilers | Accepted |
| [0008](0008-rest-openapi-api-not-aws-compatible.md) | REST + OpenAPI API, not AWS wire compatible | Accepted |
| [0009](0009-web-console-react-typescript.md) | Web console in React and TypeScript | Accepted |
| [0010](0010-lab-format-yaml-cel-probes.md) | Lab format: YAML, CEL, and live probes | Accepted |
| [0011](0011-apache-2-license-with-dco.md) | Apache-2.0 license with DCO sign-off | Accepted |

## Rules

- **One decision per ADR.** Number them sequentially and never reuse a number.
- **Accepted ADRs are immutable.** To change a decision, write a new ADR that supersedes the old one, and set the old one's status to `Superseded by ADR-NNNN`. Typos and broken links can be fixed in place.
- **Write an ADR when a change** alters a public interface (API, CLI, lab format), adds a runtime dependency to the appliance, changes how a cloud concept is implemented on the host, or reverses anything in this index.
- **"Validated in M0"** means a milestone spike must confirm the technical assumptions. If a spike fails, the ADR's fallback applies and a superseding ADR records it.

## Template

Copy this into `NNNN-short-title.md`:

```markdown
# ADR-NNNN: Title

- **Status:** Proposed | Accepted | Superseded by ADR-NNNN
- **Date:** YYYY-MM-DD

## Context

What forces are at play? What problem does this decision solve?

## Options considered

### Option A: name
- Pros:
- Cons:

### Option B: name
- Pros:
- Cons:

## Decision

What we chose, stated as an instruction.

## Consequences

- Positive:
- Negative / costs:
- Follow-ups:

## Validation

How we will know the decision holds, and what would make us revisit it.
```
