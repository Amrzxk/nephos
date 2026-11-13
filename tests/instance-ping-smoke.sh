#!/usr/bin/env bash
# Real-Docker packet/restart path; observers and faults stay in this appliance.
set -euo pipefail
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"
fail() { echo "instance-ping-smoke: $*" >&2; exit 1; }
passed() { echo "instance-ping-smoke: PASS $*"; }
for tool in docker jq ip readlink go cmp; do
    command -v "$tool" >/dev/null || fail "required tool $tool is missing"
done
if docker container inspect nephos >/dev/null 2>&1; then fail "container nephos already exists; refusing to replace it"; fi
if docker volume inspect nephos-data >/dev/null 2>&1; then fail "volume nephos-data already exists; refusing to replace it"; fi
test -f dist/ubuntu-24.04.oci.tar || fail "run make dev-ami first"
docker image inspect nephos-appliance:dev >/dev/null || fail "run make appliance first"
test -x bin/nephos || fail "run make build first"

test_home="$(mktemp -d /tmp/nephos-instance-smoke.XXXXXX)"
[[ "$test_home" == /tmp/nephos-instance-smoke.* ]] || fail "unexpected temporary home"
container_id=""
volume_created=""
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
    if [ -n "$container_id" ]; then
        if [ "$(docker inspect -f '{{.Id}}' nephos 2>/dev/null || true)" = "$container_id" ] &&
           [ "$(docker inspect -f '{{index .Config.Labels "io.nephos.appliance"}}' nephos 2>/dev/null || true)" = true ]; then
            if [ "$result" -ne 0 ]; then
                docker logs nephos >"$test_home/appliance.log" 2>&1 || true
                tail -100 "$test_home/appliance.log" >&2 || true
            fi
            docker rm -f "$container_id" >/dev/null || result=1
        else
            echo "instance-ping-smoke: test container identity changed; preserving current object" >&2
            result=1
        fi
    fi
    if [ -n "$volume_created" ]; then
        if [ "$(docker volume inspect -f '{{.CreatedAt}}' nephos-data 2>/dev/null || true)" = "$volume_created" ] &&
           [ "$(docker volume inspect -f '{{index .Labels "io.nephos.appliance"}}' nephos-data 2>/dev/null || true)" = true ]; then
            docker volume rm nephos-data >/dev/null || result=1
        else
            echo "instance-ping-smoke: test volume identity changed; preserving current object" >&2
            result=1
        fi
    fi
    if [ -f "$test_home/before.netns" ]; then
        snapshot_host "$test_home/after" || result=1
        for kind in netns links routes rules; do
            if ! cmp -s "$test_home/before.$kind" "$test_home/after.$kind"; then
                echo "instance-ping-smoke: host $kind changed" >&2
                diff -u "$test_home/before.$kind" "$test_home/after.$kind" >&2 || true
                result=1
            fi
        done
        if [ "$nft_available" = true ] && ! cmp -s "$test_home/before.nft" "$test_home/after.nft"; then
            echo "instance-ping-smoke: host nftables changed" >&2
            diff -u "$test_home/before.nft" "$test_home/after.nft" >&2 || true
            result=1
        fi
    fi
    if [ "$result" -eq 0 ]; then
        rm -r -- "$test_home"
        passed "teardown and host network hygiene"
    else
        echo "instance-ping-smoke: evidence retained at $test_home" >&2
    fi
    exit "$result"
}
trap cleanup EXIT
if command -v nft >/dev/null && command -v sudo >/dev/null && sudo -n nft -j list ruleset >/dev/null 2>&1; then
    nft_available=true
elif [ "${CI:-}" = true ]; then
    fail "native CI requires readable host nftables (install nftables and grant passwordless sudo)"
else
    echo "instance-ping-smoke: host nftables snapshot unavailable here; native CI requires it" >&2
