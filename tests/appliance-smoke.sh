#!/usr/bin/env bash
# Isolated first-slice appliance check. Never take over existing Nephos state.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

fail() { echo "appliance-smoke: $*" >&2; exit 1; }

if docker container inspect nephos >/dev/null 2>&1; then
    fail "container 'nephos' already exists; refusing to replace it"
fi
if docker volume inspect nephos-data >/dev/null 2>&1; then
    fail "volume 'nephos-data' already exists; refusing to replace it"
fi

made_volume=false
made_container=false
cleanup() {
    result=$?
    if [ "$result" -ne 0 ] && [ "$made_container" = true ]; then
        docker logs nephos >&2 || true
    fi
    if [ "$made_container" = true ]; then
        docker rm -f nephos >/dev/null || true
    fi
    if [ "$made_volume" = true ]; then
        docker volume rm nephos-data >/dev/null || true
    fi
    exit "$result"
}
trap cleanup EXIT

test -f dist/ubuntu-24.04.oci.tar || fail "run make dev-ami first"
docker image inspect nephos-appliance:dev >/dev/null || fail "run make appliance first"

docker volume create --label io.nephos.appliance=true nephos-data >/dev/null
made_volume=true
docker run -d --name nephos --label io.nephos.appliance=true --privileged --cgroupns private \
    --memory 4g --cpus 2 --pids-limit 4096 \
    -v nephos-data:/var/lib/nephos \
    -p 127.0.0.1:7788:7788 \
    nephos-appliance:dev >/dev/null
made_container=true

[ "$(docker inspect -f '{{.HostConfig.Privileged}}' nephos)" = true ] || fail "not privileged"
[ "$(docker inspect -f '{{.HostConfig.CgroupnsMode}}' nephos)" = private ] || fail "cgroup namespace is not private"
[ "$(docker inspect -f '{{.HostConfig.NetworkMode}}' nephos)" != host ] || fail "host network mode"
[ "$(docker inspect -f '{{.HostConfig.PidMode}}' nephos)" != host ] || fail "host PID mode"
[ "$(docker inspect -f '{{range .Mounts}}{{.Type}}:{{.Name}}:{{.Destination}};{{end}}' nephos)" = 'volume:nephos-data:/var/lib/nephos;' ] || fail "unexpected mount"
[ "$(docker port nephos 7788/tcp)" = '127.0.0.1:7788' ] || fail "API is not bound to loopback"

for attempt in $(seq 1 60); do
    code="$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:7788/v1/health || true)"
    if [ "$code" = 200 ]; then break; fi
    if ! docker inspect -f '{{.State.Running}}' nephos | grep -qx true; then
        fail "appliance exited during startup"
    fi
    sleep 1
done
[ "$code" = 200 ] || fail "health did not become ready"
[ "$(curl -sS -o /dev/null -w '%{http_code}' http://127.0.0.1:7788/v1/version)" = 401 ] || fail "version route accepted an unauthenticated request"
[ "$(docker cp nephos:/var/lib/nephos/secrets/api-token - | tar -xOf - api-token | wc -c)" -ge 43 ] || fail "API token missing"
version_body="$(curl -fsS --config <(
    docker cp nephos:/var/lib/nephos/secrets/api-token - 2>/dev/null \
        | tar -xOf - api-token \
        | awk '{printf "header = \"Authorization: Bearer %s\"\n", $0}'
) http://127.0.0.1:7788/v1/version)"
expected_commit="$(git rev-parse HEAD)"
[[ "$version_body" == *\"build_commit\":\"$expected_commit\"* ]] || fail "appliance build identifier does not match Git HEAD"
for controller in memory pids cpu; do
    docker exec nephos grep -qw "$controller" /sys/fs/cgroup/cgroup.subtree_control || fail "$controller controller not delegated"
done
docker exec nephos podman image exists nephos-ubuntu:dev || fail "development AMI was not imported"

echo "appliance-smoke: health, auth boundary, image import, and isolation passed"
