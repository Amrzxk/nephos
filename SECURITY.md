# Security policy

## Reporting a vulnerability

**Please do not open a public issue for a security problem.**

Report it privately through GitHub Security Advisories:
<https://github.com/Amrzxk/nephos/security/advisories/new>.

If that is not available to you, email <amrzakariya2018@gmail.com> with "nephos
security" in the subject.

Nephos is maintained by one person as an unpaid project, so please set your
expectations accordingly: you should get an acknowledgement within **7 days** and
an assessment within **30 days**. Fixes are released as quickly as the severity
warrants. You will be credited in the advisory unless you ask not to be.

## Supported versions

Nephos has not reached v0.1.0 yet. Until it does, only the `main` branch is
supported, and there are no security backports.

## What Nephos is, before you report

Two properties are **by design**, documented, and not vulnerabilities in
themselves. A concrete way to exceed them, however, is very much a vulnerability
worth reporting.

### The appliance is a privileged container

`nephos up` starts one `--privileged` container. On a Linux host that is
effectively root; on Docker Desktop the blast radius is the Docker VM. This is
the same tradeoff kind and minikube make, and it is what lets Nephos create
network namespaces and nftables rules without touching your host
([ADR-0003](docs/adr/0003-appliance-container-packaging.md),
[RISKS S1](docs/RISKS.md#s1-the-privileged-appliance-is-root-equivalent-on-linux-hosts)).

Nephos constrains itself inside that privilege, and **these are the promises
worth testing**:

- it never uses the host network or PID namespace, and never bind-mounts host paths;
- it changes only per-namespace sysctls, never kernel-global ones;
- it never loads kernel modules;
- it writes only to `~/.nephos` on the host and to the `nephos-data` volume;
- `nephosd` is the only listening component, bound to `127.0.0.1`.

Anything that breaks one of those is a bug, and a security one.

### Instance root holds CAP_NET_ADMIN in its own namespace

You are root inside an instance, and you can run `ufw` or `nft` there. That is
the point: a learner has to be able to lock themselves out and recover. It also
exposes kernel netlink and nf_tables paths from a user namespace
([RISKS S3](docs/RISKS.md#s3-kernel-attack-surface-through-instance-owned-network-namespaces)).
Keep your host kernel updated.

## Especially interesting to us

- Escaping an instance into the appliance, or the appliance onto the host.
- Reaching the API on `127.0.0.1:7788` without the bearer token, or from a web
  page (CSRF, DNS rebinding, a missed `Origin` or `Host` check).
- Any path that reaches an instance while **bypassing** security groups, network
  ACLs, or route tables — other than the serial console, which bypasses the
  network deliberately and says so.
- Nephos reporting a rule as enforced when it is not. Nephos is supposed to fail
  closed and mark the resource `failed`; anything that makes it fail open is
  serious even when it is not exploitable, because people learn from it.
- Leaking the API token, a private key, or instance state outside the volume.

## Out of scope

- The absence of multi-tenant isolation. Nephos is single-user by design and is
  not a sandbox for hostile code ([VISION](docs/VISION.md) non-goals).
- Anything requiring the attacker to already be root on the host.
- Instances being reachable from your own machine. That is the product.
- Denial of service caused by the learner's own configuration inside an
  instance, which resource limits are expected to contain rather than prevent.
