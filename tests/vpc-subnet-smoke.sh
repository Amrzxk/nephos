#!/usr/bin/env bash
# Real-Docker M1 slice-2 path. Never replace a contributor's appliance state.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

fail() { echo "vpc-subnet-smoke: $*" >&2; exit 1; }
for tool in docker curl jq ip readlink; do
    command -v "$tool" >/dev/null || fail "required tool $tool is missing"
done
if docker container inspect nephos >/dev/null 2>&1; then
    fail "container 'nephos' already exists; refusing to replace it"
fi
if docker volume inspect nephos-data >/dev/null 2>&1; then
    fail "volume 'nephos-data' already exists; refusing to replace it"
fi
test -f dist/ubuntu-24.04.oci.tar || fail "run make dev-ami first"
docker image inspect nephos-appliance:dev >/dev/null || fail "run make appliance first"
test -x bin/nephos || fail "run make build first"

test_home="$(mktemp -d /tmp/nephos-vpc-subnet-smoke.XXXXXX)"
[[ "$test_home" == /tmp/nephos-vpc-subnet-smoke.* ]] || fail "unexpected temporary home path"
started=false
nft_available=false
snapshot_host() {
    local prefix="$1"
    readlink /proc/self/ns/net >"$prefix.netns"
    ip -j link show | jq -S . >"$prefix.links"
    ip -j route show table all | jq -S . >"$prefix.routes"
    ip -j rule show | jq -S . >"$prefix.rules"
    if [ "$nft_available" = true ]; then
        sudo -n nft -j list ruleset | jq -S 'walk(if type == "object" then del(.counter, .packets, .bytes) else . end)' >"$prefix.nft"
    fi
}
cleanup() {
    local result=$?
    trap - EXIT
    if [ "$started" = true ]; then
        if [ "$(docker inspect -f '{{index .Config.Labels "io.nephos.appliance"}}' nephos 2>/dev/null || true)" = true ]; then
            if [ "$result" -ne 0 ]; then docker logs nephos >&2 || true; fi
            docker rm -f nephos >/dev/null || result=1
        fi
        if [ "$(docker volume inspect -f '{{index .Labels "io.nephos.appliance"}}' nephos-data 2>/dev/null || true)" = true ]; then
            docker volume rm nephos-data >/dev/null || result=1
        fi
    fi
    if [ -f "$test_home/before.netns" ]; then
        snapshot_host "$test_home/after" || result=1
        for kind in netns links routes rules; do
            if ! cmp -s "$test_home/before.$kind" "$test_home/after.$kind"; then
                echo "vpc-subnet-smoke: host $kind changed" >&2
                diff -u "$test_home/before.$kind" "$test_home/after.$kind" >&2 || true
                result=1
            fi
        done
        if [ "$nft_available" = true ] && ! cmp -s "$test_home/before.nft" "$test_home/after.nft"; then
            echo "vpc-subnet-smoke: host nftables changed" >&2
            diff -u "$test_home/before.nft" "$test_home/after.nft" >&2 || true
            result=1
        fi
    fi
    rm -r -- "$test_home"
    exit "$result"
}
trap cleanup EXIT

if command -v nft >/dev/null && command -v sudo >/dev/null && sudo -n nft -j list ruleset >/dev/null 2>&1; then
    nft_available=true
elif [ "${CI:-}" = true ]; then
    fail "native CI must have readable host nftables rules (install nftables and grant passwordless sudo)"
else
    echo "vpc-subnet-smoke: host nftables snapshot unavailable here; native CI requires it" >&2
fi
snapshot_host "$test_home/before"
started=true
HOME="$test_home" bin/nephos up
[ "$(HOME="$test_home" bin/nephos status)" = ready ] || fail "appliance is not ready"
credentials="$test_home/.nephos/credentials"
[ "$(stat -c '%a' "$credentials")" = 600 ] || fail "credential is not private"

