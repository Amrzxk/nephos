# Codex Environment Migration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make Nephos a portable, Codex-only development repository with one concise instruction entry point and no legacy assistant references.

**Architecture:** Keep always-on repository guidance in the root `AGENTS.md` and route detailed questions to the existing canonical documentation. Remove the legacy instruction file and migrate references without adding project-level Codex settings, skills, hooks, or product changes.

**Tech Stack:** Markdown, Go and YAML comments, POSIX shell verification

**Spec:** `docs/superpowers/specs/2026-09-22-codex-environment-migration-design.md`

## Global Constraints

- Do not change application behavior, public interfaces, build targets, CI behavior, architecture decisions, roadmap scope, or contributor platform support.
- Preserve all pre-existing working-tree changes, especially `spikes/sp3-routed-vpc/main.go` and `spikes/sp3-routed-vpc/probe.go`.
- Keep repository guidance portable across supported contributor platforms.
- Do not add `.codex/config.toml`, repository skills, hooks, or nested instruction files.
- Do not commit or push; repository instructions require explicit authorization.

## Review Focus

- Dirty worktree: unrelated edits must remain byte-for-byte unchanged; compare their before and after Git diffs.
- Legacy terminology: case-insensitive searches must find no old assistant filename, product name, or vendor name in tracked content.
- Instruction discovery: the repository must have one root `AGENTS.md`, no competing root instruction file, and stay below the 32 KiB default limit.
- Documentation navigation: every migrated relative Markdown link must resolve to the root `AGENTS.md` from its containing directory.
- Behavioral scope: the only Go and YAML changes may be comment-only filename references; no executable token may change.

---

### Task 1: Establish the canonical Codex instruction entry point

**Files:**
- Modify: `AGENTS.md`
- Delete: legacy root assistant instruction file

**Interfaces:**
- Consumes: canonical project facts from `docs/VISION.md`, `docs/ROADMAP.md`, `docs/ARCHITECTURE.md`, `docs/RISKS.md`, `docs/LABS.md`, `docs/DEVELOPMENT.md`, `docs/adr/`, and `CONTRIBUTING.md`
- Produces: one always-loaded repository guide that routes future agents to those canonical sources

- [x] **Step 1: Record the pre-existing unrelated changes**

Run:

```bash
git diff --binary -- spikes/sp3-routed-vpc/main.go spikes/sp3-routed-vpc/probe.go
```

Expected: the existing atomic-replacement sampling and `AttemptCount` edits are visible and can be compared after the migration.

- [x] **Step 2: Rewrite `AGENTS.md` as the concise repository entry point**

Keep these sections, in this order:

```text
Purpose and scope
Current milestone
Start-of-task workflow
Documentation routing table
Non-negotiable principles
M0 constraints carried into M1/M2
Go conventions
Web conventions
Development and verification
Git and pull requests
Prohibited actions
```

The start-of-task workflow must require `git status`, milestone confirmation,
area-specific documentation, scope-impact checks, proportional verification,
and preservation of unrelated changes. Detailed design prose and the glossary
remain canonical in the existing documents rather than being copied into the
instruction file.

- [x] **Step 3: Remove the legacy root instruction file**

Delete only the superseded root instruction file. Preserve `AGENTS.md` as the
single instruction source.

- [x] **Step 4: Verify instruction discovery constraints**

Run:

```bash
test -f AGENTS.md
legacy_file="$(printf '%s%s' 'CLAU' 'DE.md')"
test ! -f "$legacy_file"
test "$(wc -c < AGENTS.md)" -lt 32768
find . -path './.git' -prune -o \( -name 'AGENTS.md' -o -name 'AGENTS.override.md' \) -print
```

Expected: all commands succeed and `find` reports only `./AGENTS.md`.

### Task 2: Migrate repository references and validate the result

**Files:**
- Modify: `.golangci.yml`
- Modify: `CHANGELOG.md`
- Modify: `CONTRIBUTING.md`
- Modify: `README.md`
- Modify: `cmd/nephosd/main.go` (comment only)
- Modify: `docs/ARCHITECTURE.md`
- Modify: `docs/DEVELOPMENT.md`
- Modify: `docs/RISKS.md`
- Modify: `docs/adr/0001-record-architecture-decisions.md`
- Modify: `docs/spikes/SP1-appliance-nested-containers.md`
- Verify: `docs/superpowers/specs/2026-09-22-codex-environment-migration-design.md`

**Interfaces:**
- Consumes: the canonical root `AGENTS.md` created by Task 1
- Produces: valid Codex-oriented documentation links and comments throughout the repository

- [x] **Step 1: Replace prose and links with `AGENTS.md` references**

Use `AGENTS.md`, `../AGENTS.md`, or `../../AGENTS.md` according to the containing
file's directory. In prose, use “Codex sessions” or “agent sessions” only where
the specific tool matters; retain contributor-neutral wording elsewhere.

- [x] **Step 2: Update non-documentation comments without changing behavior**

Change only the instruction filename in `.golangci.yml` comments and the comment
in `cmd/nephosd/main.go`. Do not alter linter settings or Go tokens.

- [x] **Step 3: Prove legacy references are gone**

Run:

```bash
legacy_product="$(printf '%s%s' 'clau' 'de')"
legacy_vendor="$(printf '%s%s' 'anthro' 'pic')"
if git grep -n -i -E "$legacy_product|$legacy_vendor" -- .; then exit 1; fi
```

Expected: no output and overall exit status 0.

- [x] **Step 4: Verify all migrated Markdown links resolve**

Run:

```bash
test -f AGENTS.md
test -f docs/../AGENTS.md
test -f docs/adr/../../AGENTS.md
test -f docs/spikes/../../AGENTS.md
```

Expected: all commands exit 0.

- [x] **Step 5: Verify formatting and behavioral scope**

Run:

```bash
git diff --check
git diff --name-status
git diff -- cmd/nephosd/main.go .golangci.yml
```

Expected: `git diff --check` succeeds; the file list matches this plan plus the
pre-existing spike edits; and the Go/YAML diff changes comments only.

- [x] **Step 6: Confirm the unrelated spike edits were preserved**

Run:

```bash
git diff --binary -- spikes/sp3-routed-vpc/main.go spikes/sp3-routed-vpc/probe.go
```

Expected: output matches the diff recorded in Task 1 Step 1.

- [x] **Step 7: Run the relevant repository checks**

Run:

```bash
make test
make lint
```

Expected: unit tests pass and `golangci-lint` reports no issues. If a required
tool is unavailable, record the exact missing tool instead of modifying the
environment or project dependencies.
