# M1 Slice 1: Local Appliance and API Bootstrap Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A contributor builds the development AMI and appliance locally, runs `nephos up`, and reaches authenticated health and version endpoints.

**Architecture:** The CLI manages one named appliance through the Docker Engine API and obtains its bearer token through Docker's archive API. The appliance imports a bundled OCI AMI into volume-backed Podman storage, delegates cgroup controllers, and starts `nephosd`; the daemon serves the first generated OpenAPI handlers. Later M1 slices add resources without changing this bootstrap boundary.

**Tech Stack:** Go 1.27.1 with `CGO_ENABLED=0`, OpenAPI 3.1, `oapi-codegen` v2.8.0, Moby Go client v0.6.0, Docker Buildx, Debian trixie appliance, Ubuntu 24.04 development AMI.

**Spec:** `docs/superpowers/specs/2026-09-23-m1-two-instances-ping-design.md`

## Global Constraints

- Keep the appliance in its own network and PID namespaces with a private cgroup namespace; no host bind mounts or global sysctl changes.
- Use one implicit `default` workspace, but retain `/v1/workspaces/default/...` for future resource routes.
- The API binds to `127.0.0.1:7788` on the host; `/v1/health` alone is unauthenticated.
- The token is generated inside the appliance, persists on `nephos-data`, and is copied to `~/.nephos/credentials` mode `0600` without entering environment variables, logs, or command arguments.
- Build the `ubuntu-24.04` development AMI and appliance locally before `nephos up`; no implicit image build and no published AMI.
- Keep `CGO_ENABLED=0` for shipped binaries, follow `AGENTS.md` layering, and never import spike modules into production packages.

## Review Focus

- A missing local image must yield the exact build command and leave no container or volume.
- Startup must fail closed if `memory`, `pids`, or `cpu` is unavailable for delegation.
- A stopped existing appliance must reuse its token and volume; no second appliance may be created.
- A malformed or missing token must reject `/v1/version`, while `/v1/health` remains readable.
- Docker archive extraction must reject unexpected tar entries and preserve `0600` credentials on supported Linux/WSL M1 workflows; native-Windows ACL polish belongs to M8 QA.

---

## File map

- `api/openapi.yaml`: canonical M1 HTTP contract, beginning with health and version.
- `api/server.cfg.yaml`, `api/client.cfg.yaml`: pinned generator configurations.
- `internal/apiserver/generated/server.gen.go`, `pkg/client/client.gen.go`: generated server and public client; never edit by hand.
- `internal/apiserver/server.go`, `auth.go`, `server_test.go`: HTTP composition, bearer authentication, readiness, and contract tests.
- `internal/credentials/token.go`, `token_test.go`: volume-backed token creation and validation.
- `internal/appliance/engine.go`, `docker.go`, `docker_test.go`: Docker-facing lifecycle interface and Moby adapter.
- `internal/appliance/lifecycle.go`, `lifecycle_test.go`, `credential.go`, `credential_test.go`: preflight, idempotent up/down/status, and safe token archive extraction.
- `cmd/nephos/main.go`: add lifecycle verbs while preserving the existing version command.
- `cmd/nephosd/main.go`: start and shut down the API server.
- `images/dev-ami/Dockerfile`, `images/appliance/Dockerfile`, `images/appliance/entrypoint.sh`, `images/appliance/storage.conf`, `images/appliance/containers.conf`: production local images and fail-closed startup.
- `Makefile`, `.gitignore`, `.github/workflows/ci.yml`, `docs/DEVELOPMENT.md`: reproducible builds, generated-code checks, smoke validation, and contributor instructions.
- `tests/appliance-smoke.sh`: a real-Docker assertion of isolation, health, credentials, and restart behavior.

### Task 1: Establish the spec-first bootstrap API

**Files:** Create `api/openapi.yaml`, `api/server.cfg.yaml`, `api/client.cfg.yaml`, generated files under `internal/apiserver/generated/` and `pkg/client/`; modify `Makefile` and `go.mod`/`go.sum`.

**Interfaces:** Produces `GET /v1/health` with `{"status":"ready"}` or `{"status":"starting"}`, and `GET /v1/version` with `api_version`, `build_version`, and `build_commit`. Later API tasks extend this one source of truth.

