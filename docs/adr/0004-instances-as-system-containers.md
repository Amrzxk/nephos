# ADR-0004: Instances are system containers run by Podman

- **Status:** Accepted; **validated in M0** by [SP1](../spikes/SP1-appliance-nested-containers.md) and [SP2](../spikes/SP2-eni-plumbing-hook.md)
- **Date:** 2026-09-17 (capability wording amended 2026-09-18; see Validation)

## Context

A Nephos instance must feel like a small Linux server:

| # | Requirement |
|---|---|
| R1 | systemd as PID 1, without extra privileges |
| R2 | A separate user namespace per instance, so root in the instance is not root in the appliance |
| R3 | A network namespace wired by Nephos **before** PID 1 starts, owned by the instance's user namespace (so the learner can run `ufw`) |
| R4 | Stop/start keeps the root filesystem and private IP |
| R5 | CPU, memory, and pids limits per instance type |
| R6 | Console access and console output that bypass the network |
| R7 | OCI images as AMIs |
| R8 | Later: create an image from an instance |

It must also run **inside** the appliance container ([ADR-0003](0003-appliance-container-packaging.md)). The maintainer chose system containers over near-VM containers and microVMs for the MVP.

## Options considered

### Option A: Nested Docker Engine
- Pros: familiar; mature Go SDK.
- Cons:
  - No systemd mode: needs CAP_SYS_ADMIN or a writable bind mount of the whole cgroup tree.
  - User namespaces only daemon-wide (`userns-remap`).
  - Can't join a netns prepared by Nephos.
  - Adds its own iptables rules.

### Option B: Podman (rootful, inside the appliance), driven through its libpod REST API
- Pros:
  - `--systemd=always` runs systemd as PID 1 with no extra capabilities (tmpfs mounts, private writable cgroup namespace, correct stop signal).
  - `--userns=auto` gives every instance a unique ID range.
  - Supports OCI hooks and OCI images, and can commit containers to images (R8).
  - A Docker-compatible CLI for debugging inside the appliance.
- Cons:
  - The official Go bindings are heavy, so Nephos needs a thin HTTP client.
  - Netns ownership under `userns=auto` and the cost of ID-mapping images must be verified.

### Option C: containerd through its Go client
- Pros: full control of the OCI spec, including creating the netns and user namespace together, and hooks; embeddable; Kubernetes-grade.
- Cons: Nephos would have to build systemd-friendly specs, log handling, exec plumbing, and image commit itself. More code before the first instance boots.

### Option D: LXC or Incus system containers
- Pros: the most VM-like (lxcfs views, Docker inside instances, cloud images, live device hotplug).
- Cons:
  - Incus can't be isolated inside a container (it needs the host's network and PID namespaces), which contradicts ADR-0003.
  - Raw LXC means cgo bindings or CLI wrappers, plus a separate image pipeline instead of OCI.

### Option E: MicroVMs (Firecracker, Cloud Hypervisor)
- Pros: a real kernel and real block devices.
- Cons: needs `/dev/kvm`, which Docker Desktop VMs and Windows 10 WSL2 don't provide; slower boot; far more MVP work.

## Decision

Run instances as **system containers created by rootful Podman inside the appliance**, through a small Go client for the libpod REST API, behind a `compute.Runtime` interface.

**Container settings**
- `systemd=always` and `userns=auto` (a unique 65,536-ID range per instance).
- Default capabilities **plus `CAP_NET_ADMIN`**, and the default seccomp profile; no devices. `sudo` must keep working.
  - `CAP_NET_ADMIN` applies only inside the instance's own network namespace, and it is what satisfies R3: without it instance root cannot run `ufw` or `nft`, and the lockout-and-recover lessons are impossible. The kernel attack surface this opens is accepted and tracked in [RISKS S3](../RISKS.md#s3-kernel-attack-surface-through-instance-owned-network-namespaces), which also defines the "hardened instances" setting that drops it again.
  - *Amended 2026-09-18 after [spike SP1](../spikes/SP1-appliance-nested-containers.md).* The original wording said "default capabilities", which contradicted R3 and validation 4 of this same ADR: Podman's default set omits `CAP_NET_ADMIN`, and instance root could not add a firewall rule. This is a correction of the text, not a change of decision — R3, validation 4, and RISKS S3 all already assumed the capability.
- cgroup limits (CPU quota, memory, pids) derived from the instance type.
  - The appliance must delegate the cgroup v2 `memory`, `pids`, and `cpu` controllers to nested containers, and **refuse to start if it cannot**. SP1 found that without delegation Podman cannot apply limits at all, which would leave Nephos reporting instance-type limits it was not enforcing.

