# SP1: Appliance and nested system containers

- **Status:** **PASS** (19 of 19 assertions)
- **Date:** 2026-09-18
- **Validates:** [ADR-0003](../adr/0003-appliance-container-packaging.md) (appliance container packaging), [ADR-0004](../adr/0004-instances-as-system-containers.md) (instances as system containers)
- **Mitigates:** [RISKS](../RISKS.md) T1, T2, T5, T10, S2
- **Reproduce:** `./spikes/run.sh sp1`

## Question

Can a privileged appliance container run rootful Podman, and boot Ubuntu 24.04
system containers with systemd, sshd, and per-instance user namespaces — fast
enough, small enough, and isolated enough to be the basis for M1?

If not, ADR-0004's containerd fallback has to be taken before M1 starts.

## Environment

| | |
|---|---|
| Platform | Windows 10 + WSL2 + Docker Desktop (the maintainer's machine) |
| Kernel | 6.6.87.2-microsoft-standard-WSL2 |
| Docker | 28.3.2, cgroup v2, overlayfs |
| Host CPUs / memory | 4 / **1.9 GiB** |
| Appliance | Debian 13 (trixie), Podman 5.4.2, crun 1.21 |
| AMI | Ubuntu 24.04, systemd + openssh-server + cloud-init, 223 MB |

> The native-Linux leg of the M0 acceptance criterion has **not** run yet. The
> `Spikes` workflow (`workflow_dispatch`) exists for it but needs the branch
> pushed. See [Outstanding](#outstanding).

## Results

### Timing (ADR-0004 validation 1)

| Measurement | Result | Target | Verdict |
|---|---|---|---|
| First boot from a cached image | **3 743 ms** | < 10 s | PASS |
| Boot to sshd, median of 10 | **3 977 ms** | < 5 s | PASS |
| Boot to sshd, range | 3 666 – 4 744 ms | — | — |

Samples (ms): 3783, 3677, 4123, 4196, 3832, 3679, 4213, 3666, 4149, 4744.

**Read these with the caveat below.** SP1 instances run with `--network none`,
because giving an instance a network is [SP2](SP2-eni-plumbing-hook.md)'s job and
Nephos never uses a Podman network to model a VPC. cloud-init therefore cannot
reach a metadata service and sits out its `max_wait`, which dominates the
measurement — roughly 2.7 s of the ~4 s above is that timeout.

The realistic figure is [SP4](SP4-cloud-init-imds.md)'s **2 735 ms**, measured on
a full instance with cloud-init enabled *and* an IMDS reachable on its VPC
router. That is the configuration Nephos actually ships.

For reference, an earlier revision of this spike measured a median of 1 192 ms —
but that was with `ssh.service` failing and cloud-init disabled, so it was
timing a broken instance. See [finding 4](#4-sshd-needs-host-keys-and-socket-activation-makes-readiness-lie).

The first-boot figure is what [RISKS T10](../RISKS.md#t10-user-namespace-id-mapping-costs)
is about: the cost of ID-mapping image layers for a per-instance user namespace.
On this 6.6 kernel it is negligible — first boot is *faster* than the steady-state
median, so ID-mapped mounts are clearly being used rather than layers copied.

### Footprint (RISKS T5)

| Measurement | Result | Target | Verdict |
|---|---|---|---|
| 20 concurrent instances started | **20 of 20** | 20 | PASS |
| Total memory, 20 idle instances | **450 MB** (429 MiB) | < 2 GiB | PASS |
| Per instance | **~21 MiB** | < 60 MB (RISKS T5 warning line) | PASS |
| Storage after one instance | 579 MB total graph root | — | — |

Measured from each container's cgroup `memory.current`. `podman stats` reports
~6.3 MB per instance because it excludes inactive page cache; the 17 MiB figure
is the conservative one and is the one to plan capacity against.

Worth stating plainly: this ran on a host with **1.9 GiB of RAM**, below the 8 GB
[ARCHITECTURE §12](../ARCHITECTURE.md#12-platform-support) recommends, and 20
instances still fit. That is a stronger result than the target asked for.

### Behaviour (ADR-0004 validations 2–6)

| Criterion | Verdict | Evidence |
|---|---|---|
| systemd is PID 1 (R1) | PASS | `/proc/1/comm` = `systemd` |
| No failed units | PASS | 0 failed, after masking udev/logind/timesync |
| `systemctl start nginx` works | PASS | starts and reports active |
| apt works under `userns=auto` | PASS | see [the `_apt` trap](#the-_apt-setgroups-trap) |
| Instance root maps to non-zero appliance UID (validation 3) | PASS | uid **200000** |
| Instance root still sees uid 0, so `sudo` works | PASS | `id -u` = 0 |
| Each instance gets a distinct UID range (R2) | PASS | 200000 and 201024 |
| An instance cannot see another's processes (S2) | PASS | checked `ps` |
| An instance cannot reach the Podman socket or `/var/lib/nephos` (S2) | PASS | neither exists inside |
| Instance root can add an nftables rule in its own netns (validation 4) | PASS | needs `CAP_NET_ADMIN`, see below |
| Instance nftables state stays inside the instance | PASS | appliance ruleset unaffected |
| Root filesystem survives stop/start (R4) | PASS | marker file intact |

### Host safety (ADR-0003)

| Criterion | Verdict |
|---|---|
| Appliance does not share the host network or PID namespace | PASS (`network=bridge`, `pid=private`) |
| Appliance has no host bind mounts | PASS (one named volume only) |
| Required cgroup controllers delegated | PASS (see below) |

## Four findings that change the implementation

These are the reason the spike was worth running. Each one would have been a
confusing bug in M1.

### 1. Nested cgroup v2 delegation is required, and must fail closed

Podman inside the appliance failed outright:

```
Error: OCI runtime error: crun: controller `pids` is not available under
/sys/fs/cgroup/libpod_parent/libpod-<id>/cgroup.controllers
```

cgroup v2 forbids a cgroup from both holding processes and enabling controllers
for its children ("no internal processes"). The appliance's own processes start
at the root of its cgroup namespace, so Podman's children inherited a cgroup with
no controllers at all.

The fix, which kind and minikube both use, is in the appliance entrypoint: move
every process into a leaf cgroup, then write the controllers into
`cgroup.subtree_control`. All seven delegate on this kernel
(`cpuset cpu io memory hugetlb pids rdma`).

**This must fail closed.** Without `memory` and `pids` delegated, instance type
limits silently enforce nothing while the API still reports them — precisely
what [CLAUDE.md](../../CLAUDE.md) principle 2 forbids. The entrypoint now
refuses to start unless `memory`, `pids`, and `cpu` are all delegated.

**Carry into M1:** the real appliance entrypoint needs this, and
`nephos doctor` should report it.

### 2. `CAP_NET_ADMIN` must be added explicitly, and ADR-0004 is ambiguous

Instance root could not run `nft`:

```
Error: Could not process rule: Operation not permitted
```

Podman's default capability set omits `CAP_NET_ADMIN`. ADR-0004 says "default
capabilities and default seccomp profile", but its own R3 and validation 4
require instance root to run `ufw` in its network namespace, and
[RISKS S3](../RISKS.md#s3-kernel-attack-surface-through-instance-owned-network-namespaces)
explicitly accepts the resulting kernel attack surface. With `--cap-add NET_ADMIN`
(CapEff `0x800415fb`) it works, and the appliance's own ruleset stays untouched.

**Carry into M1:** ADR-0004's "default capabilities" wording should be amended
to "default capabilities plus `CAP_NET_ADMIN`", and the hardened-instances
setting in RISKS S3 becomes "drop `CAP_NET_ADMIN`" rather than "change nothing".
This is a documentation correction, not a design change — the intent is
unambiguous everywhere else.

### 3. The `_apt` setgroups trap

Every apt operation failed inside an instance:

```
E: setgroups 65534 failed - setgroups (22: Invalid argument)
```

apt drops privileges to the `_apt` user, and `setgroups` returns `EINVAL` inside
a user namespace. Since `userns=auto` is mandatory (ADR-0004 R2), this affects
**every instance, always** — a learner's first `apt install` would fail.

Fixed in the AMI with `APT::Sandbox::User "root";`.

**Carry into M2:** the real `images/amis/ubuntu-24.04` needs this, and it
deserves a test of its own, because it is invisible until someone runs apt.

### 4. sshd needs host keys, and socket activation makes readiness lie

Two problems, one symptom. The AMI deletes its host keys at build time so that
instances do not share identity — but nothing regenerated them, so `ssh.service`
failed with `no hostkeys available`.

The harness did not notice, because `ssh.socket` binds port 22 immediately under
socket activation and hands over to `ssh.service` only when a connection arrives.
**A port check therefore reported "ready" in ~830 ms on an instance whose sshd had
failed outright**, and the first boot-time measurements in this spike were
meaningless as a result.

Two fixes:

- Generate host keys in an `ExecStartPre`. Ubuntu already ships
  `ExecStartPre=/usr/sbin/sshd -t`, and systemd drop-ins **append**, so the
  validity check ran first and failed. The drop-in has to reset the list with an
  empty `ExecStartPre=` before adding both in order.
- Disable socket activation in favour of a plain long-running sshd, as a real
  EC2 instance runs. Readiness should not be able to lie.

The harness now waits for an actual `SSH-2.0-…` banner rather than an open port.

**This invalidated the spike's own first set of numbers.** The 1 192 ms median
reported before the fix was the time for systemd to open a socket on behalf of a
service that then failed. Every timing figure in this report was re-measured
afterwards.

**Carry into M2:** the AMI must generate host keys on first boot, and
instance-status checks (post-MVP) must test the protocol, not the port. This is
the same class of error as "stored but not enforced" — a green signal that means
nothing.

### 5. cloud-init's metadata wait dominates boot when IMDS is unreachable

Turning cloud-init on (a [SP4](SP4-cloud-init-imds.md) change) pushed this
spike's boot-to-sshd from 1.2 s to **10.9 s**, and the run failed its own target.
Nothing had regressed in the runtime: SP1 instances have no network, so
cloud-init waited out its `max_wait` of 10 s before giving up.

The AMI now uses `max_wait: 3` and `timeout: 1`. cloud-init's stock values are
sized for a real cloud; Nephos serves IMDS from the instance's own VPC router,
where a reachable service answers in well under a millisecond and an unreachable
one should cost seconds rather than minutes.

**Carry into M2:** keep the short timeouts, and measure boot-to-sshd against a
*reachable* IMDS. A benchmark run with the metadata service missing measures the
timeout, not the product.

## Platform notes

**Docker Desktop's credential helper is flaky through WSL2.** Builds
intermittently fail with `error getting credentials - err: exit status 1`,
alongside `WSL ERROR: UtilAcceptVsock:271: accept4 failed 110`. It succeeds on
retry. The harness retries builds three times. Evidence for
[RISKS T1](../RISKS.md#t1-nested-runtime-fragility-across-hosts): this platform
is genuinely unreliable, and `nephos up` will need the same tolerance.

**Podman storage survives appliance restarts**, because it lives on the
`nephos-data` volume by design. A rerun therefore starts with the previous run's
containers present. That is correct behaviour and exactly what ADR-0007's
garbage collection is for — but it means M1's startup GC is not optional.

**Short names do not resolve.** `docker save | podman load` lands an image under
`docker.io/library/...`, and Podman refuses bare short names. AMIs need
fully-qualified local references.

## Verdict

**ADR-0003 and ADR-0004 are validated on WSL2 + Docker Desktop.** No fallback
ADR is needed; the containerd path in ADR-0004 stays unused.

Boot-to-sshd comes in under the 5 s target even in this spike's deliberately
pessimistic no-network configuration, and at **2 735 ms** in the realistic one
([SP4](SP4-cloud-init-imds.md)). Twenty idle instances use 450 MB against a 2 GiB
budget — on a host with 1.9 GiB of RAM, a quarter of what
[ARCHITECTURE §12](../ARCHITECTURE.md#12-platform-support) recommends.

## Outstanding

- [ ] **Native Linux leg.** ROADMAP M0 requires these results on Ubuntu 24.04
      with native Docker as well. The `Spikes` workflow runs SP1–SP4 on a
      `ubuntu-24.04` runner, but it needs the branch pushed.
- [ ] Amend ADR-0004's capability wording (finding 2).
- [ ] Carry findings 1, 3, and 4 into the M1 appliance image and the M2 AMI
      pipeline.
