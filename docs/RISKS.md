# Nephos Risks

**Status:** planning (September 2026). Reviewed at the end of every milestone ([ROADMAP](ROADMAP.md)).

## How to read this document

Each risk has:

- an ID: **T** technical, **S** security, **P** project;
- a likelihood and an impact (Low, Medium, or High);
- a mitigation, and the milestone where it lands;
- an early-warning signal: what to watch for.

Risks whose mitigation lands in **M0–M8** must be mitigated before v0.1.0 (see the [M8 acceptance criteria](ROADMAP.md#m8-v010-the-mvp-release)). Retired risks move to the [retired list](#retired-risks) with a date and the reason.

## Top risks

| Rank | Risk | Why it's on top |
|---|---|---|
| 1 | [T3](#t3-network-behavior-diverges-from-aws) Network behavior diverges from AWS | A learning tool that teaches the wrong mental model does harm |
| 2 | [T1](#t1-nested-runtime-fragility-across-hosts) Nested runtime fragility | If instances don't boot on common setups, nothing else matters |
| 3 | [P2](#p2-solo-maintainer-bandwidth) Solo maintainer bandwidth | The scope is large and the bus factor is 1 |
| 4 | [S1](#s1-the-privileged-appliance-is-root-equivalent-on-linux-hosts) / [S3](#s3-kernel-attack-surface-through-instance-owned-network-namespaces) Privileged appliance and kernel attack surface | Nephos runs on learners' personal machines |
| 5 | [P1](#p1-the-aws-feature-treadmill) AWS feature treadmill | Chasing breadth would sink the learning focus |

---

## Technical risks

### T1. Nested runtime fragility across hosts

- **Likelihood / impact:** High / High.
- **Risk:** Podman inside a privileged container behaves differently across kernels, cgroup setups (WSL2 has historically mixed cgroup v1 and v2), Docker Desktop releases, AppArmor and SELinux policies, snap-packaged Docker, and storage drivers. Instances then fail to boot for part of the user base.
- **Mitigation:**
  - Spikes SP1 and SP2 on Ubuntu 24.04 and Windows 10 WSL2 + Docker Desktop.
  - The appliance pins its own userspace.
  - `nephos doctor` checks each prerequisite and names a specific remedy (for example, the `.wslconfig` kernel command line that forces cgroup v2).
  - A published support matrix; e2e CI on Ubuntu 24.04; manual release QA on WSL2 and macOS.
  - The containerd fallback in [ADR-0004](adr/0004-instances-as-system-containers.md).
- **Milestone:** M0, then M8.
- **Early warning:** SP1 fails on WSL2; issues mentioning cgroup or overlay mount errors.

### T2. systemd and cloud-init quirks inside containers

- **Likelihood / impact:** Medium / Medium.
- **Risk:** Units that expect a real machine fail (udev, logind, time sync). cloud-init waits for networking and slows boot. Upstream image updates break AMIs silently.
- **Mitigation:**
  - Nephos-built AMIs with irrelevant units masked and the cloud-init datasource preconfigured.
  - A boot-time budget test in CI (median under 5 s).
  - AMIs pinned by digest and rebuilt on a schedule with tests.
  - `nephos debug instance`.
- **Milestone:** M0 (SP1, SP4), M2.
- **Early warning:** boot time creeping upward; failed units in CI AMI tests.

### T3. Network behavior diverges from AWS

- **Likelihood / impact:** Medium / High.
- **Risk:** a modeling mistake or bug (NACL scope, route precedence, NAT behavior) teaches learners something that isn't true on AWS. This is the worst possible failure for Nephos.
- **Mitigation:**
  - One semantics catalog (`internal/semantics`) shared by the nftables renderer and `explain`.
  - A connectivity matrix where every case cites the AWS behavior it reproduces.
  - Differential testing: `explain` must equal `probe`.
  - The fidelity table in [ARCHITECTURE §5.5](ARCHITECTURE.md#55-aws-fidelity-and-known-deviations) is updated in the same pull request as any behavior change.
  - Before v0.1, the key matrix scenarios are replayed once on real AWS with Terraform (a few dollars) to confirm the expected results.
  - A "fidelity bug" issue template.
- **Milestone:** M3–M5, M8.
- **Early warning:** reports of "AWS does this differently"; `explain` and `probe` disagreeing.

### T4. Reconciliation drift and partial failures

- **Likelihood / impact:** Medium / High.
- **Risk:** the database says an instance is running but its container is gone, namespaces are orphaned after a crash, or an operation stops halfway. Any of these breaks the "reset always works" promise.
- **Mitigation:**
  - Level-triggered reconcilers and idempotent engine operations ([ADR-0007](adr/0007-sqlite-state-and-reconciliation.md)).
  - Garbage collection at startup by `nx-` prefix and container labels; periodic resync.
  - A leak checker after every reset.
  - Chaos and leak tests in nightly CI.
  - `nephos reset --hard` as the guaranteed escape hatch.
- **Milestone:** M1, M8.
- **Early warning:** nightly leak-checker failures; "stuck in pending" reports.

### T5. Laptop resource limits

- **Likelihood / impact:** High / Medium.
- **Risk:** on 8 GB machines already running Docker Desktop and a browser, several instances plus the appliance exhaust memory. AMI layers and root volumes fill the disk.
- **Mitigation:**
  - Slim AMIs and default appliance limits.
  - A capacity model with a bounded memory overcommit, and a running-instance quota.
  - `nephos doctor` checks memory and disk; `nephos system prune` clears the image cache.
  - Labs use `t3.nano` or `t3.micro` and at most 5 instances.
- **Milestone:** M0 (measure), M2, M8.
- **Early warning:** SP1 idle memory above 60 MB per instance; OOM reports.

### T6. MTU, DNS, and nested networking surprises

- **Likelihood / impact:** Medium / Medium.
- **Risk:** Docker Desktop's VM networking, corporate VPNs with a lower MTU, or odd resolver setups cause SSH sessions that hang after connecting, stalled `apt`, and failed name resolution.
- **Mitigation:**
  - Uplink MTU detection and TCP MSS clamping at the edge; configurable MTU and upstream DNS.
  - `nephos doctor` runs connectivity and MTU checks against `echo.nephos.test` and the real internet.
  - A troubleshooting guide.
- **Milestone:** M3, M8.
- **Early warning:** "SSH hangs" or "apt hangs" issues.

### T7. Image size, pull time, and offline use

- **Likelihood / impact:** Medium / Medium.
- **Risk:** the first run pulls hundreds of megabytes (appliance and AMIs). This fails on flaky networks and in offline classrooms, and breaks the 10-minute quickstart.
- **Mitigation:**
  - Slim images, and progress output during `nephos up`.
  - `nephos images pull` to pre-fetch.
  - `--sealed` mode works once images are cached.
  - An offline bundle export and import after the MVP.
- **Milestone:** M2, M8.
- **Early warning:** quickstart QA exceeds 10 minutes.

### T8. Go and network namespace pitfalls

- **Likelihood / impact:** Medium / High.
- **Risk:** the Go runtime moves goroutines between OS threads. A thread left inside a namespace can create sockets, netlink calls, or **nftables rules in the wrong namespace**, including the appliance root namespace. These bugs are intermittent and hard to diagnose.
- **Mitigation:**
  - Only `internal/network/netns` switches namespaces, with locked OS threads.
  - A thread that switched namespace is never returned to the scheduler; it exits.
  - Netlink handles are bound to namespace handles.
  - Stress tests run with the race detector, plus a lint rule forbidding `setns` elsewhere ([ADR-0002](adr/0002-go-for-control-plane-and-cli.md)).
- **Milestone:** M0 (SP3), M1.
- **Early warning:** flaky integration tests; objects appearing in the wrong namespace.

### T9. Access friction for learners

- **Likelihood / impact:** High / Medium.
- **Risk:** ProxyCommand setup, private-key permissions (OpenSSH on Windows rejects keys with broad ACLs), and confusion between WSL and Windows `ssh` stall beginners at the very first step.
- **Mitigation:**
  - `nephos ssh` is the primary path and handles keys and permissions.
  - `nephos ssh-config` enables plain `ssh`, and the console offers Instance Connect.
  - Key files are written with correct permissions or ACLs on each OS.
  - Per-OS quickstarts.
- **Milestone:** M3, M6, M8.
- **Early warning:** onboarding tests stall at the SSH step.

### T10. User-namespace ID-mapping costs

- **Likelihood / impact:** Medium / Medium.
- **Risk:** without ID-mapped mounts (older kernels), per-instance user namespaces force copying or chowning image layers. First boot becomes slow and disk usage doubles.
- **Mitigation:**
  - Recommend 6.x kernels, and measure in SP1.
  - An option to share one ID range per AMI.
  - The containerd fallback with ID-mapped snapshots.
- **Milestone:** M0, M2.
- **Early warning:** SP1 first boot over 10 s; disk usage per instance close to the AMI size.

### T11. Dependency drift (Podman, nftables, cloud-init)

- **Likelihood / impact:** Medium / Low.
- **Risk:** API or behavior changes in pinned components break upgrades.
- **Mitigation:**
  - The appliance pins versions.
  - The Podman client covers a small, contract-tested API surface.
  - Renovate proposes upgrades with the full e2e suite as the gate.
- **Milestone:** M1, M8.
- **Early warning:** upgrade pull requests failing e2e.

## Security risks

### S1. The privileged appliance is root-equivalent on Linux hosts

- **Likelihood / impact:** Medium / High.
- **Risk:** a privileged container can reach the host kernel. A Nephos bug, or code running in the appliance's root context, could change host kernel settings or escape the container. On Docker Desktop, the blast radius is the Docker VM; on native Linux, it's the host.
- **Mitigation:**
  - Say so plainly in the install output and docs, with kind and minikube as familiar precedents.
  - Keep the privileged surface small:
    - `nephosd` is the only listening component, bound to localhost;
    - no host mounts; own network and PID namespaces;
    - only per-namespace sysctls;
    - Nephos never loads kernel modules (`nephos doctor` asks the user to).
  - Offer VM delivery for stronger isolation (M16).
  - After the MVP, research running the appliance under the Sysbox runtime where it's installed.
- **Milestone:** M1 (design rules), M8 (docs, threat model), M16.
- **Early warning:** any code that writes global sysctls (`/proc/sys/net/*/all`, `/proc/sys/net/*/default` outside Nephos namespaces) or touches `/sys/module`.

### S2. Learner root escapes an instance into the appliance

- **Likelihood / impact:** Low / High.
- **Risk:** misconfiguration (a missing user namespace, extra capabilities, an exposed socket) or a runtime bug lets root inside an instance reach the appliance, and so the host.
- **Mitigation:**
  - `userns=auto` is mandatory, and the reconciler verifies the UID mapping of every instance.
  - Default capabilities and seccomp; no devices; no Podman socket; metadata exposes no secrets.
  - Automated security tests assert that an instance can't see appliance processes, mounts, or other instances.
- **Milestone:** M0 (SP1), M2, M8.
- **Early warning:** failing security tests; container runtime CVEs.

### S3. Kernel attack surface through instance-owned network namespaces

- **Likelihood / impact:** Medium / High.
- **Risk:** instance root holds `CAP_NET_ADMIN` in its own namespace, which is what lets `ufw` work. That exposes nf_tables and netlink code paths, which have had local privilege escalation CVEs (for example CVE-2024-1086) reachable from user namespaces.
- **Mitigation:**
  - Document the tradeoff.
  - `nephos doctor` warns when the kernel is older than a maintained minimum.
  - A "hardened instances" setting drops `CAP_NET_ADMIN` inside instances (no instance-level firewall). It becomes the default for classroom mode (M15).
  - VM delivery (M16), and guidance to keep the host or Docker Desktop kernel updated.
- **Milestone:** M2, M8, M15.
- **Early warning:** new nf_tables or user-namespace privilege escalation CVEs.

### S4. Instance exposure and egress abuse

- **Likelihood / impact:** Low / Medium.
- **Risk:** if instances were reachable from the LAN or the internet, learner-made configurations (password SSH, open services) would become targets. Real egress could be misused to attack third parties.
- **Mitigation:**
  - Public IPs exist only inside the appliance, and every access path is bound to localhost ([ADR-0006](adr/0006-learner-access-through-simulated-internet.md)).
  - AMIs allow key-only SSH, with no default passwords.
  - `--sealed` mode.
  - No port-publishing feature. Any future "expose to LAN" option needs an ADR, an explicit opt-in, and warnings.
  - Egress rate limits if abuse ever appears.
- **Milestone:** M3, M8.
- **Early warning:** requests to expose instances on the LAN.

### S5. Attacks on the localhost API

- **Likelihood / impact:** Medium / High.
- **Risk:** a malicious web page sends requests to `127.0.0.1:7788` (CSRF), uses DNS rebinding, or another local process reads the token. The API can run commands as root inside instances.
- **Mitigation:**
  - A token on every endpoint except `/v1/health`.
  - Host header allowlist (`localhost`, `127.0.0.1`, `[::1]`); Origin checks on WebSockets and state-changing requests.
  - `SameSite=Strict` HttpOnly session cookies; no CORS.
  - Credentials file mode 0600, and `nephos token rotate`.
  - M1 tests cover bearer-token rejection and localhost binding for the
    endpoints it ships. M6 adds browser-facing Host, Origin, cookie, and CORS
    tests with the web console.
- **Milestone:** M1 (token, localhost binding), M6 (browser protections).
- **Early warning:** any unauthenticated endpoint besides health; security reports.

### S6. Resource exhaustion

- **Likelihood / impact:** High / Medium.
- **Risk:** learners experiment. Fork bombs, memory hogs, and disks filled inside instances, or runaway reconcile loops, degrade the appliance or the host.
- **Mitigation:**
  - pids, memory, and CPU limits per instance, and limits on the appliance itself.
  - Disk usage monitoring with warnings, and a per-instance soft quota: an instance exceeding it is stopped with a clear `state_reason`.
  - Reconcile backoff and log rotation.
- **Milestone:** M2, M8.
- **Early warning:** appliance OOM events; rapid volume growth.

### S7. Supply chain: images, dependencies, and lab content

- **Likelihood / impact:** Low / High.
- **Risk:** a compromised upstream package or image, or a malicious third-party lab pack that runs scripts.
- **Mitigation:**
  - Base images pinned by digest; cosign signatures verified on pull; SBOMs.
  - Renovate, `govulncheck`, and `npm audit` in CI.
  - Only built-in labs in the MVP. Signed catalogs with a trust prompt later (M15). Script checks run sandboxed ([LABS §8](LABS.md#8-safety-of-checks)).
- **Milestone:** M2, M7, M8.
- **Early warning:** vulnerability scanner findings; unsigned artifacts in a release.

## Project risks

### P1. The AWS feature treadmill

- **Likelihood / impact:** High / High.
- **Risk:** requests for more services, SDK compatibility, and API parity pull the project away from learning and stall the roadmap.
- **Mitigation:**
  - Explicit non-goals ([VISION](VISION.md#5-non-goals)).
  - "A feature ships with a lab" rule.
  - New resource types need an ADR.
  - The backlog is triaged by learning value.
- **Milestone:** ongoing.
- **Early warning:** the resource count grows while milestones slip; repeated requests for AWS SDK compatibility.

### P2. Solo maintainer bandwidth

- **Likelihood / impact:** High / High.
- **Risk:** burnout, long gaps, and a bus factor of 1 on a large scope.
- **Mitigation:**
  - Thin milestones with demos, so progress stays visible.
  - [AGENTS.md](../AGENTS.md) and ADRs preserve context between sessions.
  - Early automation of tests and releases.
  - `good first issue` labels, with lab authoring as the low-barrier contribution path.
  - No public promises of dates.
- **Milestone:** ongoing.
- **Early warning:** a milestone overruns its estimate by more than 50%; pull requests go stale.

### P3. Name collision

- **Likelihood / impact:** Low / Low. **Decided 2026-09-17: keep the name.**
- **Risk:** "Nephos" is also used by the **NeoNephos Foundation** (Linux Foundation Europe, cloud-to-edge) and an archived Hyperledger Labs project. That can mean confusion and poor search visibility.
- **Decision:** Nephos is a freely downloadable open source learning tool that people run locally, not a commercial offering, so the overlap is accepted. No trademark filing is planned, and no rename.
- **Remaining mitigation:** documentation states that Nephos is unaffiliated with those projects. Revisit only if a rights holder objects or users actually confuse the projects.
- **Milestone:** decided; no milestone work.
- **Early warning:** an objection from a rights holder; issues or reviews mixing up the projects.

### P4. AWS trademarks and trade dress

- **Likelihood / impact:** Low / Medium.
- **Risk:** copying the AWS console's look, logos, or architecture icons, or implying affiliation, draws complaints.
- **Mitigation:**
  - Nephos's own visual identity; no AWS logos or icons.
  - AWS names are used only descriptively.
  - A "not affiliated with Amazon Web Services" notice in the README and the console's About page.
- **Milestone:** M6, M8.
- **Early warning:** UI changes that imitate AWS styling.

### P5. License compliance of shipped artifacts

- **Likelihood / impact:** Medium / Medium.
- **Risk:** each shipped artifact carries third-party license obligations:
  - the appliance image bundles GPL programs (nftables, iproute2, crun);
  - the console bundles EPL-2.0 code (`elkjs`);
  - AMIs contain distribution packages.
- **Mitigation:**
  - Ship unmodified distribution packages with sources available.
  - A generated NOTICE and third-party license report for every release.
  - License scanning in CI; AGPL dependencies need an ADR ([ADR-0011](adr/0011-apache-2-license-with-dco.md)).
- **Milestone:** M8.
- **Early warning:** scanner flags; release artifacts without notices.

### P6. Competitors move into learning

- **Likelihood / impact:** Medium / Medium.
- **Risk:** fakecloud or Floci add full VPC semantics, Vyomi adds guided labs, or LocalStack targets education, and Nephos's differentiation narrows.
- **Mitigation:**
  - Compete where others aren't: explainability, a community lab ecosystem, correct semantics end to end, and a truly open license.
  - Ship v0.1 early and grow lab authors.
  - Position as complementary to testing emulators.
  - Review competitor changelogs every quarter.
- **Milestone:** ongoing.
- **Early warning:** competitor release notes mentioning labs, routing, or NAT behavior.

## Retired risks

None yet.