**Networking** ([ADR-0005](0005-nephos-owned-routed-network-plane.md))
- The netns is created together with the instance's user namespace.
- `nephos-hook`, an OCI `createRuntime` hook, asks `nephosd` to plumb interfaces before PID 1 runs.

**AMIs**
- OCI images built from `images/amis/` with systemd, openssh-server, sudo, and cloud-init configured to read the Nephos instance metadata service.
- cloud-init creates the default user (`ubuntu` on Ubuntu). The MVP ships `ubuntu-24.04`.

**Storage and console**
- Root volume: the container's writable layer on the `nephos-data` volume. It survives stop/start and is deleted on terminate.
- Serial-console analog: `exec` into the instance. Console output is the container log.

The interface keeps backends swappable (Podman now, microVMs in M16):

```go
type Runtime interface {
    EnsureImage(ctx context.Context, ref ImageRef) error
    Create(ctx context.Context, spec InstanceSpec) (RuntimeID, error)
    Start(ctx context.Context, id RuntimeID) error
    Stop(ctx context.Context, id RuntimeID, timeout time.Duration) error
    Delete(ctx context.Context, id RuntimeID) error
    Inspect(ctx context.Context, id RuntimeID) (RuntimeStatus, error)
    Exec(ctx context.Context, id RuntimeID, req ExecRequest) (ExecSession, error)
    ConsoleOutput(ctx context.Context, id RuntimeID) (io.ReadCloser, error)
}
```

**Fallback:** if Podman fails validation, implement `internal/compute/containerd` with a Nephos-built OCI spec and record the switch in a superseding ADR.

## Consequences

- Positive:
  - Instances boot in seconds, use tens of megabytes idle, and run wherever the appliance runs.
  - Learners get real systemd, SSH, package managers, and cloud-init user data.
- Negative / costs: documented deviations from EC2.
  - The kernel is shared: `uname` shows the appliance kernel, and there are no kernel modules or swap.
  - No raw block devices yet (the EBS "mkfs and mount" exercise waits for M12).
  - `free` and `nproc` show appliance resources.
  - Docker inside an instance is unsupported in the MVP.
  - Reboot means a container restart.
- Follow-ups: the AMI build pipeline and signing arrive in M2 and M8. v0.1 ships **Ubuntu 24.04 only**; further AMIs (Amazon Linux 2023, Debian) are in the roadmap backlog.

## Validation

**M0 spikes SP1 and SP2** must show, on Ubuntu 24.04 and on Windows 10 with WSL2 and Docker Desktop:

1. An `ubuntu-24.04` instance accepts SSH connections within 5 s of start (median). The first start from a cached image takes under 10 s (this measures the ID-mapping cost).
2. `systemctl start nginx` works, and no units fail except ones deliberately masked.
3. Root inside the instance maps to a non-zero UID in the appliance and can't see other instances.
4. Instance root can add a firewall rule (`ufw` or `nft`) in its own netns.
5. `eth0` exists before systemd starts (no boot race).
6. Stop/start keeps files and the private IP. Twenty idle instances fit in 2 GiB of appliance memory.

**Result (2026-09-18):** all six pass on Windows 10 with WSL2 and Docker Desktop.
[SP1](../spikes/SP1-appliance-nested-containers.md) records 19 of 19 assertions,
with median boot-to-sshd of 3 977 ms against the 5 s target, first boot of
3 743 ms against 10 s, and 20 idle instances in 450 MB — on a host with 1.9 GiB
of RAM. SP1 runs instances without a network, so cloud-init sits out its
metadata wait; with a reachable IMDS, SP4 measures 2 735 ms. [SP2](../spikes/SP2-eni-plumbing-hook.md) records 18 of 18, including
`eth0` present before PID 1 and an unplumbable instance refusing to start. The
containerd fallback below is therefore **not** being taken.

Three corrections came out of those spikes and are reflected above and in the
AMI: `CAP_NET_ADMIN` must be added explicitly, the cgroup v2 controllers must be
delegated (and the appliance must fail closed if they cannot be), and
`APT::Sandbox::User "root"` is required or every `apt` command fails inside a
user namespace.

**Native result (2026-09-22):** all six criteria also pass on Ubuntu 24.04 with
Docker 28.0.4 in
[`Spikes` run 35698867324](https://github.com/Amrzxk/nephos/actions/runs/35698867324).
SP1 records a 4 278 ms median boot-to-sshd, a 5 614 ms first boot, and
443,744,256 bytes total for 20 instances; SP2 again proves pre-PID-1 plumbing
and fail-closed launch behavior. The containerd fallback remains unnecessary.

**Revisit** if any point fails with no workaround, or when the microVM backend lands.
