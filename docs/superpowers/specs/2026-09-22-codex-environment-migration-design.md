# Codex environment migration design

**Date:** 2026-09-22
**Status:** Approved

## Purpose

Make Nephos a Codex-only development repository without changing the product,
architecture, build behavior, tests, or current implementation work. Codex
should receive concise, durable guidance at startup and should be able to find
the canonical project document for any task without relying on legacy
assistant-specific files or terminology.

## Scope

- Make the root `AGENTS.md` the sole repository instruction entry point.
- Delete the legacy assistant instruction file.
- Replace every tracked legacy-assistant reference with the corresponding Codex
  or `AGENTS.md` reference.
- Improve `AGENTS.md` as a task router and guardrail document while preserving
  the project's established architecture, milestone scope, conventions, and M0
  findings.
- Preserve all unrelated working-tree changes.

The migration does not change application code, public interfaces, build
targets, CI behavior, architecture decisions, roadmap scope, or contributor
platform support.

## Instruction architecture

`AGENTS.md` is always loaded and therefore contains only guidance that applies
to every repository task:

- the project's purpose and current milestone;
- the required start-of-task workflow;
- a task-to-document routing table;
- non-negotiable architecture and safety constraints;
- implementation conventions that prevent recurrent mistakes;
- portable development and verification commands;
- Git and pull-request expectations;
- M0 findings that must constrain M1 and M2 implementation.

Detailed information remains canonical in the existing documentation:

- `docs/VISION.md` defines purpose and non-goals;
- `docs/ROADMAP.md` defines milestone scope and completion criteria;
- `docs/ARCHITECTURE.md` defines system design and implementation boundaries;
- `docs/adr/` records accepted decisions;
- `docs/RISKS.md` records technical and security risks;
- `docs/LABS.md` defines learning content;
- `docs/DEVELOPMENT.md` defines environment setup and commands;
- `CONTRIBUTING.md` defines the human contribution workflow.

No `.codex/config.toml` is added. Model choice, permissions, approval policy,
sandbox configuration, and machine paths are contributor preferences rather
than portable repository requirements. No repository skill or nested
`AGENTS.md` is added because the current guidance is always-on and no
specialized repeatable workflow yet warrants a lazy-loaded skill.

## Agent workflow

For every future task, the root instructions require an agent to:

1. inspect the working tree and preserve unrelated changes;
2. confirm the current milestone and keep work within it;
3. read only the documents routed for the affected area;
4. identify ADR, fidelity, risk, test, lab, roadmap, and changelog obligations;
5. implement within the established architecture;
6. run verification proportional to the change and report unavailable checks;
7. avoid commits, pushes, appliance startup, and destructive operations unless
   explicitly authorized.

## Reference migration

All links and textual references to the legacy instruction file will point to
`AGENTS.md` or use Codex-neutral wording. This includes repository
documentation, historical changelog prose, the architecture tree, ADR and spike
reports, source comments, and linter comments. The meaning of the referenced
rules will not change.

## Verification

The migration is complete when:

- tracked content contains no legacy assistant or vendor references;
- every local Markdown link changed by the migration resolves;
- `AGENTS.md` is below Codex's default 32 KiB combined instruction limit;
- `git diff --check` passes;
- application behavior is unchanged;
- pre-existing edits to `spikes/sp3-routed-vpc/main.go` and
  `spikes/sp3-routed-vpc/probe.go` remain untouched.
