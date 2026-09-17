# ADR-0004: Instances are system containers run by Podman

- **Status:** Accepted; the engine choice is validated in M0 (spikes SP1, SP2)
- **Date:** 2026-09-17

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
- Default capabilities and default seccomp profile; no devices. `sudo` must keep working.
- cgroup limits (CPU quota, memory, pids) derived from the instance type.

**Networking** ([ADR-0005](0005-nephos-owned-routed-network-plane.md))
- The netns is created together with the instance's user namespace.
- `nephos-hook`, an OCI `createRuntime` hook, asks `nephosd` to plumb interfaces before PID 1 runs.

**AMIs**
- OCI images built from `images/amis/` with systemd, openssh-server, sudo, and cloud-init configured to read the Nephos instance metadata service.
- cloud-init creates the default user (`ubuntu` on Ubuntu). The MVP ships `ubuntu-24.04`.

**Storage and console**
- Root volume: the container's writable layer on the `nephos-data` volume. It survives stop/start and is deleted on terminate.
- Serial-console analog: `exec` into the instance. Console output is the container log.

The interface keeps backends swappable (containerd now, microVMs in M16):

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

**Revisit** if any point fails with no workaround, or when the microVM backend lands.
