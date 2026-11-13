#!/usr/bin/env bash
# Real-Docker reset (M1 slices 4C and 4D). Soft reset: populate the default
# workspace, run `nephos reset`, and prove that the API is empty and that the
# appliance's kernel and Podman state match the baseline taken right after
# `nephos up`. Hard reset and purge: prove the old container, volume, data and
# token are gone, the fresh appliance keeps the old limits, and purge leaves
# no Nephos Docker object behind.
set -euo pipefail
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"
fail() { echo "reset-smoke: $*" >&2; exit 1; }
passed() { echo "reset-smoke: PASS $*"; }
for tool in docker jq; do
    command -v "$tool" >/dev/null || fail "required tool $tool is missing"
done
if docker container inspect nephos >/dev/null 2>&1; then fail "container nephos already exists; refusing to replace it"; fi
if docker volume inspect nephos-data >/dev/null 2>&1; then fail "volume nephos-data already exists; refusing to replace it"; fi
docker image inspect nephos-appliance:dev >/dev/null || fail "run make dev-ami appliance first"
test -x bin/nephos || fail "run make build first"

test_home="$(mktemp -d /tmp/nephos-reset-smoke.XXXXXX)"
[[ "$test_home" == /tmp/nephos-reset-smoke.* ]] || fail "unexpected temporary home"
container_id=""
volume_created=""
cleanup() {
    local result=$?
    trap - EXIT
    if [ -n "$container_id" ]; then
        if [ "$(docker inspect -f '{{.Id}}' nephos 2>/dev/null || true)" = "$container_id" ] &&
           [ "$(docker inspect -f '{{index .Config.Labels "io.nephos.appliance"}}' nephos 2>/dev/null || true)" = true ]; then
            if [ "$result" -ne 0 ]; then
                docker logs nephos 2>&1 | tail -100 >&2 || true
            fi
            docker rm -f "$container_id" >/dev/null || result=1
        else
            echo "reset-smoke: test container identity changed; preserving current object" >&2
            result=1
        fi
    fi
    if [ -n "$volume_created" ]; then
        if [ "$(docker volume inspect -f '{{.CreatedAt}}' nephos-data 2>/dev/null || true)" = "$volume_created" ] &&
           [ "$(docker volume inspect -f '{{index .Labels "io.nephos.appliance"}}' nephos-data 2>/dev/null || true)" = true ]; then
            docker volume rm nephos-data >/dev/null || result=1
        else
            echo "reset-smoke: test volume identity changed; preserving current object" >&2
            result=1
        fi
    fi
    if [ "$result" -eq 0 ]; then
        rm -r -- "$test_home"
    else
        echo "reset-smoke: evidence retained at $test_home" >&2
    fi
    exit "$result"
}
trap cleanup EXIT

cli() { HOME="$test_home" bin/nephos "$@"; }
# Everything Nephos can leave behind inside the appliance: named namespaces,
# root-namespace links, root nftables tables and Podman containers.
appliance_state() {
    {
        echo "# netns"; docker exec nephos ip netns list | awk '{print $1}' | sort
        echo "# links"; docker exec nephos ip -o link show | awk -F': ' '{print $2}' | sed 's/@.*//' | sort
        echo "# nft"; docker exec nephos nft list tables | sort
        echo "# podman"; docker exec nephos podman ps -a --format '{{.Names}}' | sort
    }
}
expect_empty_api() {
    local kind
    for kind in instance subnet vpc; do
        cli "$kind" list -o json | jq -e '.items | length == 0' >/dev/null || fail "$kind list is not empty after reset"
    done
}

# A non-default limit, so the hard reset below can prove it is preserved.
cli up --pids-limit 5000
container_id="$(docker inspect -f '{{.Id}}' nephos)"
volume_created="$(docker volume inspect -f '{{.CreatedAt}}' nephos-data)"
[ "$(cli status)" = ready ] || fail "appliance not ready"
appliance_state >"$test_home/baseline"

# An empty workspace resets cleanly.
cli reset -o json | jq -e '.workspace == "default" and .leak_check.clean == true' >/dev/null || fail "empty reset was not clean"
passed "reset of an empty workspace"

