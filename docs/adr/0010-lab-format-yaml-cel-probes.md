# ADR-0010: Lab format: YAML, CEL, and live probes

- **Status:** Accepted
- **Date:** 2026-09-17

## Context

Guided labs are Nephos's core learning feature. Labs must:

- be writable by educators and contributors who don't write Go;
- verify **behavior** (traffic actually flows or is blocked), not just configuration;
- resist trivial shortcuts, such as putting everything in a public subnet;
- vary enough to be replayable, yet stay deterministic for testing;
- be tested in CI so they never rot;
- run safely.

## Options considered

### Option A: Declarative YAML, built-in check types, and CEL expressions
- Pros:
  - Readable and reviewable.
  - Can be validated statically with a schema and compiled expressions.
  - Behavioral probes come built in.
  - CEL is sandboxed, non-Turing-complete, and cost-limited, and it is familiar from Kubernetes validation rules.
  - Labs can be tested automatically.
- Cons: the check vocabulary must be designed up front and extended deliberately.

### Option B: Shell scripts per step (setup, check, solve), as in Instruqt or Killercoda
- Pros: fastest to start; unlimited flexibility.
- Cons: brittle; no static validation; checks drift toward testing configuration; harder to sandbox.

### Option C: Labs as Go code
- Pros: type-safe and powerful.
- Cons: only Go developers can author labs, and every new lab needs a Nephos release.

### Option D: Markdown runbooks with embedded check blocks
- Pros: prose and checks live side by side.
- Cons: fragile parsing, and logic mixed into narrative.

## Decision

Use **declarative YAML with built-in checks and CEL**. The full specification is in [LABS.md](../LABS.md).

- **A lab is a directory:** `lab.yaml` (`schema_version: v1alpha1`, `kind: Lab`), plus optional Markdown files and a `scripts/` folder.
- **`lab.yaml` sections:** `metadata`, `environment` (resources to create), `faults` (seeded variants), `objectives` (with checks), `hints`, `solution` (actions per variant), and `debrief`.
- **Check types:**

  | Type | What it does |
  |---|---|
  | `probe` | Real TCP, UDP, ICMP, or HTTP traffic from a vantage point: `internet`, `my_ip`, or an instance |
  | `assert` | A CEL expression over the workspace's resource graph, with helpers including `explain()` |
  | `exec` | A command run inside an instance through the console channel |
  | `script` | A sandboxed escape hatch |
  | `all`, `any`, `not` | Combinators |

  Every check supports `stable_for` and `timeout`.
- **Anti-shortcut rules:** each lab pairs positive objectives with negative ones ("still not reachable from the internet") and constraint assertions.
- **Determinism:** fault variants are chosen with a seed recorded on the lab attempt, so `nephos lab reset` reproduces the same scenario.
- **Testing:** `nephos lab test <dir>` is required in CI for every lab. It validates the schema, compiles every CEL expression, and then, for each fault variant:
  1. applies the environment and the fault;
  2. asserts that objectives marked `initially: fail` fail;
  3. applies the solution;
  4. asserts that every objective passes;
  5. tears down and checks for leaks.

## Consequences

- Positive:
  - Labs double as end-to-end regression tests of network behavior.
  - Educators can contribute without Go.
  - Lab quality is enforced mechanically.
- Negative / costs:
  - The engine needs a probe executor, a CEL environment, workspace isolation, and a schema that evolves carefully.
  - `v1alpha1` may change until the classroom milestone (M15). Each lab declares the minimum Nephos version it needs.

## Validation

M7 acceptance criteria:

- The five MVP labs are written in YAML with at most one `script` check among them, and all pass `nephos lab test` in CI.
- Someone who didn't write labs 1–3 can complete them using only the lab text and the console.
