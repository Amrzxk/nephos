# Developing Nephos

This is the setup guide. For *what to build and why*, read
[AGENTS.md](../AGENTS.md), [ARCHITECTURE.md](ARCHITECTURE.md), and
[ROADMAP.md](ROADMAP.md). For how to submit, read
[CONTRIBUTING.md](../CONTRIBUTING.md).

## What you need

| | |
|---|---|
| **Go** | The version pinned in `go.mod` (currently 1.27.1) |
| **Docker** | Docker Engine on Linux, or Docker Desktop on Windows/macOS. Must be **rootful** and on a **cgroup v2** host |
| **golangci-lint** | v2.13.2, for `make lint` |
| **Node 22** | Only from M6 onward, for the web console |

Integration and end-to-end tests additionally need Docker and the ability to run
a **privileged** container.

## Linux

```bash
sudo apt-get install -y build-essential git jq
# Go: use the official tarball, not the distribution package, which lags.
curl -fsSLO https://go.dev/dl/go1.27.1.linux-amd64.tar.gz
sudo rm -rf /usr/local/go && sudo tar -C /usr/local -xzf go1.27.1.linux-amd64.tar.gz
echo 'export PATH="/usr/local/go/bin:$HOME/go/bin:$PATH"' >> ~/.bashrc && . ~/.bashrc

go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.13.2

git clone https://github.com/Amrzxk/nephos.git ~/src/nephos
cd ~/src/nephos && make ci
```

This is the primary CI target.

## Windows with WSL2 and Docker Desktop

This is the maintainer's platform, and it has three traps worth stating plainly.

**1. Keep the working copy on the WSL ext4 filesystem.** Use `~/src/nephos`, not
`/mnt/c/...` or `/mnt/f/...`. The Windows mount is slow, does not preserve Unix
permissions, and will mangle line endings. `.gitattributes` enforces LF, but the
filesystem is still the wrong place for the repository.

**2. There are usually several WSL distributions, and the default is often
`docker-desktop`.** Always name the one you mean:

```powershell
wsl -l -v                    # find yours
wsl -d Ubuntu-24.04          # not just `wsl`
```

**3. Do not pass inline scripts from PowerShell into `bash -lc`.** PowerShell
mangles the quoting and silently corrupts the command. Write the script to a file
and run that:

```powershell
wsl -d Ubuntu-24.04 -- bash /mnt/c/path/to/script.sh
```

Setup inside WSL is then identical to the Linux instructions above. Enable
Docker Desktop's WSL integration for your distribution so that `docker` works
from inside WSL.

If you lose the password for your WSL user, `wsl -d Ubuntu-24.04 -u root` gets
you a root shell without one.

## macOS