fi
snapshot_host "$test_home/before"
CGO_ENABLED=0 GOOS=linux go build -o "$test_home/hook-observer" ./tests/fixtures/hookobserver
CGO_ENABLED=0 GOOS=linux go build -o "$test_home/task-limit-helper" ./tests/fixtures/tasklimit
cli() { HOME="$test_home" bin/nephos "$@"; }
command_console() { cli console "$@" </dev/null; }
cli up
container_id="$(docker inspect -f '{{.Id}}' nephos)"
volume_created="$(docker volume inspect -f '{{.CreatedAt}}' nephos-data)"
[ "$(cli status)" = ready ] || fail "appliance not ready"
docker exec nephos mv /usr/local/bin/nephos-hook /usr/local/bin/nephos-hook.production
docker cp "$test_home/hook-observer" nephos:/usr/local/bin/nephos-hook

vpc_a="$(cli vpc create A --cidr-block 10.0.0.0/16 --wait -o json | jq -r .id)"
vpc_a_ns="$(docker exec nephos ip netns list | awk '{print $1}')"
[[ "$vpc_a_ns" =~ ^nx-vpc-[0-9]+$ ]] || fail "first VPC namespace missing"
vpc_b="$(cli vpc create B --cidr-block 10.0.0.0/16 --wait -o json | jq -r .id)"
subnet_a1="$(cli subnet create a1 --vpc "$vpc_a" --cidr-block 10.0.1.0/24 --availability-zone local-1a --wait -o json | jq -r .id)"
subnet_a2="$(cli subnet create a2 --vpc "$vpc_a" --cidr-block 10.0.2.0/24 --availability-zone local-1a --wait -o json | jq -r .id)"
subnet_b1="$(cli subnet create b1 --vpc "$vpc_b" --cidr-block 10.0.1.0/24 --availability-zone local-1a --wait -o json | jq -r .id)"
one="$(cli instance run one --subnet "$subnet_a1" --wait -o json)"
two="$(cli instance run two --subnet "$subnet_a2" --wait -o json)"
overlap="$(cli instance run overlap-source --subnet "$subnet_b1" --wait -o json)"
remote="$(cli instance run remote-only --subnet "$subnet_b1" --wait -o json)"
one_id="$(jq -r .id <<<"$one")"
two_id="$(jq -r .id <<<"$two")"
overlap_id="$(jq -r .id <<<"$overlap")"
remote_id="$(jq -r .id <<<"$remote")"
for spec in "$one:10.0.1.4" "$two:10.0.2.4" "$overlap:10.0.1.4" "$remote:10.0.1.5"; do
    item="${spec%:*}"; expected_ip="${spec##*:}"
    jq -e --arg ip "$expected_ip" '.state == "running" and .private_ip == $ip and .observed_generation == .generation' <<<"$item" >/dev/null || fail "wrong running state/IP: $item"
    id="$(jq -r .id <<<"$item")"
    proof="$(docker exec nephos cat "/run/nephos/hook-proofs/$id")"
    jq -e --arg id "$id" --arg ip "$expected_ip/24" '.instance_id == $id and .addresses == [$ip]' <<<"$proof" >/dev/null || fail "eth0 was not configured before PID 1"
done
passed "reserved-safe independent IPs and eth0 before PID 1"

# Exercise the real authenticated console path, including stdin half-close.
set +e
printf 'x\0y' | cli console one -- /bin/sh -c 'cat; printf "tail\000"; printf "err\000" >&2; exit 7' >"$test_home/console.out" 2>"$test_home/console.err"
console_status=$?
set -e
[ "$console_status" -eq 7 ] || fail "remote exit 7 became $console_status"
cmp -s "$test_home/console.out" <(printf 'x\0ytail\0') || fail "console stdout bytes changed"
tail -c 4 "$test_home/console.err" | cmp -s - <(printf 'err\0') || fail "console stderr bytes changed"
passed "console separate NUL bytes, trailing output, EOF and exit 7"
cli console one -- /bin/sh -c 'cat > /root/nephos-task-limit-helper && chmod 0755 /root/nephos-task-limit-helper' <"$test_home/task-limit-helper" >/dev/null
limit_result="$(command_console one -- systemd-run --quiet --scope --property=TasksMax=infinity /root/nephos-task-limit-helper)"
jq -e '.Limit == 512 and .Peak == 512 and .Refused and .ExistingAlive and .Children > 0 and .After < .Peak' <<<"$limit_result" >/dev/null || fail "fixed 512-task ceiling not enforced: $limit_result"
[ "$(command_console one -- /bin/echo responsive)" = responsive ] || fail "exec unresponsive after task ceiling"
passed "512-task refusal, existing-task survival and worker release"

