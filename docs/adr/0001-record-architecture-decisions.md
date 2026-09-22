# ADR-0001: Record architecture decisions

- **Status:** Accepted
- **Date:** 2026-09-17

## Context

Nephos makes several decisions that are expensive to reverse: how instances run, how VPC networking is implemented on the host, how the appliance is packaged, and the license. The project starts with a solo maintainer, expects outside contributors, and uses AI coding sessions that need durable context. Without a written record, decisions get re-argued and their reasoning is lost.

## Options considered

### Option A: No formal records; rely on commit messages and issues
- Pros: zero overhead.
- Cons: reasoning scatters across tools; rejected alternatives are never written down, so they get proposed again.

### Option B: One large design document
- Pros: everything in one place.
- Cons: hard to tell what changed and when; no clear status per decision.

### Option C: Architecture Decision Records in the repository
- Pros: one small file per decision with explicit status; reviewed in PRs next to the code; easy for humans and AI sessions to find.
- Cons: some discipline is needed to keep them current.

## Decision

Keep Architecture Decision Records in `docs/adr/`, using the template and rules in [README.md](README.md). Architecture overviews ([ARCHITECTURE.md](../ARCHITECTURE.md)) summarize the current design and link to the ADRs that justify it.

## Consequences

- Positive: new contributors can learn *why* the system looks the way it does; superseded ADRs keep the history.
- Negative / costs: every significant change needs a short extra document.
- Follow-ups: [AGENTS.md](../../AGENTS.md) instructs Codex sessions to read the relevant ADRs before changing an area, and to propose a superseding ADR instead of silently deviating.

## Validation

Revisit if ADRs stop being updated. The warning sign is code that contradicts an Accepted ADR with no superseding record.