- [ ] **Step 1: Make the missing-contract check fail.** Run `test -f api/openapi.yaml && test -f internal/apiserver/generated/server.gen.go && test -f pkg/client/client.gen.go`; expect nonzero.
- [ ] **Step 2: Add the OpenAPI 3.1 contract and generator configurations.** Start with these exact shapes and status codes, using `operationId: getHealth` and `operationId: getVersion`:

```yaml
openapi: 3.1.0
info:
  title: Nephos API
  version: 0.1.0
paths:
  /v1/health:
    get:
      operationId: getHealth
      responses:
        '200':
          description: Appliance ready
          content:
            application/json:
              schema:
                $ref: '#/components/schemas/HealthResponse'
        '503':
          description: Appliance starting
          content:
            application/json:
              schema:
                $ref: '#/components/schemas/HealthResponse'
  /v1/version:
    get:
      operationId: getVersion
      security:
        - bearerAuth: []
      responses:
        '200':
          description: API and appliance build version
          content:
            application/json:
              schema:
                $ref: '#/components/schemas/VersionResponse'
        '401':
          $ref: '#/components/responses/Unauthorized'
components:
  securitySchemes:
    bearerAuth:
      type: http
      scheme: bearer
  schemas:
    HealthResponse:
      type: object
      additionalProperties: false
      required: [status]
      properties:
        status:
          type: string
          enum: [starting, ready]
    VersionResponse:
      type: object
      additionalProperties: false
      required: [api_version, build_version, build_commit]
      properties:
        api_version: {type: string}
        build_version: {type: string}
        build_commit: {type: string}
    Error:
      type: object
      additionalProperties: false
      required: [code, message]
      properties:
        code: {type: string}
        message: {type: string}
        resource_id: {type: string}
        docs_url: {type: string}
  responses:
    Unauthorized:
      description: Missing or invalid bearer token
      content:
        application/json:
          schema:
            $ref: '#/components/schemas/Error'
```

Use `package: generated` with `generate: {models: true, std-http-server: true}` for the server, and `package: client` with `generate: {models: true, client: true}` for the client. Add `oapi-codegen` v2.8.0 to Go's tool dependencies and make `make generate` invoke both configurations. `make generate-check` must compare the two generated files before and after regeneration, so unrelated working-tree edits cannot create false failures.

- [ ] **Step 3: Generate and verify.** Run `make generate`, `go mod tidy`, `go test ./...`, and `make generate-check`; expect exit 0 and generated-code freshness. Commit this self-contained contract/generation change with DCO sign-off.

### Task 2: Serve health and authenticated version from the appliance daemon

**Files:** Create `internal/credentials/token.go`, `token_test.go`, `internal/apiserver/server.go`, `auth.go`, `server_test.go`; modify `cmd/nephosd/main.go`.

**Interfaces:** `credentials.LoadOrCreate(path string) (string, error)` returns a persistent random token. `apiserver.New(token string, build version.Info, ready func() bool) http.Handler` returns the generated-route handler wrapped by auth. `nephosd` listens on `:7788` *inside* the appliance and exits on signal.

- [ ] **Step 1: Write the failing token and auth tests.** Use temporary directories and `httptest`:

```go
func TestLoadOrCreatePersistsRestrictedToken(t *testing.T) {
    path := filepath.Join(t.TempDir(), "secrets", "api-token")
    first, err := credentials.LoadOrCreate(path)
    if err != nil { t.Fatal(err) }
    again, err := credentials.LoadOrCreate(path)
    if err != nil || first != again || len(first) < 43 { t.Fatalf("unstable token: %q %v", again, err) }
    info, err := os.Stat(path)
    if err != nil || info.Mode().Perm() != 0o600 { t.Fatalf("token mode: %v %v", info, err) }
}
```