witness_start() {
    command_console "$1" -- nft add table inet witness
    command_console "$1" -- nft add chain inet witness input '{ type filter hook input priority 0; policy accept; }'
    command_console "$1" -- nft add rule inet witness input icmp type echo-request counter
}
witness_packets() {
    command_console "$1" -- nft -j list chain inet witness input | jq '[.nftables[] | .rule.expr[]? | .counter.packets? // empty] | add // 0'
}
witness_start two
command_console one -- ping -c 3 -W 2 10.0.2.4
[ "$(witness_packets two)" -ge 3 ] || fail "positive cross-subnet requests missing at destination"
command_console two -- ping -c 1 -W 2 10.0.1.4
[ -z "$(command_console one -- getcap /usr/bin/ping)" ] || fail "ping retained NET_RAW file capability"
passed "real bidirectional cross-subnet ICMP"

witness_start remote-only
command_console overlap-source -- ping -c 1 -W 2 10.0.1.5
remote_before="$(witness_packets remote-only)"
[ "$remote_before" -ge 1 ] || fail "overlap fixture lacks positive control"
if command_console one -- ping -c 2 -W 1 10.0.1.5; then fail "cross-VPC request unexpectedly succeeded"; fi
[ "$(witness_packets remote-only)" -eq "$remote_before" ] || fail "cross-VPC request reached remote-only destination"
passed "overlapping-VPC isolation with remote-only destination witness"

spoof_before="$(witness_packets two)"
drop_packets() { docker exec nephos ip netns exec "$vpc_a_ns" nft -j list chain inet nephos source_check | jq '[.nftables[] | .rule.expr[]? | .counter.packets? // empty] | add // 0'; }
drops_before="$(drop_packets)"
command_console one -- ip addr add 10.0.1.5/32 dev eth0
if command_console one -- ping -I 10.0.1.5 -c 2 -W 1 10.0.2.4; then fail "spoofed source unexpectedly succeeded"; fi
[ "$(witness_packets two)" -eq "$spoof_before" ] || fail "forged packet reached destination"
[ "$(drop_packets)" -ge "$((drops_before+2))" ] || fail "anti-spoof drop counter did not advance"
command_console one -- ip addr del 10.0.1.5/32 dev eth0
command_console one -- ping -c 1 -W 2 10.0.2.4
passed "source spoof rejected with drop and destination evidence"

docker exec nephos touch /run/nephos/force-hook-failure
if cli instance run hook-fail --subnet "$subnet_a1" --wait --timeout 20s >"$test_home/fail.out" 2>"$test_home/fail.err"; then fail "forced hook failure reported success"; fi
failed="$(cli instance describe hook-fail -o json)"
jq -e '.state == "failed" and .observed_generation < .generation and (.state_reason | contains("error executing hook") and contains("/usr/local/bin/nephos-hook"))' <<<"$failed" >/dev/null || fail "hook failure did not produce visible failed state and hook reason: $failed"
failed_id="$(jq -r .id <<<"$failed")"
docker exec nephos cat "/run/nephos/hook-proofs/$failed_id.failed" | jq -e --arg id "$failed_id" '.instance_id == $id and .forced_failure' >/dev/null || fail "forced-failure observer did not run for this instance"
[ "$(docker exec nephos podman inspect --format '{{.State.Running}}' "$failed_id")" = false ] || fail "failed hook ran PID 1"
cli instance terminate "$failed_id" --wait >/dev/null
docker exec nephos rm /run/nephos/force-hook-failure
passed "hook failure refuses PID 1 and records failed reason"