vpc_a="$(HOME="$test_home" bin/nephos vpc create 'Lab East' --cidr-block 10.0.0.0/16 --wait -o json)"
jq -e '.state == "available" and .cidr_block == "10.0.0.0/16"' <<<"$vpc_a" >/dev/null || fail "first VPC not available"
vpc_a_id="$(jq -r '.id' <<<"$vpc_a")"
names_a="$(docker exec nephos ip netns list | awk '{print $1}' | sort)"
[[ "$names_a" =~ ^nx-vpc-[0-9]+$ ]] || fail "unexpected first VPC namespace: $names_a"

vpc_b="$(HOME="$test_home" bin/nephos vpc create 'Lab West' --cidr-block 10.0.0.0/16 --wait -o json)"
jq -e '.state == "available" and .cidr_block == "10.0.0.0/16"' <<<"$vpc_b" >/dev/null || fail "second same-CIDR VPC not available"
vpc_b_id="$(jq -r '.id' <<<"$vpc_b")"
[ "$vpc_a_id" != "$vpc_b_id" ] || fail "overlapping VPCs share an ID"
names_b="$(docker exec nephos ip netns list | awk '{print $1}' | sort)"
vpc_b_ns="$(comm -13 <(printf '%s\n' "$names_a") <(printf '%s\n' "$names_b"))"
[[ "$vpc_b_ns" =~ ^nx-vpc-[0-9]+$ ]] || fail "second VPC did not get an isolated namespace"
[ "$(printf '%s\n' "$names_b" | wc -l)" -eq 2 ] || fail "expected exactly two VPC namespaces"
vpc_a_ns="$names_a"

subnet_a="$(HOME="$test_home" bin/nephos subnet create 'App α' --vpc "$vpc_a_id" --cidr-block 10.0.1.0/24 --availability-zone local-1a --wait -o json)"
subnet_b="$(HOME="$test_home" bin/nephos subnet create 'App β' --vpc "$vpc_b_id" --cidr-block 10.0.2.0/24 --availability-zone local-1a --wait -o json)"
jq -e --arg id "$vpc_a_id" '.state == "available" and .vpc_id == $id' <<<"$subnet_a" >/dev/null || fail "first subnet not available"
jq -e --arg id "$vpc_b_id" '.state == "available" and .vpc_id == $id' <<<"$subnet_b" >/dev/null || fail "second subnet not available"
subnet_a_id="$(jq -r '.id' <<<"$subnet_a")"
subnet_b_id="$(jq -r '.id' <<<"$subnet_b")"

gateway_addresses() { docker exec nephos ip -n "$1" -4 -o addr show dev nxr0; }
gateway_a="$(gateway_addresses "$vpc_a_ns")"
gateway_b="$(gateway_addresses "$vpc_b_ns")"
grep -q 'inet 10.0.1.1/32 ' <<<"$gateway_a" || fail "first gateway missing"
grep -q 'inet 10.0.2.1/32 ' <<<"$gateway_b" || fail "second gateway missing"
[[ "$gateway_a" != *'10.0.2.1/32'* ]] || fail "second gateway leaked into first VPC"
[[ "$gateway_b" != *'10.0.1.1/32'* ]] || fail "first gateway leaked into second VPC"

vpc_page="$(HOME="$test_home" bin/nephos vpc list --limit 1 -o json)"
jq -e '.items | length == 1' <<<"$vpc_page" >/dev/null || fail "first VPC page size is wrong"
vpc_cursor="$(jq -r '.next_page_token // empty' <<<"$vpc_page")"
[ -n "$vpc_cursor" ] || fail "VPC pagination cursor missing"
vpc_next="$(HOME="$test_home" bin/nephos vpc list --limit 1 --page-token "$vpc_cursor" -o json)"
jq -e '.items | length == 1' <<<"$vpc_next" >/dev/null || fail "second VPC page size is wrong"
[ "$(jq -r '.items[0].id' <<<"$vpc_page")" != "$(jq -r '.items[0].id' <<<"$vpc_next")" ] || fail "VPC pagination repeated an item"
subnet_page="$(HOME="$test_home" bin/nephos subnet list --limit 1 -o json)"
subnet_cursor="$(jq -r '.next_page_token // empty' <<<"$subnet_page")"
[ -n "$subnet_cursor" ] || fail "subnet pagination cursor missing"
subnet_next="$(HOME="$test_home" bin/nephos subnet list --limit 1 --page-token "$subnet_cursor" -o json)"
jq -e '.items | length == 1' <<<"$subnet_next" >/dev/null || fail "second subnet page size is wrong"
[ "$(jq -r '.items[0].id' <<<"$subnet_page")" != "$(jq -r '.items[0].id' <<<"$subnet_next")" ] || fail "subnet pagination repeated an item"