Docker Desktop or OrbStack, then the Linux instructions with Homebrew in place of
apt. macOS is **best effort** until v0.1 release QA
([ARCHITECTURE §12](ARCHITECTURE.md#12-platform-support)).

## The build targets

```bash
make build        # the three binaries into bin/
make test         # unit and golden tests
make test-race    # the same under the race detector (needs cgo; Linux)
make test-update  # regenerate golden files
make lint         # golangci-lint
make fmt-check    # fail if anything is unformatted
make cross        # all six release targets, CGO_ENABLED=0
make dev-ami      # local Ubuntu 24.04 development AMI OCI archive
make appliance    # local appliance image, after make dev-ami
make generate     # generated API server and Go client
make ci           # everything a pull request runs
make clean
```

`make generate`, `make dev-ami`, and `make appliance` now work. `make web` and
`make e2e` remain placeholders until their owning slices are implemented.

## M1 local appliance workflow

Slices 1 and 2 are implemented and merged. Slice 3 is implemented in
[PR #6](https://github.com/Amrzxk/nephos/pull/6): instance/ENI state, fail-closed boot plumbing, real
cross-subnet ping, a 512-task instance ceiling, and interactive PTY/non-PTY
command consoles. WSL2/Docker Desktop tests and native Ubuntu
[CI run 36973066755](https://github.com/Amrzxk/nephos/actions/runs/36973066755)
pass, including the native host nftables check. The
[roadmap](ROADMAP.md#m1-thinnest-end-to-end-slice-two-instances-ping) tracks
completion. Retained instance-root restart recovery, reset, leak closure and
the full replayable e2e demo remain slice 4; do not use the roadmap's complete
restart/reset demo as a slice-3 acceptance claim.

Run these commands inside native Linux or WSL2, not PowerShell. Docker must be
rootful, support cgroup v2 and privileged containers, expose a Linux 5.15+
kernel, and have **at least 6 GiB** available so the default 4 GiB appliance
fits alongside Docker and BuildKit. The `nephos-data` volume needs at least
2 GiB free. On WSL2, change the WSL/Docker Desktop memory allocation and
restart WSL before relying on the new limit. Check what Docker actually sees:

```bash
docker info --format 'Memory bytes={{.MemTotal}} Cgroups={{.CgroupVersion}} CPUs={{.NCPU}}'
```

Build the two images locally, then start the appliance:

```bash
make dev-ami         # dist/ubuntu-24.04.oci.tar; creates/reuses the nephos-oci Buildx builder
make appliance       # nephos-appliance:dev, containing the AMI archive
make build           # bin/nephos and appliance binaries
bin/nephos up        # 4 GiB, 2 CPUs, 4096 PIDs by default
bin/nephos status
awk '{printf "header = \"Authorization: Bearer %s\"\n", $0}' ~/.nephos/credentials \
  | curl -fsS --config - http://127.0.0.1:7788/v1/version
bin/nephos down      # stop only; preserve the data volume and token
bin/nephos up        # reuses the same container, volume, and token
```

`make dev-ami` uses a dedicated `docker-container` Buildx builder, not the
user's default builder. `make appliance` deliberately refuses to build if
the AMI archive is absent; rebuild both after changing the AMI definition.
The CLI does not download or build either image. Publishing the AMI belongs
to M2. M1 provides VPC, subnet and instance commands. The sole workspace is
implicit and named `default`.

Create a VPC explicitly, then create a subnet inside it. Names are
case-sensitive and may contain spaces or UTF-8 characters:

```bash
bin/nephos vpc create 'Lab East' --cidr-block 10.0.0.0/16 --wait
bin/nephos subnet create 'App α' --vpc 'Lab East' \
  --cidr-block 10.0.1.0/24 --availability-zone local-1a --wait
bin/nephos vpc describe 'Lab East' -o json
bin/nephos subnet list --limit 50 -o json
```

`vpc` and `subnet` support `create`, `list`, `describe`, and `delete`.
Describe/delete references accept an ID or exact name. Lists return one
stable ID-ordered page; `--limit` and `--page-token` select later pages.
`-o json` prints the API object or page. `--wait` polls until availability,
failure, or timeout (two minutes by default, overridden with `--timeout`).
Delete with `--wait` succeeds only after the resource disappears. Resource
commands use the locally stored token and never contact the Docker Engine.
For `delete --wait -o json`, the output is the original API-accepted deletion
object; the successful exit code confirms that the later GET returned 404.

Run two instances in different subnets, then send real packets through their
VPC. M1 fixes the type at `t3.micro` (2 vCPU, 1 GiB, 512 tasks):

```bash
bin/nephos subnet create 'App β' --vpc 'Lab East' \
  --cidr-block 10.0.2.0/24 --availability-zone local-1b --wait
bin/nephos instance run one --subnet 'App α' --wait -o json
bin/nephos instance run two --subnet 'App β' --wait -o json
bin/nephos instance list --limit 50 -o json
bin/nephos console one -- ping -c 3 10.0.2.4
bin/nephos console one                 # local terminal required; interactive Bash
bin/nephos instance terminate one --wait
bin/nephos instance terminate two --wait
```

`instance run|list|describe|terminate` resolves exact case-sensitive names or
IDs; name resolution reads all pages. `--wait` reports running, failed reason,
removal or a timeout; `--timeout` overrides the two-minute default. Run retries
retain one idempotency key. `terminate --wait -o json` returns the original
accepted deletion object once removal has been confirmed.

Console command mode uses exact argv, no implicit shell and no PTY. Redirect
stdin/stdout normally; stdout/stderr bytes remain distinct and remote exit
codes propagate to the CLI. The serial/exec label goes to stderr. Interactive
mode runs `/bin/bash`, transfers resize changes and restores terminal settings
on exit/error/interruption. Both use the same authenticated, instance-scoped
[WebSocket protocol](../api/console-v1.md); packets sent inside the instance
still traverse the VPC. A connection without a valid final exit and normal
closure fails. Native Windows/macOS console runtime QA is not yet established;
project commands remain supported inside Linux or WSL2. Pending pipe/terminal
output writes are bounded to ten seconds and observe cancellation, including
when stderr cannot accept diagnostics. Linux uses private nonblocking stream
handles without changing caller flags; Darwin's best-effort implementation
temporarily sets shared output nonblocking flags and restores them on normal
session cleanup. Redirected regular files keep their offset/append behavior;
blocking filesystem I/O is not a poller-cancellable stream.

### Activating rebuilt development images

An existing Docker container keeps the image it was created from. Ordinary
`nephos down && nephos up` deliberately restarts that same container, so
rebuilding `nephos-appliance:dev` alone does not activate new daemon code.
For an **appliance-only** change, build first, then replace only the stopped
Nephos-owned container. The data volume and API token remain intact:

```bash
make appliance
bin/nephos down
test "$(docker inspect -f '{{index .Config.Labels "io.nephos.appliance"}}' nephos)" = true
docker rm nephos
bin/nephos up
```

The development AMI is cached by Podman **inside** `nephos-data`. Rebuilding
its OCI archive and the appliance image does not replace that cached AMI.
For an **AMI change** during M1 development, the supported refresh is a fresh
volume. This **permanently deletes all Nephos state in that volume**,
including nested instances and the previous API token. Back up anything you
need before running it; do not use this procedure on a volume you want to keep:

```bash
make dev-ami
make appliance
bin/nephos down
test "$(docker inspect -f '{{index .Config.Labels "io.nephos.appliance"}}' nephos)" = true
test "$(docker volume inspect -f '{{index .Labels "io.nephos.appliance"}}' nephos-data)" = true
docker rm nephos
docker volume rm nephos-data
bin/nephos up
```

If no `nephos` container exists yet, just build and run `bin/nephos up`.
The M1 slice-4 purge/reset commands will make the data-destructive case less
manual; they are not available in this slice.

The API token lives in the `nephos-data` volume. On first startup, the
CLI copies it through Docker's archive API to
`~/.nephos/credentials` with mode `0600`. Keep that file private; do
not paste its contents into an issue or put the token in an environment
variable or command argument. The API binds only to
`127.0.0.1:7788`; only `/v1/health` is public.

The appliance is privileged and should be treated as root-equivalent on a
native Linux host. The test scripts refuse to touch a pre-existing
`nephos` container or `nephos-data` volume; the CLI smoke uses a
temporary HOME so it cannot replace your normal credential:

```bash
bash tests/appliance-smoke.sh
bash tests/appliance-cli-smoke.sh
bash tests/vpc-subnet-smoke.sh
bash tests/instance-ping-smoke.sh
```

The VPC/subnet script requires locally built images and `bin/nephos`, `jq`, Docker,
and permission to run a privileged appliance. It creates overlapping-CIDR
VPCs in separate namespaces, checks subnet gateways, authentication, durable
events, pagination, restart persistence, and deletion. It snapshots host
network objects before the appliance starts and after its test-owned
container/volume are removed. Native CI installs `nftables` and requires
readable host rules; a WSL2 host without `nft` reports that part as unavailable.
The instance script adds real bidirectional pings, an overlapping-VPC fixture
with a remote-only destination counter, source spoof/drop witnesses, a
pre-PID-1 hook observer and forced failure, console NUL bytes/EOF/exit 7, the
bounded 512-task probe, and instance/network teardown. Its helpers are built
locally and copied only into the test-owned appliance; they are never shipped.
Failure evidence stays in the printed `/tmp/nephos-instance-smoke.*` directory;
cleanup verifies container identity and volume creation time/ownership labels.
The [M1 connectivity matrix](tests/M1-connectivity-matrix.md) records the
exact packet paths and distinguishes local 4A restart proof from pending reset.

### Slice-4 recovery progress and planned reset workflow

**4A is implemented locally; 4B–4E remain planned.** The
[4A checkpoint](tests/M1-slice4a-local-verification.md) records unit/race,
privileged integration, and WSL2/Docker Desktop retained-root/ENI/ping proof.
Implementation has not been pushed or merged; native Ubuntu CI is pending.
The slice-4
[design](superpowers/specs/2026-10-02-m1-slice-4-recovery-reset-design.md),
[implementation plan](superpowers/plans/2026-10-02-m1-slice-4-recovery-reset.md),
and [verification plan](tests/M1-slice4-verification-plan.md) describe the next
work. M1 remains incomplete. `make e2e` is still a failing placeholder;
`nephos reset`, `nephos reset --hard`, and the replayable `docs/demos/M1.md`
are future deliverables. The examples below specify future behavior and are
not a runnable acceptance procedure for the current tree:

```text
# Full future acceptance workflow; reset/e2e are not yet implemented.
make dev-ami
make appliance
make build
nephos down
nephos up             # same instance IDs, ENIs, MACs, private IPs, roots and files
nephos reset          # waits for durable deletion and an in-appliance leak check
nephos reset --hard   # remove verified old container/volume, then start fresh
make e2e             # replay the demo using freshly built images/binaries above
```

4A recovery keeps the original Podman container and writable root;
it fails the instance visibly if a provisioned root is missing, rather than
booting an empty replacement. Fresh private runtime directories at
`/var/lib/nephos/runroot`, `/run/libpod`, and `/run/crun` are prepared before
the first Podman call on each whole-appliance start. The configured durable
and runroot paths are validated before serving the daemon; graphroot is
retained. A daemon-only restart must
preserve the live runtime directories.
An already-running instance needs verified ENI plumbing before it is reported
running; healthy observation must not flap links or reset source-check counters.
4B will join console cleanup and gracefully stop nested instances before
stopping Podman; the current 4A restart smoke does not prove graceful shutdown.

Soft reset is planned as a durable SQLite operation. Its transaction fences
resource creation, and its worker deletes instances before subnets before
VPCs. The CLI waits for completion, but disconnecting or timing out does not
cancel the admitted operation. Startup resumes unfinished work. Replaying a
completed operation's idempotency key returns its recorded result without
deleting resources created afterward. Leak failure keeps the create fence
until an explicit retry of that failed operation succeeds. AMI cache, event
history, request replay history and allocated kernel-index history remain.
The default workspace stays empty; M1 has no default VPC.
The future `reset --status` reads progress; `reset --retry` explicitly resumes
the failed operation by its ID. The ordinary reset waiter defaults to two
minutes, with a `--timeout` override; its timeout does not cancel the worker.

Hard reset is planned to work even when the API is unhealthy or SQLite cannot
open. It verifies both old Docker objects and any volume consumers before
deletion, pins the old container ID, rechecks the volume's creation identity,
and refuses foreign consumers. It starts fresh only after both old objects
are confirmed absent; partial purge is an error, not a reason to call `up`.
The fresh appliance gets a new token and an atomic mode-0600 credential
replacement. Actual old appliance CPU, memory and PID limits are retained
unless the caller explicitly overrides them.

Future e2e runs must build the development AMI, appliance, CLI and test-only
helpers from the candidate source, and record exact source/image/package
versions. They use a private credential directory and refuse pre-existing
objects. The leak assertion runs inside the still-live appliance after joined
controller/GC/hook/console barriers, before test-owned Docker teardown.
Snapshots around inner recovery/reset operations use a stable post-`up`
host baseline; a separate comparison covers the whole appliance lifecycle.
Native Ubuntu 24.04 CI must read host nftables. Windows 10 with WSL2 and
Docker Desktop requires a fresh manual full-demo run; an unavailable WSL
nftables check must be recorded explicitly. Cross-builds do not establish
native Windows/macOS console behavior. Slice-3 smoke results remain their
historical evidence and do not close these slice-4 gates.

### Why `make test-race` sets `CGO_ENABLED=1`

Everything Nephos *ships* is built with `CGO_ENABLED=0`, so release tooling can
cross-compile all six targets with a pure-Go SQLite driver
([ADR-0002](adr/0002-go-for-control-plane-and-cli.md)). The race detector needs
cgo, but a race-detector test run is not a shipped artifact — and it is how
[RISKS T8](RISKS.md#t8-go-and-network-namespace-pitfalls) (goroutines migrating
between network namespaces) gets caught. Do not "fix" this by dropping `-race`.

## Running the spikes

M0's spikes are throwaway experiments that validate the riskiest assumptions
before M1 starts. They live on `spike/*` branches; their **reports** are the
deliverable and live in [docs/spikes/](spikes/).

```bash
./spikes/run.sh all     # or sp1, sp2, sp3, sp4
```

They need Docker and will start a privileged container. They clean up after
themselves; if one is interrupted, `./spikes/run.sh clean` removes what it left.

The same spikes run on a native-Linux GitHub runner through the **Spikes**
workflow (`workflow_dispatch`), which is how the "passes on Ubuntu 24.04 *and*
WSL2" acceptance criterion gets both of its legs.

## Layout

The repository structure and the dependency rules between packages are specified
in [ARCHITECTURE §13](ARCHITECTURE.md#13-repository-structure). The ones the
linter enforces for you:

- `internal/network` and `internal/compute` never import `internal/service` or
  `internal/apiserver`;
- `internal/explain`, `internal/semantics`, and the firewall renderer do no I/O
  and use no clock or randomness;
- library code logs with `log/slog`, never `fmt.Print*`;
- `context.Context` is the first parameter, and `context.Background()` appears
  only in `main` and tests.

A rule that still needs explicit review beyond the linter: **only
`internal/network/netns` may switch network namespaces**, and a thread that
switched namespace is never returned to the scheduler. The package now exists;
its unit and privileged integration tests cover the namespace boundary.

## Troubleshooting

**`-race requires cgo`** — you ran `go test -race` with `CGO_ENABLED=0`. Use
`make test-race`, which sets it correctly.

**`golangci-lint not found`** — `make lint` prints the exact `go install` line.

**Docker works in PowerShell but not in WSL** — enable Docker Desktop's WSL
integration for your distribution (Settings → Resources → WSL integration).

**Integration tests fail with permission errors** — they need a privileged
container. Rootless Docker is not supported
([ARCHITECTURE §12](ARCHITECTURE.md#12-platform-support)).
