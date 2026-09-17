# ADR-0006: Learner access through a simulated internet edge

- **Status:** Accepted
- **Date:** 2026-09-17

## Context

Learners need to SSH into instances and open their web pages. Two facts shape how:

- On Docker Desktop (macOS and Windows), the host cannot route into container networks at all.
- If access **bypasses** the simulated network (port publishing, `docker exec`), the lessons that matter most disappear. You'd never learn that port 22 must be open, that the subnet needs a route to an internet gateway, or that the instance needs a public IP.

LocalStack illustrates the problem: SG ingress rules become host port mappings at launch, and later rule changes don't apply.

## Options considered

### Option A: Publish instance ports to the host
- Pros: plain `ssh localhost -p 2201`.
- Cons: bypasses routes, NACLs, and SGs; changes need a restart; port conflicts.

### Option B: Add host routes for the public IP pool through the appliance
- Pros: plain `ssh 203.0.113.10`.
- Cons: modifies host routing, which violates [ADR-0003](0003-appliance-container-packaging.md); impossible on Docker Desktop.

### Option C: Exec-only access
- Pros: trivial to build.
- Cons: bypasses networking entirely and teaches nothing about access paths.

### Option D: A simulated internet, with every learner connection originating inside it
- Pros:
  - Works the same on every platform.
  - Each connection crosses the internet gateway's NAT, NACLs, and SGs, exactly like a connection from your laptop to AWS.
  - "My IP" is a stable, known address.
- Cons:
  - Plain `ssh` needs a one-time `ProxyCommand` setup.
  - Connections take an extra hop through the API.
  - Your real public IP is never used.

## Decision

Nephos simulates the internet in the `nx-edge` namespace and originates all learner traffic there.

**Addresses (documentation ranges, never routed on the real internet)**

| Address | Role |
|---|---|
| **198.51.100.10** | Learner vantage address. Offered as "My IP" in the console and CLI (`--cidr my-ip`). |
| **203.0.113.0/24** | Pool for public IPs and Elastic IPs. |
| **198.51.100.80** | Internet test endpoint `echo.nephos.test`: HTTP on 80, TCP on 443, ICMP echo. Lab checks use it offline. |

**Access paths.** Every tunnel is an authenticated WebSocket to `nephosd`, which dials from inside `nx-edge` with source address 198.51.100.10.

| Command / feature | What it does |
|---|---|
| `nephos ssh <instance>` | Resolves the public IP and uses your key file. |
| `nephos proxy <host> <port>` | Tunnel helper for OpenSSH's `ProxyCommand`. |
| `nephos ssh-config` | Writes an opt-in `Host 203.0.113.*` block so plain `ssh -i key.pem ubuntu@203.0.113.10` works. |
| `nephos forward <public-ip>:<port>` | Exposes a local port such as `127.0.0.1:18080`. |
| Console **Instance Connect** | Browser terminal: a short-lived key is pushed through the console channel, then SSH dials from the edge. |
| Console **HTTP preview** | View an instance's web page through the edge. |

Private instances are reachable only through a bastion (`ProxyJump`), as in AWS.

**Out-of-band access.** `nephos console <instance>` is a serial-console analog: it bypasses networking and is labeled as such. `nephos instance console-output` shows boot logs. These are how a learner recovers from a lockout.

**Real internet egress.** `nx-edge` masquerades out through the appliance uplink, so `apt install` works. `nephos up --sealed` disables it; only the simulated endpoints remain.

## Consequences

- Positive:
  - Identical access on Linux, macOS, and Windows.
  - Lockouts are real, so lessons stick.
  - Lab checks can use the same vantage points as the learner.
- Negative / costs:
  - Documentation must teach the ProxyCommand setup.
  - Services on instances aren't reachable from other devices on the LAN. This is deliberate for safety ([RISKS](../RISKS.md) S4).
- Follow-ups: the probe engine (M3) and `explain` (M5) model "internet" and "My IP" as first-class endpoints.

## Validation

M3 acceptance criteria:

- `ssh -i key.pem ubuntu@203.0.113.x` works through `nephos ssh-config` on Ubuntu, WSL2, and macOS.
- Deleting the SG rule for port 22 makes new SSH attempts time out.
- Deleting the subnet's `0.0.0.0/0` route to the internet gateway breaks SSH.
- `nephos console` still works while network access is broken.