url=http://127.0.0.1:7788
[ "$(curl -sS -o /dev/null -w '%{http_code}' "$url/v1/workspaces/default/vpcs")" = 401 ] || fail "anonymous VPC list was accepted"
[ "$(awk '{printf "header = \"Authorization: Bearer %s\"\n", $0}' "$credentials" | curl -sS --config - -H 'Last-Event-ID: invalid' -o /dev/null -w '%{http_code}' "$url/v1/events")" = 400 ] || fail "invalid event cursor was accepted"
events="$(awk '{printf "header = \"Authorization: Bearer %s\"\n", $0}' "$credentials" | curl -s --no-buffer --max-time 2 --config - "$url/v1/events" || [ "$?" -eq 28 ])"
printf '%s\n' "$events" | sed -n 's/^data: //p' | jq -s -e --arg a "$vpc_a_id" --arg b "$vpc_b_id" 'any(.[]; .resource_id == $a) and any(.[]; .resource_id == $b)' >/dev/null || fail "durable events missing VPC creates"
printf '%s\n' "$events" | awk '/^id: / { if ($2 <= prior) exit 1; prior=$2; count++ } END { if (count < 4) exit 1 }' || fail "event IDs were not ordered"

HOME="$test_home" bin/nephos down
HOME="$test_home" bin/nephos up
[ "$(docker exec nephos ip netns list | awk '{print $1}' | sort)" = "$names_b" ] || fail "restart changed VPC short indexes"
for id in "$vpc_a_id" "$vpc_b_id"; do
    HOME="$test_home" bin/nephos vpc describe "$id" -o json | jq -e '.state == "available"' >/dev/null || fail "VPC $id lost after restart"
done
for id in "$subnet_a_id" "$subnet_b_id"; do
    HOME="$test_home" bin/nephos subnet describe "$id" -o json | jq -e '.state == "available"' >/dev/null || fail "subnet $id lost after restart"
done
gateway_a="$(gateway_addresses "$vpc_a_ns")"
gateway_b="$(gateway_addresses "$vpc_b_ns")"
grep -q 'inet 10.0.1.1/32 ' <<<"$gateway_a" || fail "first gateway lost after restart"
grep -q 'inet 10.0.2.1/32 ' <<<"$gateway_b" || fail "second gateway lost after restart"

HOME="$test_home" bin/nephos subnet delete "$subnet_a_id" --wait >/dev/null
gateway_a="$(gateway_addresses "$vpc_a_ns")"
[[ "$gateway_a" != *'10.0.1.1/32'* ]] || fail "deleted subnet gateway remains"
HOME="$test_home" bin/nephos vpc delete "$vpc_a_id" --wait >/dev/null
remaining_names="$(docker exec nephos ip netns list | awk '{print $1}')"
[[ "$remaining_names" != *"$vpc_a_ns"* ]] || fail "deleted VPC namespace remains"
HOME="$test_home" bin/nephos subnet delete "$subnet_b_id" --wait >/dev/null
HOME="$test_home" bin/nephos vpc delete "$vpc_b_id" --wait >/dev/null
[ -z "$(docker exec nephos ip netns list)" ] || fail "VPC namespaces remain after deletion"

echo "vpc-subnet-smoke: overlapping VPC isolation, gateways, API/CLI, events, restart, and deletion passed"
