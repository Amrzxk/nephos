# ADR-0011: Apache-2.0 license with DCO sign-off

- **Status:** Accepted
- **Date:** 2026-09-17

## Context

Nephos is a serious open source and portfolio project. We want universities, bootcamps, employers, and individuals to adopt, host, fork, and contribute to it without legal friction. Recent relicensing of well-known infrastructure projects (to BSL and similar) has made contributors wary of projects that can change their license. Vyomi, the closest learning-oriented tool, is source-available under BSL 1.1.

## Options considered

### Licenses

| Option | Pros | Cons |
|---|---|---|
| **Apache-2.0** | Permissive; explicit patent grant; the default for cloud-native projects; accepted by virtually every company and university | Anyone, including commercial lab platforms, may host Nephos without contributing back |
| **MIT** | Shortest and simplest | No explicit patent grant |
| **AGPL-3.0** | Hosted derivatives must publish their changes; protects a possible future hosted offering | Many companies ban AGPL; fewer corporate contributors |
| **MPL-2.0** | File-level copyleft middle ground | Uncommon in this ecosystem; mixing rules confuse contributors |
| **BSL or other source-available** | Maximum commercial control | Not open source; contradicts Nephos's "open by default" principle |

### Contribution terms

| Option | Pros | Cons |
|---|---|---|
| **DCO sign-off** (`git commit -s`) | Lightweight; used by Linux and many CNCF projects; no copyright assignment | Relicensing would need every contributor's consent |
| **CLA** | Allows future relicensing | Friction and distrust; legal overhead for a solo maintainer |
| **Nothing** | Zero friction | Weak provenance record |

## Decision

- License all Nephos code, documentation, and built-in labs under **Apache-2.0**. The `LICENSE` and `NOTICE` files are added in M0.
- Require **DCO sign-off** on every commit, enforced by a CI check. No CLA.
- Prefer permissive dependencies. GPL-licensed programs shipped in the appliance image (for example nftables, iproute2, crun) run as separate programs installed from Debian packages, whose sources are publicly available. AGPL components are avoided unless an ADR justifies them.
- Generate an SBOM and third-party license report for the appliance image and the web bundle (M8).

## Consequences

- Positive: maximum adoption; contributor trust (the license can't be quietly changed); simple compliance for users.
- Negative / costs:
  - Commercial platforms may host Nephos without giving back. Accepted.
  - Relicensing is practically impossible. Accepted, and a feature.
- Follow-ups:
  - A trademark and name-usage policy once the name is confirmed ([RISKS](../RISKS.md) P3).
  - CI license scanning for Go and npm dependencies (M8).

## Validation

- The license scan passes on every release from M8 onward.
- Any future change of license requires a superseding ADR and consent from all contributors.
