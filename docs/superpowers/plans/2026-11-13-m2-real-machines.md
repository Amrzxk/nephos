# M2: Real Machines — Design and Slice Plan

**Status:** accepted, 2026-11-13. Written by Claude, who owns delivery. The
maintainer decided AMI publishing (deferred to M8).

**Goal (ROADMAP M2):** instances behave like EC2 instances you can log into and
bootstrap. The roadmap section holds the scope, the demo and the acceptance criteria.
This document decides *how*. The settled architecture comes from ARCHITECTURE §5.1 and
[SP4](../../spikes/SP4-cloud-init-imds.md), which proved stock cloud-init works with a
Nephos IMDS.

## Starting point (what M1 left)

- **Instance type:** hardcoded to `t3.micro` in two places (`internal/service/instance.go`
  and `internal/store/instances.go`), with fixed limits of 2 vCPU, 1 GiB and 512 tasks.
- **Lifecycle:** run and terminate only. A terminated instance is deleted at once.
- **AMI:** `images/dev-ami` is built locally and imported by the appliance entrypoint
  under the fixed tag `nephos-ubuntu:dev`. It has **no sshd and no cloud-init**.
- **VPC namespaces:** no DNS or IMDS listeners yet. ARCHITECTURE §4.4 step 6 reserves
  their place.

## Design decisions

1. **One process, per-VPC listeners.** `nephosd` opens the DNS socket (base+2 and
   169.254.169.253, UDP/TCP 53) and the IMDS socket (169.254.169.254:80) *inside* each
   VPC namespace.
   - Only `internal/network/netns` may enter a namespace, so it gets a `Listen` helper
     that binds there and returns. The goroutines that serve the sockets never switch
     namespace.
   - The VPC reconciler owns the listener lifecycle, the same way it owns the namespace.
     Listeners are idempotent and rebuilt from SQLite after a restart.
2. **IMDS identifies the caller by its address**, never by anything the request
   carries.
   - The ENI's private IP plus the incoming veth map to exactly one instance through
     the ENI table.
   - An unknown source gets 404.
   - Source spoofing is already dropped by the ENI policy from M1, so a source address
     is trustworthy inside its VPC.
3. **IMDS surface (M2):**
   - The version index at `/` and dated versions such as `2009-04-04` as well as
     `latest`. SP4 showed cloud-init silently gives up without the dated versions.
   - `meta-data/` keys: `instance-id`, `ami-id`, `instance-type`, `local-ipv4`,
     `local-hostname`, `hostname`, `mac`, `placement/availability-zone`, `public-keys/0/openssh-key`.
   - `user-data`, plus the dynamic instance-identity document.
   - IMDSv2: `PUT /latest/api/token` with a TTL header from 1 to 21600 s, and a per-instance
     `http_tokens` setting of `optional` or `required`. Tokens are HMACs in memory keyed
     per daemon start, which is allowed because tokens do not survive a reboot in AWS either.
   - Hop limit: the router already decrements TTL, so the default hop limit of 1 behaves
     as in AWS.
   - **No IAM credential paths.**
4. **Key pairs.**
   - Create generates ed25519 and returns the private key once; Nephos never stores it.
   - Import accepts an OpenSSH public key.
   - The fingerprint uses the AWS format: SHA-256 of the public key, base64.
   - Key pairs live at the workspace level: `key-…` IDs, names unique per workspace.
5. **AMI.**
   - `images/amis/ubuntu-24.04` replaces `images/dev-ami`. It adds openssh-server,
     cloud-init with the SP4 fixes (forced ds-identify policy, `datasource_list: [Ec2]`,
     the ExecStartPre host-key unit) and the default user `ubuntu`.
   - The SQLite `amis` table maps `ami-<17 hex>` to an OCI digest.
   - `instance run --image-id` defaults to the single built-in AMI.
   - Podman runs the image **by digest**, never by tag.
   - The appliance still imports the AMI archive it was built with, so M2 works offline.
6. **Instance types** are a static table, `t3.nano`, `t3.micro`, `t3.small` and `t3.medium`,
   with the AWS vCPU and memory figures and pids scaled to memory. Limits are applied
   through Podman cgroup settings.
   - A capacity check compares the sum of running instances' memory against the
     appliance memory minus a fixed 512 MiB reserve, and fails with
     `InsufficientInstanceCapacity`.
   - The running-instance quota defaults to 20.
7. **Lifecycle.**
   - `stop` runs `podman stop` (SIGRTMIN+3, so systemd shuts down cleanly) and keeps the
     root and the ENI/IP. `start` reuses both.
   - `reboot` runs `podman restart`.
   - A terminated instance becomes a `terminated` row whose resources are released; a
     sweep deletes it after one hour.
   - State names follow EC2: `pending`, `running`, `stopping`, `stopped`, `shutting-down`
     and `terminated`.
8. **VPC DNS.**
   - A small Go resolver answers `ip-a-b-c-d.<az-region>.compute.internal` A and PTR
     records for addresses in the VPC.
   - Everything else is forwarded to the appliance's resolver, so it works without an
     internet gateway, as in AWS.
   - It honors `enable_dns_support` and `enable_dns_hostnames`. The VPC gets those two
     attributes, both defaulting to true.
   - Library: `github.com/miekg/dns` (BSD-3). No cgo.
9. **console-output** returns the instance's journal from boot, read through Podman
   logs, which hold the PID 1 console. It is capped at 64 KiB, as in EC2.

## Slices (one PR each, each with tests and docs)

| Slice | Scope | Proof |
|---|---|---|
| **2A** | Instance types and limits, capacity and quota, stop/start/reboot, terminated retention, `console-output` | Unit tests. A smoke test in which a process over the `t3.nano` limit is OOM-killed inside the instance while the appliance stays up. Stop/start keeps the root file and the IP. |
| **2B** | `images/amis/ubuntu-24.04` (sshd, cloud-init), the AMI table with digests, key pairs, IMDS with v1/v2 | IMDS golden tests, including a dated path and v1 getting 401 when tokens are required. A smoke test where user data writes a marker and `ubuntu`'s authorized_keys holds the key. SSH works between two instances. Boot-to-sshd median under 5 s. |
| **2C** | The VPC DNS resolver and the VPC DNS attributes | Resolver unit tests. A smoke test where `curl http://ip-10-0-1-4.local-1.compute.internal` works from another instance with no IGW. A connectivity-matrix row. |
| **2D** | Reproducible AMI build in CI (pinned base by digest, `SOURCE_DATE_EPOCH`, two builds compared), the M2 demo as `tests/m2-e2e.sh` in `make e2e`, `docs/demos/M2.md`, closure | CI evidence for every box. |

Order: 2A → 2B → 2C → 2D. 2B carries the most risk, and its parts, such as the AMI
and cloud-init, were already de-risked by SP4.

## Decision recorded

- **AMI publishing is deferred to M8** (maintainer, 2026-11-13). M2 builds the AMI
  reproducibly in CI and pins it by digest. The image stays local and is imported by the
  appliance. M8 publishes the appliance and the AMI to GHCR as multi-arch, cosign-signed
  images.

## Risks

- **ARCHITECTURE §13 forbids namespace switching outside `internal/network/netns`:**
  listening inside a namespace is done there, and is covered by a race-detector test.
- **systemd stop inside userns containers:** SP1 already validated `SIGRTMIN+3`. The
  2A smoke test proves a clean stop and start.
- **Capacity accounting with Docker Desktop's VM memory:** the check uses the appliance's
  own cgroup limit, not the host's memory, so it is deterministic.
