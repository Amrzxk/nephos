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
make ci           # everything a pull request runs
make clean
```

`make generate`, `make web`, `make appliance`, and `make e2e` exist but report
which milestone fills them in.

M1's first runnable contributor workflow will use documented local builds of
the appliance and `ubuntu-24.04` development AMI before `nephos up`. Image
publishing is an M2 task. The M1 implementation will replace the current
`make appliance` and `make e2e` placeholders and document the exact image-build
commands when they work; these are not available in the M0 scaffold.

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

A rule the linter cannot yet enforce, because the package does not exist: **only
`internal/network/netns` may call `setns`**, and a thread that switched namespace
is never returned to the scheduler.

## Troubleshooting

**`-race requires cgo`** — you ran `go test -race` with `CGO_ENABLED=0`. Use
`make test-race`, which sets it correctly.

**`golangci-lint not found`** — `make lint` prints the exact `go install` line.

**Docker works in PowerShell but not in WSL** — enable Docker Desktop's WSL
integration for your distribution (Settings → Resources → WSL integration).

**Integration tests fail with permission errors** — they need a privileged
container. Rootless Docker is not supported
([ARCHITECTURE §12](ARCHITECTURE.md#12-platform-support)).
