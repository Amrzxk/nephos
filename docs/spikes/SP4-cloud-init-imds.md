# SP4: cloud-init against a Nephos metadata service

- **Status:** **PASS** (15 of 15 assertions)
- **Date:** 2026-09-18
- **Mitigates:** [RISKS](../RISKS.md) T2 (systemd and cloud-init quirks inside containers)
- **Supports:** [ARCHITECTURE §5.1](../ARCHITECTURE.md#51-how-each-cloud-concept-is-implemented-on-the-host) (VPC DNS and IMDS), M2
- **Reproduce:** `./spikes/run.sh sp4`

## Question

Will **stock, unmodified** cloud-init accept a metadata service that Nephos
serves at 169.254.169.254 inside a VPC namespace?

If it will not, user data and SSH key injection have to be built some other way
and M2 changes shape entirely — so this is worth knowing before M1 starts, not
after.

## Results

| Criterion | Verdict | Evidence |
|---|---|---|
| The VPC namespace holds 169.254.169.254 | PASS | on the router's dummy interface |
| `instance-id` is served | PASS | `i-0a1b2c3d4e5f67890` |
| The availability zone uses Nephos's own naming | PASS | `local-1a`, not an AWS region |
| The instance identity document is well-formed | PASS | the Ec2 datasource probes it |
| The SSH public key is served | PASS | `public-keys/0/openssh-key` |
| User data is served | PASS | `/latest/user-data` |
| A PUT to `/latest/api/token` returns a token | PASS | IMDSv2 |
| A token-authenticated request succeeds | PASS | |
| With `http_tokens=required`, IMDSv1 gets **401** | PASS | |
| With `http_tokens=required`, a token request still succeeds | PASS | |
| The instance boots and reaches the service | PASS | `eth0` holds 10.60.1.4 |
| **cloud-init completes without error** | **PASS** | `status: done` |
| **cloud-init selects the Ec2 datasource** | **PASS** | `DataSourceEc2Local` |
| **User data runs on first boot** | **PASS** | marker file written |
| **The SSH public key is installed for the default user** | **PASS** | `ssh-ed25519 …` in `ubuntu`'s `authorized_keys` |

Stock cloud-init, with no patches, accepts a Nephos-served metadata service.

## Three findings that would each have broken M2

These are the whole value of the spike. Every one of them produced a *plausible
but wrong* result first.

### 1. IMDS must serve dated API versions, not just `/latest`

The most important finding. cloud-init does not ask for `/latest/…`. It fetches
`/` to get the list of supported API versions, then addresses metadata under a
**dated** one:

```
GET /2009-04-04/meta-data/instance-id
```

A service that answers only `/latest` responds perfectly to `curl`, passes every
hand-written endpoint test, and still makes cloud-init give up and fall back to
`DataSourceNone` — which reports `status: done`, because falling back is not an
error. The instance then boots with no user data, no SSH key, and a "healthy"
cloud-init.

The IMDS now serves the version index at `/` and accepts any known version
prefix. **M2's IMDS must do the same, and needs a test that asserts a dated path
works**, not just `/latest`.

### 2. `ds-identify` disables cloud-init in containers before it ever looks

cloud-init's `ds-identify` runs first and decides whether cloud-init should run
at all. In a container it finds no recognised platform and writes `disabled`, so
the `datasource_list` in `/etc/cloud/cloud.cfg.d` is never consulted and
`cloud-init status` answers `disabled`.

Nephos instances **are** containers by design ([ADR-0004](../adr/0004-instances-as-system-containers.md)),
so this would have disabled user data and key injection on every instance.

Fixed in the AMI:

```
policy: enabled,found=all,maybe=all,notfound=enabled   # /etc/cloud/ds-identify.cfg
```

### 3. cloud-init merges config lists by appending, so overlays do not remove anything

`cc_install_hotplug` runs `udevadm control --reload-rules`, which fails because
udev is masked in the AMI — correctly, per RISKS T2. One failing module makes
`cloud-init status` report `error` even though everything that matters
succeeded.

The obvious fix — drop the module in a `/etc/cloud/cloud.cfg.d/` overlay —
**does not work**: cloud-init merges lists by *appending*, so the original entry
survives and the module still runs. The module list has to be edited in
`/etc/cloud/cloud.cfg` itself.

It also lives in `cloud_final_modules` on Ubuntu 24.04, not
`cloud_config_modules`, so the AMI filters all three module lists and the build
fails if `install_hotplug` is still present afterwards.

## Deviations worth recording

- **Hostname.** cloud-init warns `Failed to non-persistently adjust the system
  hostname`, and the instance keeps its container ID as its hostname. Setting
  the hostname needs privileges the instance does not have in its UTS namespace.
  ARCHITECTURE §5.1 promises private DNS names like
  `ip-10-0-1-10.local-1.compute.internal`; **M2 must set the hostname at
  container creation** rather than relying on cloud-init to do it.
- **`netplan apply` fails** inside the instance, harmlessly: the ENI is already
  configured by the hook before cloud-init runs, so there is nothing for it to
  do. Worth suppressing so logs stay readable.
- **`growpart` and `resizefs` are skipped** — no block device. Expected, and
  consistent with ADR-0004's documented deviations.

## A harness bug worth recording too

The first run of this spike reported "user data is served" as a **failure** when
it was being served correctly. The assertion helper runs its command through
`eval`, and the user data — a shell script — contained a `>` redirect, which
executed against the harness's own filesystem.

Fetched content must never be interpolated into an eval'd string.
`spike::assert_file_contains` now exists for exactly this, matching with
`grep -F` against a file. The same hazard will exist anywhere Nephos evaluates
lab check output or user data.

## Verdict

**Stock cloud-init works against a Nephos IMDS**, including the IMDSv2 token
flow and `http_tokens=required` returning 401. User data runs and SSH keys are
installed on first boot.

M2 can build the real IMDS and AMI pipeline against this shape, provided it
carries the three findings above.

## Outstanding

- [ ] **Native Linux leg** — the `Spikes` workflow covers it.
- [ ] Hostname must be set at container creation (see Deviations).
- [ ] The spike serves one instance and identifies it by nothing. The real IMDS
      identifies the caller by source address and incoming interface; that has
      not been exercised.
- [ ] No VPC DNS resolver here. SP3 proved a UDP socket can be opened on the
      resolver address inside the namespace, but nothing resolves names yet.