```go
func TestHealthIsPublicButVersionNeedsBearer(t *testing.T) {
    h := apiserver.New("secret", version.Get(), func() bool { return true })
    for _, tc := range []struct{ path, auth string; want int }{
        {"/v1/health", "", 200},
        {"/v1/version", "", 401},
        {"/v1/version", "Bearer wrong", 401},
        {"/v1/version", "Bearer secret", 200},
    } {
        req := httptest.NewRequest(http.MethodGet, tc.path, nil)
        req.Host = "localhost:7788"
        if tc.auth != "" { req.Header.Set("Authorization", tc.auth) }
        rec := httptest.NewRecorder()
        h.ServeHTTP(rec, req)
        if rec.Code != tc.want { t.Fatalf("%s: got %d want %d", tc.path, rec.Code, tc.want) }
    }
}
```

- [ ] **Step 2: Run the focused tests and confirm red.** Run `go test ./internal/credentials ./internal/apiserver`; expect missing packages or failing assertions.
- [ ] **Step 3: Implement token creation and generated HTTP routing.** Generate 32 cryptographically random bytes, encode with `base64.RawURLEncoding`, write a new file with exclusive creation and `0600`, and reject existing empty/over-permissive files. Use `subtle.ConstantTimeCompare` for bearer checks. Reject unexpected `Host` values and browser `Origin` headers at the API boundary. Route both operations through the generated server interface; return the ADR-0008 error envelope for `401`.
- [ ] **Step 4: Wire `cmd/nephosd` and verify green.** Keep `signal.NotifyContext`, create the token in `/var/lib/nephos/secrets/api-token`, serve until cancellation, then shut down with a bounded timeout. Run `go test ./internal/credentials ./internal/apiserver ./cmd/nephosd`, `make test-race`, and `make generate-check`; expect exit 0. Commit with DCO sign-off.

### Task 3: Build a local development AMI and fail-closed appliance image

**Files:** Create `images/dev-ami/Dockerfile`, `images/appliance/Dockerfile`, `images/appliance/entrypoint.sh`, `images/appliance/storage.conf`, `images/appliance/containers.conf`, and `tests/appliance-smoke.sh`; modify `Makefile` and `.gitignore`.

**Interfaces:** `make dev-ami` creates `dist/ubuntu-24.04.oci.tar` tagged `nephos-ubuntu:dev`; `make appliance` creates `nephos-appliance:dev` containing the archive and all three Nephos binaries. The entrypoint starts `nephosd` only after it validates prerequisites and imports the AMI.

- [ ] **Step 1: Write a failing image smoke check.** The script must assert that an image build and manual Docker start produce a ready unauthenticated health endpoint, a denied unauthenticated version endpoint, and a present token file:

```bash
test -f dist/ubuntu-24.04.oci.tar
docker image inspect nephos-appliance:dev >/dev/null
test "$(curl -sS -o /dev/null -w '%{http_code}' http://127.0.0.1:7788/v1/health)" = 200
test "$(curl -sS -o /dev/null -w '%{http_code}' http://127.0.0.1:7788/v1/version)" = 401
docker cp nephos:/var/lib/nephos/secrets/api-token - >/dev/null
```

The script creates only its own `nephos` test container and `nephos-data` volume after verifying neither name already exists; trap cleanup must remove only those exact objects it created. Run `bash -n tests/appliance-smoke.sh` and then the script before the images exist; expect failure at the image check.

- [ ] **Step 2: Add the two Dockerfiles and Podman configuration.** Use a minimal Ubuntu 24.04 systemd/ping AMI with `APT::Sandbox::User "root"`, `userns=auto` compatibility, and no embedded host keys; preserve the M0 `ds-identify` and cloud-init configuration if cloud-init is included. The Debian trixie appliance installs Podman, crun, conmon, nftables, iproute2, catatonit, and copies the three CGO-free Go binaries plus the OCI archive. Its storage and runroot live under `/var/lib/nephos`.
- [ ] **Step 3: Add the fail-closed entrypoint.** Use the M0 SP1 report as evidence, not an imported runtime dependency. Check cgroup v2, privately delegate `memory`, `pids`, and `cpu` to a leaf cgroup, verify nftables, netns, veth, dummy, memory, and available volume disk, import `nephos-ubuntu:dev` if missing, and `exec nephosd` under catatonit. A failed check must exit nonzero with a specific error; never start an unbounded or partially capable appliance.
- [ ] **Step 4: Add reproducible build commands.** Create or reuse a named `docker-container` Buildx builder without changing the user's default builder. Build the AMI with `docker buildx build --builder nephos-oci --output type=oci,name=nephos-ubuntu:dev,dest=dist/ubuntu-24.04.oci.tar -f images/dev-ami/Dockerfile images/dev-ami`, then build `nephos-appliance:dev` from the repository root. `make appliance` must fail with `make dev-ami` guidance if the archive is absent; it does not silently build the AMI.
- [ ] **Step 5: Verify the real build and startup.** Run `make dev-ami`, `make appliance`, `bash tests/appliance-smoke.sh`, `make build`, and `make test`. The smoke test must inspect Docker's container configuration to assert `Privileged=true`, private cgroup namespace, non-host network/PID modes, only the named volume mounted, and host port `127.0.0.1:7788`. Confirm no `nephos` test container or volume remains, then commit with DCO sign-off.