# Retained roots and stable desired identities survive a whole-appliance stop.
# Runtime/network observers are test-only; learner access remains the console.
identity_witness() {
    local id="$1" item="$2" runtime mac
    runtime="$(docker exec nephos podman inspect --format '{{.Id}}' "$id")"
    mac="$(command_console "$id" -- cat /sys/class/net/eth0/address)"
    jq -cS --arg runtime "$runtime" --arg mac "$mac" '{id,eni_id,private_ip,runtime:$runtime,mac:$mac}' <<<"$item"
}
for id in "$one_id" "$two_id" "$overlap_id" "$remote_id"; do
    item="$(cli instance describe "$id" -o json)"
    identity_witness "$id" "$item" >"$test_home/$id.before"
    command_console "$id" -- /bin/sh -c 'printf retained-root-marker > /root/m1-retained-marker'
done
cli down
cli up
[ "$(docker inspect -f '{{.Id}}' nephos)" = "$container_id" ] || fail "ordinary restart replaced the appliance"
for id in "$one_id" "$two_id" "$overlap_id" "$remote_id"; do
    item="$(cli instance describe "$id" -o json)"
    jq -e '.state == "running" and .observed_generation == .generation' <<<"$item" >/dev/null || fail "restart did not restore running state"
    after="$(identity_witness "$id" "$item")"
    [ "$after" = "$(<"$test_home/$id.before")" ] || fail "restart changed instance/ENI/IP/MAC/runtime identity"
    [ "$(command_console "$id" -- cat /root/m1-retained-marker)" = retained-root-marker ] || fail "restart lost writable root marker"
    printf 'instance-ping-smoke: preserved %s\n' "$after"
done
command_console one -- ping -c 3 -W 2 10.0.2.4
command_console two -- ping -c 1 -W 2 10.0.1.4
passed "down/up preserves four roots, identities, addresses and bidirectional ping"

# Already-running drift cannot be hidden by a running-only fast path. Remove
# only this owned router veth; the normal durable resync must restore it.
one_eni="$(jq -r .eni_id <<<"$one")"
one_link="$(docker exec nephos ip -j -n "$vpc_a_ns" link show | jq -r --arg prefix "nephos:eni:$one_eni " '.[] | select(.ifalias // "" | startswith($prefix)) | .ifname')"
[[ "$one_link" =~ ^ve[1-9][0-9]*$ ]] || fail "owned drift target is ambiguous"
docker exec nephos ip -n "$vpc_a_ns" link delete "$one_link"
drift_deadline=$((SECONDS+75))
while ! docker exec nephos ip -n "$vpc_a_ns" link show "$one_link" >/dev/null 2>&1; do
    [ "$SECONDS" -lt "$drift_deadline" ] || fail "running ENI drift was not repaired by resync"
    sleep 1
done
[ "$(command_console one -- cat /root/m1-retained-marker)" = retained-root-marker ] || fail "running repair replaced root"
[ "$(identity_witness "$one_id" "$(cli instance describe "$one_id" -o json)")" = "$(<"$test_home/$one_id.before")" ] || fail "running repair changed identity"
command_console one -- ping -c 3 -W 2 10.0.2.4
passed "already-running missing ENI repaired with preserved root and real ping"

for id in "$one_id" "$two_id" "$overlap_id" "$remote_id"; do cli instance terminate "$id" --wait >/dev/null; done
[ "$(cli instance list -o json | jq '.items | length')" -eq 0 ] || fail "instance rows remain"
[ -z "$(docker exec nephos podman ps -aq --filter label=io.nephos.managed=true)" ] || fail "managed runtime containers remain"
for id in "$subnet_a1" "$subnet_a2" "$subnet_b1"; do cli subnet delete "$id" --wait >/dev/null; done
for id in "$vpc_a" "$vpc_b"; do cli vpc delete "$id" --wait >/dev/null; done
[ -z "$(docker exec nephos ip netns list)" ] || fail "VPC namespaces remain"
passed "resource teardown removes runtime and network objects"
