<!--
Thank you for contributing. Every commit needs a DCO sign-off (git commit -s);
see CONTRIBUTING.md.
-->

## What changed

<!-- One or two sentences. What does this do that the tree did not do before? -->

## Milestone

<!-- Which milestone in docs/ROADMAP.md does this serve? If none, say so: it may
     belong in the backlog instead. -->

## Acceptance criteria advanced

<!-- Quote the ROADMAP checkbox(es) this moves toward, and say whether they are
     now fully met or only partly. -->

## Tests

<!-- Which tests pin this down? Unit, golden, a connectivity-matrix case, a lab,
     e2e. Paste the relevant output if it helps. -->

## Checklist

- [ ] Every commit is signed off (`git commit -s`)
- [ ] `make ci` passes locally
- [ ] Tests cover the change, and any skipped test says why
- [ ] `docs/ROADMAP.md` checkboxes and `CHANGELOG.md` updated

If this changes **network behaviour**:

- [ ] The fidelity table in [ARCHITECTURE §5.5](../docs/ARCHITECTURE.md#55-aws-fidelity-and-known-deviations) is updated in this PR
- [ ] Connectivity-matrix cases added, each citing the AWS behaviour it reproduces
- [ ] `explain` still agrees with `probe` on every matrix case

If this changes a **decision, interface, resource type, or runtime dependency**:

- [ ] A new or superseding ADR is included (`docs/adr/`)

If this crosses a **security boundary** ([ARCHITECTURE §9](../docs/ARCHITECTURE.md#9-security-model)):

- [ ] [`docs/RISKS.md`](../docs/RISKS.md) is updated
- [ ] Nothing bypasses security groups, NACLs, or routes
- [ ] Nothing touches the host namespace, host firewall, or global sysctls