# Two overlapping VPCs, three subnets and three running instances.
vpc_a="$(cli vpc create A --cidr-block 10.0.0.0/16 --wait -o json | jq -r .id)"
vpc_b="$(cli vpc create B --cidr-block 10.0.0.0/16 --wait -o json | jq -r .id)"
subnet_a1="$(cli subnet create a1 --vpc "$vpc_a" --cidr-block 10.0.1.0/24 --availability-zone local-1a --wait -o json | jq -r .id)"
subnet_a2="$(cli subnet create a2 --vpc "$vpc_a" --cidr-block 10.0.2.0/24 --availability-zone local-1b --wait -o json | jq -r .id)"
subnet_b1="$(cli subnet create b1 --vpc "$vpc_b" --cidr-block 10.0.1.0/24 --availability-zone local-1a --wait -o json | jq -r .id)"
cli instance run one --subnet "$subnet_a1" --wait -o json | jq -e '.state == "running"' >/dev/null || fail "instance one not running"
cli instance run two --subnet "$subnet_a2" --wait -o json | jq -e '.state == "running"' >/dev/null || fail "instance two not running"
cli instance run three --subnet "$subnet_b1" --wait -o json | jq -e '.state == "running"' >/dev/null || fail "instance three not running"
cli console one -- ping -c 1 -W 2 10.0.2.4 </dev/null >/dev/null || fail "cross-subnet ping failed before reset"
appliance_state >"$test_home/populated"
! cmp -s "$test_home/baseline" "$test_home/populated" || fail "populated appliance state looks like the baseline; the check would prove nothing"

result="$(cli reset -o json)" || fail "reset failed: $result"
jq -e '.leak_check.complete == true and .leak_check.clean == true and (.leak_check.findings | length == 0)' <<<"$result" >/dev/null ||
    fail "reset leak check was not clean: $result"
expect_empty_api
appliance_state >"$test_home/after-reset"
if ! cmp -s "$test_home/baseline" "$test_home/after-reset"; then
    diff -u "$test_home/baseline" "$test_home/after-reset" >&2 || true
    fail "appliance state differs from the post-up baseline"
fi
passed "reset removes every instance, subnet and VPC and leaves no namespace, link, table or container"

# The workspace is usable again, and a second reset is idempotent.
again="$(cli vpc create A --cidr-block 10.0.0.0/16 --wait -o json | jq -r .id)"
[ -n "$again" ] && [ "$again" != "$vpc_a" ] || fail "VPC create after reset did not return a new ID"
cli reset >/dev/null || fail "second reset failed"
cli reset >/dev/null || fail "reset of the emptied workspace failed"
expect_empty_api
appliance_state >"$test_home/after-second-reset"
cmp -s "$test_home/baseline" "$test_home/after-second-reset" || fail "second reset left appliance state behind"
[ "$(cli status)" = ready ] || fail "appliance not ready after reset"
passed "reset is repeatable and the appliance stays ready"

# Hard reset: a file in an instance root, the database and the token all go;
# a new container and volume start with the old limits.
vpc="$(cli vpc create keep --cidr-block 10.0.0.0/16 --wait -o json | jq -r .id)"
subnet="$(cli subnet create keep --vpc "$vpc" --cidr-block 10.0.1.0/24 --availability-zone local-1a --wait -o json | jq -r .id)"
cli instance run keeper --subnet "$subnet" --wait -o json | jq -e '.state == "running"' >/dev/null || fail "instance keeper not running"
cli console keeper -- /bin/sh -c 'echo nephos-reset-marker > /root/marker' </dev/null || fail "could not write the marker file"
old_token="$(cat "$test_home/.nephos/credentials")"
old_container="$container_id"
old_volume="$volume_created"
cli reset --hard </dev/null >/dev/null 2>&1 && fail "non-interactive hard reset ran without --yes"
[ "$(docker inspect -f '{{.Id}}' nephos)" = "$old_container" ] || fail "refused hard reset still touched the container"
cli reset --hard --yes </dev/null >"$test_home/hard-reset.out" || fail "hard reset failed: $(cat "$test_home/hard-reset.out")"
container_id="$(docker inspect -f '{{.Id}}' nephos)"
volume_created="$(docker volume inspect -f '{{.CreatedAt}}' nephos-data)"
[ "$container_id" != "$old_container" ] || fail "hard reset kept the old container"
[ "$volume_created" != "$old_volume" ] || fail "hard reset kept the old volume"
[ "$(cat "$test_home/.nephos/credentials")" != "$old_token" ] || fail "hard reset kept the old API token"
[ "$(docker inspect -f '{{.HostConfig.PidsLimit}}' nephos)" = 5000 ] || fail "hard reset did not preserve the pids limit"
[ "$(cli status)" = ready ] || fail "fresh appliance not ready"
expect_empty_api
appliance_state >"$test_home/after-hard-reset"
cmp -s "$test_home/baseline" "$test_home/after-hard-reset" || fail "fresh appliance state differs from the first baseline"
passed "reset --hard replaces the container, volume and token, keeps limits, and starts empty"

# Purge: nothing Nephos-owned remains in Docker, and a second purge is a no-op.
cli down --purge --yes </dev/null >/dev/null || fail "down --purge failed"
container_id=""
volume_created=""
docker container inspect nephos >/dev/null 2>&1 && fail "purge left the container"
docker volume inspect nephos-data >/dev/null 2>&1 && fail "purge left the volume"
[ ! -e "$test_home/.nephos/credentials" ] || fail "purge left the API token"
cli down --purge --yes </dev/null | grep -q 'No Nephos appliance' || fail "second purge was not a no-op"
passed "down --purge removes the appliance, its volume and the token"
