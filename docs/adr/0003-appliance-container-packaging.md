# ADR-0003: Package Nephos as an appliance container

- **Status:** Accepted
- **Date:** 2026-09-17

## Context

Nephos needs a Linux kernel with network namespaces, nftables, and cgroup v2. It must satisfy these requirements:

- work on Linux first, and on Docker Desktop (macOS, Windows), where Docker already runs inside a VM;
- never break the learner's machine, and make "wipe everything" reliable;
- install with one command and be friendly to beginners;
- run on the maintainer's own setup: Windows 10, WSL2, and Docker Desktop.

## Options considered

### Option A: Native Linux install (deb/rpm package + systemd service)
- Pros: simplest to debug; no nesting; best performance.
- Cons:
  - Linux only.
  - Creates namespaces, sysctls, and nftables tables on the host, next to firewalld, ufw, and Docker's own rules.
  - Cleanup depends on Nephos's code being bug-free.
  - Needs root.
  - Breaks the "can't break your machine" promise.

### Option B: One privileged appliance container (the kind/minikube pattern)
- Pros:
  - Docker is the only prerequisite. Works on Linux, Docker Desktop (macOS/Windows), OrbStack, and Colima.
  - All Nephos networking lives inside the container's own network namespace, so the host's routes and firewall stay untouched.
  - A hard reset is simply "remove the container and its volume".
  - A versioned image pins the exact Podman, nftables, and tool versions.
  - CPU, memory, and pids limits come straight from `docker run` flags.
- Cons:
  - `--privileged` is close to root on a Linux host: it can load kernel modules, change global sysctls, and access devices.
  - Nested container storage must live on a volume (no overlay on overlay).
  - The host can't route into container IPs on Docker Desktop (solved by [ADR-0006](0006-learner-access-through-simulated-internet.md)).
  - The kernel is shared, so kernel-global settings are visible to the host.
  - Rootless Docker and environments that forbid privileged containers are unsupported.

### Option C: VM appliance (Lima, WSL2, Hyper-V, QEMU/KVM)
- Pros: strongest isolation (hypervisor boundary); its own kernel; unlocks Incus and microVMs.
- Cons:
  - Per-OS drivers, VM images, and hypervisor prerequisites.
  - More RAM, disk, and startup time.
  - Much more for a solo maintainer to support.
  - Nested virtualization is unavailable in many setups (for example, Windows 10 WSL2).

### Option D: Sibling containers through the host Docker socket
- Pros: no nesting.
- Cons: networking has to live in namespaces owned by the host daemon, which needs host network or PID access. That defeats both isolation and reliable teardown.

## Decision

Ship Nephos as an **appliance container**. `nephos up` starts image `nephos-appliance` as a container named `nephos` with:

- `--privileged`, but **its own** network and PID namespaces. Never `--network host` or `--pid host`.
- `--cgroupns private`.
- A named volume `nephos-data` mounted at `/var/lib/nephos`. It holds the database, instance storage, and the AMI cache.
- No host bind mounts.
- Resource limits (`--memory`, `--cpus`, `--pids-limit`) from `nephos up` flags, with safe defaults.
- The API and console published on **127.0.0.1:7788** only.

Lifecycle commands: `nephos down` stops the appliance. `nephos down --purge` removes the container and its volume. `nephos reset --hard` purges, then starts again.

`nephos doctor` (also run by `nephos up`) refuses to continue unless all of these hold:

- Docker is reachable, and it is not rootless Docker;
- cgroup v2 is available;
- the kernel meets the version floor, with `nf_tables`, `veth`, and `dummy` available;
- nftables and namespaces work inside the appliance;
- there is enough free memory and disk;
- the architecture is amd64 or arm64.

The appliance image uses a Debian stable base. It contains `nephosd`, `nephos-hook`, Podman with crun and conmon, nftables, iproute2, and a minimal init.

## Consequences

- Positive: one prerequisite everywhere; host networking untouched; a reset that always works; identical userspace on every platform.
- Negative / costs:
  - On a Linux host, the appliance must be treated as root-equivalent. The documentation says so plainly, and [RISKS](../RISKS.md) S1 tracks it.
  - Environments that block privileged containers (for example, some corporate Docker Desktop policies) are unsupported in the MVP.
  - Only per-namespace sysctls are changed; kernel-global settings are never modified.
- Follow-ups: M16 adds a VM delivery that boots the same appliance contents as a VM (`nephos up --driver vm`) for stronger isolation and KVM-backed instances.

## Validation

- **M0 spike SP1:** the appliance starts, runs nested instances, and applies nftables rules on Ubuntu 24.04 (native Docker) and on Windows 10 with WSL2 and Docker Desktop.
  - **Result (2026-09-18):** passes on Windows 10 with WSL2 and Docker Desktop ([SP1](../spikes/SP1-appliance-nested-containers.md), [SP3](../spikes/SP3-routed-vpc-plane.md)). The appliance keeps its own network and PID namespaces, has no host bind mounts, and its root namespace holds nothing but `lo` and its uplink. The native-Docker leg runs through the `Spikes` workflow on a `ubuntu-24.04` runner.
  - One requirement the spike added: the appliance entrypoint must delegate the cgroup v2 controllers to a leaf cgroup, because cgroup v2 forbids a cgroup from both holding processes and enabling controllers for its children. Without it, nested Podman cannot apply any limit. The entrypoint refuses to start unless `memory`, `pids`, and `cpu` all delegate.
- **M8 install matrix:** also covers macOS with Docker Desktop and with OrbStack.
- **Revisit** if privileged containers are blocked for a large share of target users, or if a kernel-global side effect reaches the host.
