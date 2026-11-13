#!/usr/bin/env bash
# Run every `integration`-tagged Go test package inside a throwaway privileged
# appliance container built from nephos-appliance:dev.
#
# The integration tests create real network namespaces, veths, nftables state
# and Podman containers. They must never run on the host. Each test binary is
# compiled on the host (CGO disabled), copied into the fixture container, and
# executed there with NEPHOS_IN_APPLIANCE=1. The fixture keeps the production
# entrypoint (cgroup delegation, Podman service, AMI import) but runs
# `sleep infinity` instead of nephosd, so tests own all kernel/runtime state.
#
# Usage: tests/integration-in-appliance.sh [package ...]
#   With no arguments, every package that has a linux && integration test file
#   is run. The leak checker package always runs last, because its final test
#   asserts that the appliance holds no Nephos-owned state before teardown.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

fail() { echo "integration-in-appliance: $*" >&2; exit 1; }

image="${NEPHOS_APPLIANCE_IMAGE:-nephos-appliance:dev}"
timeout="${NEPHOS_INTEGRATION_TIMEOUT:-300s}"
docker image inspect "$image" >/dev/null 2>&1 || fail "image $image missing; run make dev-ami appliance first"

work="$(mktemp -d)"
container=""
cleanup() {
    result=$?
    if [ -n "$container" ]; then
        if [ "$result" -ne 0 ]; then
            echo "--- fixture container logs ---" >&2
            docker logs "$container" >&2 || true
        fi
        docker rm -f -v "$container" >/dev/null 2>&1 || true
    fi
    rm -rf "$work"
    exit "$result"
}
trap cleanup EXIT

if [ "$#" -gt 0 ]; then
    packages=("$@")
else
    mapfile -t packages < <(
        grep -rl --include='*_test.go' '^//go:build linux && integration' cmd internal \
            | xargs -n1 dirname | sort -u | sed 's|^|./|'
    )
fi
[ "${#packages[@]}" -gt 0 ] || fail "no integration test packages found"

# Run the leak checker last so its whole-appliance emptiness proof sees the
# state left behind by every other package.
ordered=()
last=()
for pkg in "${packages[@]}"; do
    case "$pkg" in
        */internal/leakcheck) last+=("$pkg") ;;
        *) ordered+=("$pkg") ;;
    esac
done
ordered+=("${last[@]}")

echo "== compiling ${#ordered[@]} integration test binaries"
for pkg in "${ordered[@]}"; do
    name="$(echo "${pkg#./}" | tr '/' '_').test"
    CGO_ENABLED=0 GOOS=linux go test -tags integration -c -o "$work/$name" "$pkg"
done
CGO_ENABLED=0 GOOS=linux go build -o "$work/namespacehold" ./tests/fixtures/namespacehold
CGO_ENABLED=0 GOOS=linux go build -o "$work/task-limit-helper" ./tests/fixtures/tasklimit

echo "== starting privileged fixture appliance"
container="$(docker run -d \
    --label io.nephos.test=true \
    --privileged --cgroupns private \
    --memory 4g --cpus 2 --pids-limit 4096 \
    "$image" /bin/sleep infinity)"

# The entrypoint finishes setup (cgroup delegation, Podman service, AMI
# import) before it execs the command; wait for the Podman socket and image.
for _ in $(seq 1 120); do
    if docker exec "$container" podman image exists nephos-ubuntu:dev >/dev/null 2>&1; then
        break
    fi
    if [ "$(docker inspect -f '{{.State.Running}}' "$container")" != true ]; then
        fail "fixture appliance exited during setup"
    fi
    sleep 1
done
docker exec "$container" podman image exists nephos-ubuntu:dev || fail "fixture appliance never became ready"

docker cp "$work/." "$container:/tmp/itest/" >/dev/null
# Some tests re-exec their binary as nobody (65534), and the pids-limit test
# reads its helper from a fixed path inside the appliance.
docker exec "$container" sh -c 'chmod 0755 /tmp/itest /tmp/itest/* && cp /tmp/itest/task-limit-helper /run/task-limit-helper' \
    || fail "could not stage integration fixtures"

failed=()
for pkg in "${ordered[@]}"; do
    name="$(echo "${pkg#./}" | tr '/' '_').test"
    echo "== $pkg"
    if ! docker exec \
        -e NEPHOS_IN_APPLIANCE=1 \
        -e NEPHOS_NAMESPACEHOLD=/tmp/itest/namespacehold \
        -w /tmp/itest \
        "$container" "/tmp/itest/$name" -test.v -test.count=1 -test.timeout "$timeout"; then
        failed+=("$pkg")
    fi
done

if [ "${#failed[@]}" -gt 0 ]; then
    fail "failed packages: ${failed[*]}"
fi
echo "== all ${#ordered[@]} integration packages passed"