### Task 4: Define and test the Docker Engine adapter

**Files:** Create `internal/appliance/engine.go`, `docker.go`, and `docker_test.go`; modify `go.mod`/`go.sum`.

**Interfaces:** `appliance.Engine` exposes `Ping`, `Info`, `ImageExists`, `Inspect`, `CreateVolume`, `Create`, `Start`, `Stop`, `Remove`, `RemoveVolume`, and `CopyFile`, each accepting `context.Context` first. `NewDockerEngine() (Engine, error)` adapts the pinned Moby client; `Create` accepts `Limits{MemoryBytes: 4 << 30, NanoCPUs: 2e9, PIDs: 4096}` by default.

- [ ] **Step 1: Write failing configuration tests.** The pure helper `appliance.ContainerConfig(limits Limits) (*container.Config, *container.HostConfig)` must produce the exact isolation contract:

```go
func TestContainerConfigNeverUsesHostNamespaces(t *testing.T) {
    cfg, host := appliance.ContainerConfig(appliance.Limits{MemoryBytes: 4 << 30, NanoCPUs: 2e9, PIDs: 4096})
    if cfg.Image != "nephos-appliance:dev" || !host.Privileged { t.Fatal("wrong appliance") }
    if host.NetworkMode == "host" || host.PidMode == "host" || host.CgroupnsMode != "private" { t.Fatal("host isolation lost") }
    if len(host.Binds) != 0 || len(host.Mounts) != 1 || host.Mounts[0].Source != "nephos-data" { t.Fatal("unexpected mount") }
    port := network.MustParsePort("7788/tcp")
    if len(host.PortBindings[port]) != 1 || host.PortBindings[port][0].HostIP.String() != "127.0.0.1" { t.Fatal("API is not loopback-only") }
}
```

- [ ] **Step 2: Run `go test ./internal/appliance` and confirm red.** Expect missing helper/types.
- [ ] **Step 3: Implement the domain interface and adapter.** Use `github.com/moby/moby/client` v0.6.0 with `client.New(client.FromEnv)`, API negotiation, and `github.com/moby/moby/api` types. Convert SDK not-found errors to `appliance.ErrNotFound`; do not infer absence from arbitrary failures. `CopyFile` wraps `CopyFromContainer` and returns a bounded TAR reader. `ContainerConfig` sets `NetworkMode` and `PidMode` to private Docker defaults, `CgroupnsMode` to `private`, the named volume mount to `/var/lib/nephos`, `HostIP` to `127.0.0.1`, and no host bind mounts.
- [ ] **Step 4: Run `go test ./internal/appliance`, `make cross`, and `go mod tidy`; expect exit 0.** The cross-build proves the host CLI still compiles on Windows and macOS. Commit with DCO sign-off.

### Task 5: Implement idempotent CLI lifecycle and token bootstrap

**Files:** Create `internal/appliance/lifecycle.go`, `lifecycle_test.go`, `credential.go`, `credential_test.go`; modify `cmd/nephos/main.go`.

**Interfaces:** `appliance.Manager{Engine Engine, Health HealthProbe, Credentials CredentialStore}` exposes `Up(ctx, Limits) error`, `Down(ctx) error`, and `Status(ctx) (Status, error)`. `Status` is one of `missing`, `stopped`, `starting`, `ready`, `unhealthy`. `CredentialStore` writes only `~/.nephos/credentials` with mode `0600`.

