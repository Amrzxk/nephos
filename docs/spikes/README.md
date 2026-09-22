# Spike reports

M0's spikes exist to test the riskiest assumptions in the design **before** M1
writes code against them ([ROADMAP](../ROADMAP.md#m0-foundations-and-spikes)).

The code under [`spikes/`](../../spikes/) is throwaway and lives in its own Go
module. **These reports are the deliverable.**

| Spike | Result | Validates | Mitigates |
|---|---|---|---|
| [SP1 — appliance and nested system containers](SP1-appliance-nested-containers.md) | **PASS** 19/19 | [ADR-0003](../adr/0003-appliance-container-packaging.md), [ADR-0004](../adr/0004-instances-as-system-containers.md) | T1, T2, T5, T10, S2 |
| [SP2 — ENI plumbing through an OCI hook](SP2-eni-plumbing-hook.md) | **PASS** 18/18 | [ADR-0004](../adr/0004-instances-as-system-containers.md) R3 | T1 |
| [SP3 — the routed VPC network plane](SP3-routed-vpc-plane.md) | **PASS** 21/21 | [ADR-0005](../adr/0005-nephos-owned-routed-network-plane.md), [ADR-0002](../adr/0002-go-for-control-plane-and-cli.md) | T3, T8 |
| [SP4 — cloud-init against a Nephos IMDS](SP4-cloud-init-imds.md) | **PASS** 16/16 | supports M2 | T2 |

All four pass on both **Windows 10 with WSL2 and Docker Desktop** and
**Ubuntu 24.04 with native Docker**. The native result is preserved in
[`Spikes` run 35698867324](https://github.com/Amrzxk/nephos/actions/runs/35698867324).

The first native run ([35290287254](https://github.com/Amrzxk/nephos/actions/runs/35290287254))
found a harness timing issue rather than a platform failure: SP3 completed 100
ruleset replacements so quickly that only 36 probe attempts landed in the
window. All 36 remained denied, but the sample-density assertion required more
than 50. SP3 now keeps replacing until it has completed at least 100
replacements and at least 50 probes, with a 1,000-replacement safety cap. The
passing rerun completed 189 replacements and 56 probes with zero leaks.
The same patch also passed SP3 locally in the privileged WSL2 appliance: 21/21
assertions, 100 replacements, 88 probes, and zero leaks.

No fallback was needed: ADR-0004's containerd path stays unused, and ADR-0005's
routed design is confirmed. ADR-0004's capability wording was amended in place
(a text correction, not a decision change).

## How to read these

Every assertion cites the ADR validation criterion or RISKS item it serves, and
carries the measurement beside it. A failing assertion is a **result**, not an
error — a spike that fails honestly is worth more than one that passes vaguely.

Reproduce any of them with:

```bash
./spikes/run.sh sp1     # or sp2, sp3, sp4, all
```

## What they changed

Six findings feed into M1 and M2. Each produced a plausible-but-wrong result
first, which is the whole argument for spiking:

1. **cgroup v2 controllers must be delegated** to a leaf cgroup, and the
   appliance must refuse to start if `memory`, `pids`, or `cpu` cannot be —
   otherwise instance-type limits enforce nothing while the API reports them.
2. **`CAP_NET_ADMIN` must be added explicitly** (ADR-0004 amended).
3. **AMIs need `APT::Sandbox::User "root"`**, or every `apt` command fails under
   `userns=auto`.
4. **Readiness must not be able to lie.** `ssh.socket` binds port 22 instantly,
   so a port check reported a healthy instance whose `sshd` had failed.
5. **The IMDS must serve dated API versions**, not only `/latest`, or cloud-init
   silently falls back to `DataSourceNone` and still reports success.
6. **`ds-identify` disables cloud-init in containers** unless its policy is
   forced, and cloud-init merges config lists by *appending*.
