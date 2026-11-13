#!/usr/bin/env bash
# The M1 demo from docs/ROADMAP.md, replayed exactly as a learner types it
# (names, not IDs), with assertions between the steps. It prints a transcript
# that doubles as evidence. docs/demos/M1.md is the manual version.
#
# Requires: make dev-ami appliance build, Docker, jq. Refuses to touch an
# existing `nephos` container or `nephos-data` volume.
set -euo pipefail
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"
fail() { echo "m1-e2e: FAIL $*" >&2; exit 1; }
passed() { echo "m1-e2e: PASS $*"; }
for tool in docker jq; do
    command -v "$tool" >/dev/null || fail "required tool $tool is missing"
done
if docker container inspect nephos >/dev/null 2>&1; then fail "container nephos already exists; refusing to replace it"; fi
if docker volume inspect nephos-data >/dev/null 2>&1; then fail "volume nephos-data already exists; refusing to replace it"; fi
docker image inspect nephos-appliance:dev >/dev/null || fail "run make dev-ami appliance first"
test -x bin/nephos || fail "run make build first"

test_home="$(mktemp -d /tmp/nephos-m1-e2e.XXXXXX)"
[[ "$test_home" == /tmp/nephos-m1-e2e.* ]] || fail "unexpected temporary home"
container_id=""
volume_created=""
cleanup() {
    local result=$?
    trap - EXIT
    if [ -n "$container_id" ]; then
        if [ "$(docker inspect -f '{{.Id}}' nephos 2>/dev/null || true)" = "$container_id" ] &&
           [ "$(docker inspect -f '{{index .Config.Labels "io.nephos.appliance"}}' nephos 2>/dev/null || true)" = true ]; then
            [ "$result" -eq 0 ] || docker logs nephos 2>&1 | tail -100 >&2 || true
            docker rm -f "$container_id" >/dev/null || result=1
        else
            echo "m1-e2e: test container identity changed; preserving current object" >&2
            result=1
        fi
    fi
    if [ -n "$volume_created" ]; then
        if [ "$(docker volume inspect -f '{{.CreatedAt}}' nephos-data 2>/dev/null || true)" = "$volume_created" ] &&
           [ "$(docker volume inspect -f '{{index .Labels "io.nephos.appliance"}}' nephos-data 2>/dev/null || true)" = true ]; then
            docker volume rm nephos-data >/dev/null || result=1
        else
            echo "m1-e2e: test volume identity changed; preserving current object" >&2
            result=1
        fi
    fi
    if [ "$result" -eq 0 ]; then
        rm -r -- "$test_home"
    else
        echo "m1-e2e: evidence retained at $test_home" >&2
    fi
    exit "$result"
}
trap cleanup EXIT

# `demo` echoes each learner command before running it, so the log reads as
# the demo transcript.
nephos() { HOME="$test_home" bin/nephos "$@" </dev/null; }
demo() {
    printf '\n$ nephos %s\n' "$*"
    nephos "$@"
}

echo "== M1 demo ($(bin/nephos version))"
demo up
container_id="$(docker inspect -f '{{.Id}}' nephos)"
volume_created="$(docker volume inspect -f '{{.CreatedAt}}' nephos-data)"
demo vpc create lab --cidr-block 10.0.0.0/16
demo subnet create a --vpc lab --cidr-block 10.0.1.0/24 --availability-zone local-1a
demo subnet create b --vpc lab --cidr-block 10.0.2.0/24 --availability-zone local-1b
demo instance run one --subnet a --wait
demo instance run two --subnet b --wait
[ "$(nephos instance describe one -o json | jq -r .private_ip)" = 10.0.1.4 ] || fail "one did not get the first assignable address"
[ "$(nephos instance describe two -o json | jq -r .private_ip)" = 10.0.2.4 ] || fail "two did not get the first assignable address"
demo console one -- ping -c 3 10.0.2.4 || fail "one cannot reach two across subnets"
passed "two instances in different subnets ping each other"

# Not part of the printed demo: prove the restart keeps the writable root.
nephos console one -- /bin/sh -c 'echo m1-demo-marker > /root/marker' || fail "could not write the marker file"
before="$(nephos instance list -o json | jq -cS '[.items[] | {id, name, private_ip, state}]')"
demo down
demo up
[ "$(docker inspect -f '{{.Id}}' nephos)" = "$container_id" ] || fail "down/up replaced the appliance container"
after="$(nephos instance list -o json | jq -cS '[.items[] | {id, name, private_ip, state}]')"
[ "$after" = "$before" ] || fail "restart changed instances: before=$before after=$after"
demo console one -- ping -c 3 10.0.2.4 || fail "ping failed after restart"
[ "$(nephos console one -- cat /root/marker)" = m1-demo-marker ] || fail "restart lost the file in one's root filesystem"
passed "down/up keeps instances, private IPs, a root-filesystem file and connectivity"

demo reset
for kind in instance subnet vpc; do
    nephos "$kind" list -o json | jq -e '.items | length == 0' >/dev/null || fail "$kind list not empty after reset"
done
passed "reset leaves the default workspace empty with a clean leak check"
echo "== M1 demo complete"