- [ ] **Step 1: Write failing fake-Engine lifecycle tests.** A new `Up` must preflight before creating a volume/container, create exactly one container, start it, wait for ready health, and copy a token; a second `Up` must not create another object. A stopped container must restart with the same volume and token. A missing image must report `make dev-ami && make appliance` and make zero mutating Engine calls. `Down` preserves volume; `Status` distinguishes each documented state.
- [ ] **Step 2: Write failing archive and permissions tests.** Feed `CredentialStore` a one-file TAR archive named `api-token` with a valid token and require `0600`; feed it `../api-token`, a symlink, two entries, an oversized file, and an empty token and require errors without modifying an existing credential. Native-Windows credential ACL behavior is recorded for M8, not claimed as validated in M1.
- [ ] **Step 3: Run `go test ./internal/appliance ./cmd/nephos` and confirm red.** Expect missing manager methods and credential writer.
- [ ] **Step 4: Implement preflight and lifecycle.** Query Docker Info for rootless mode, cgroup v2, Linux kernel >=5.15, `amd64`/`arm64`, and at least the configured 4 GiB; leave volume disk checking to appliance startup where its filesystem is visible. Start only the fixed `nephos` container with the fixed `nephos-data` volume and the image already present. Do not call `ContainerRemove` or `VolumeRemove` from `Down`; those are reserved for slice 4 purge/reset. Wait for public health with a bounded deadline, copy and validate the token archive via the Engine, then atomically replace the credential file without logging token bytes. Respect `context.Context` cancellation.
- [ ] **Step 5: Wire `nephos up|down|status` into the existing CLI.** Preserve `nephos version`; parse `--memory`, `--cpus`, and `--pids-limit` with architecture defaults `4g`, `2`, and `4096`. Usage errors return code 2, operational errors code 1. Never call the Docker API for resource CRUD.
- [ ] **Step 6: Verify and commit.** Run `go test ./internal/appliance ./cmd/nephos`, `make test-race`, `make cross`, and the real `tests/appliance-smoke.sh` through CLI `up/status/down/up`; expect preserved credential bytes and zero duplicate objects. Commit with DCO sign-off.

### Task 6: Finish the slice's contributor path and CI evidence

**Files:** Modify `docs/DEVELOPMENT.md`, `.github/workflows/ci.yml`, `CHANGELOG.md`, and `tests/appliance-smoke.sh`.

**Interfaces:** A fresh contributor can run `make dev-ami`, `make appliance`, `make build`, `bin/nephos up`, authenticated `curl /v1/version`, `bin/nephos down`, and `bin/nephos up` using only local builds.

- [ ] **Step 1: Write a failing contributor-path check.** Start from a clean ignored `dist/`, assert `nephos up` gives actionable missing-image guidance, then build locally and run the command sequence above. Do not run this check against any pre-existing `nephos` container or `nephos-data` volume; fail with an explicit collision message.
- [ ] **Step 2: Document the exact build and test commands.** Explain the named Buildx builder, rootful Docker/cgroup v2 requirements, local image tags, `~/.nephos/credentials` handling, and that this is M1 slice 1 (resource commands arrive in later slices). Update `CHANGELOG.md` without marking M1 complete.
- [ ] **Step 3: Add a native-Ubuntu CI job for the image/CLI smoke test.** Use GitHub Actions `ubuntu-24.04`, build both local images, run `tests/appliance-smoke.sh`, collect appliance logs on failure, and always remove only the job's `nephos` container and `nephos-data` volume. Keep non-Docker Go checks on their current jobs.
- [ ] **Step 4: Verify all relevant checks and review the diff.** Run `make ci`, `make generate-check`, `make dev-ami`, `make appliance`, and `bash tests/appliance-smoke.sh` in a clean test Docker context; then `git diff --check`, `git status --short --branch`, and inspect Docker for residual test objects. Commit with DCO sign-off. The slice is done only when the fresh-contributor path works and the CI job passes; do not check off full M1 roadmap acceptance yet.
