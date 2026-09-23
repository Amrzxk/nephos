#!/usr/bin/env bash
# Exercise a Docker start failure without leaving Nephos test objects behind.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

fail() { echo "appliance-smoke-failed-start: $*" >&2; exit 1; }

if docker container inspect nephos >/dev/null 2>&1; then
    fail "container 'nephos' already exists; refusing to replace it"
fi
if docker volume inspect nephos-data >/dev/null 2>&1; then
    fail "volume 'nephos-data' already exists; refusing to replace it"
fi
if docker container inspect nephos-smoke-port-blocker >/dev/null 2>&1; then
    fail "port blocker already exists; refusing to replace it"
fi
docker image inspect nephos-appliance:dev >/dev/null || fail "run make appliance first"
test -x bin/nephos || fail "run make build first"

blocker_id=""
cleanup() {
    result=$?
    if docker container inspect nephos >/dev/null 2>&1; then
        if [ "$(docker inspect -f '{{index .Config.Labels "io.nephos.appliance"}}' nephos)" = true ]; then
            docker rm -f nephos >/dev/null || true
        fi
    fi
    if docker volume inspect nephos-data >/dev/null 2>&1; then
        if [ "$(docker volume inspect -f '{{index .Labels "io.nephos.appliance"}}' nephos-data)" = true ]; then
            docker volume rm nephos-data >/dev/null || true
        fi
    fi
    if [ -n "$blocker_id" ]; then
        docker rm -f "$blocker_id" >/dev/null || true
    fi
    exit "$result"
}
trap cleanup EXIT

# Hold PID 1 before the API listener opens to verify the real Docker health
# signal reaches the CLI as 'starting', rather than 'unhealthy'.
docker volume create --label io.nephos.appliance=true nephos-data >/dev/null
bootstrap_id="$(docker create --name nephos --label io.nephos.appliance=true \
    --privileged --cgroupns private --memory 4g --cpus 2 --pids-limit 4096 \
    -v nephos-data:/var/lib/nephos --entrypoint /bin/sleep \
    nephos-appliance:dev 60)"
docker start "$bootstrap_id" >/dev/null
[ "$(bin/nephos status)" = starting ] || fail "CLI did not report starting before API listener"
docker rm -f "$bootstrap_id" >/dev/null
docker volume rm nephos-data >/dev/null

# Docker reserves published ports when a container starts. The blocker needs
# no service on 7788; it only needs to stay alive with that mapping.
blocker_id="$(docker create --name nephos-smoke-port-blocker \
    --label io.nephos.test=true --entrypoint /bin/sleep \
    -p 127.0.0.1:7788:7788 nephos-appliance:dev 60)"
docker start "$blocker_id" >/dev/null
[ "$(docker inspect -f '{{.State.Health.Status}}' "$blocker_id")" = starting ] \
    || fail "Docker did not expose the startup health state"

set +e
output="$(bash tests/appliance-smoke.sh 2>&1)"
smoke_result=$?
set -e
[ "$smoke_result" -ne 0 ] || fail "expected smoke startup to fail on occupied port"
[[ "$output" == *port* ]] || fail "smoke failed for another reason: $output"
if docker container inspect nephos >/dev/null 2>&1; then
    fail "failed Docker start leaked the Nephos container"
fi
if docker volume inspect nephos-data >/dev/null 2>&1; then
    fail "failed Docker start leaked the Nephos volume"
fi

echo "appliance-smoke-failed-start: failed start left no test objects"
