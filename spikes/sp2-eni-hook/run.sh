#!/usr/bin/env bash
# SP2 — ENI plumbing through an OCI createRuntime hook.
#
# Validates ADR-0004 R3 (a network namespace wired by Nephos BEFORE PID 1
# starts, owned by the instance's user namespace). Mitigates RISKS T1.
#
# The four things that have to be true, or M1's instance launch does not work:
#
#   1. eth0 exists before the container's first process runs. No boot race.
#   2. The instance's netns is owned by the instance's USER namespace, so
#      instance root holds CAP_NET_ADMIN there and can run ufw.
#   3. Stop/start re-plumbs correctly, with the same address.
#   4. If plumbing fails, the instance does NOT start. Failing closed is the
#      entire reason the hook exists.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=../common.sh
source "$HERE/../common.sh"

RESULTS="$HERE/results"
VPC_NS="nx-vpc-sp2"
ROUTER_IF="nxr0"
GATEWAY="10.50.1.1"
INSTANCE_IP="10.50.1.4"

main() {
    mkdir -p "$RESULTS"
    spike::section "SP2: ENI plumbing via an OCI hook"
    spike::record_host "$RESULTS/host.txt"

    build_binaries
    ensure_appliance
    install_hook
    prepare_vpc
    start_plumbd

    check_plumbed_before_pid1
    check_netns_ownership
    check_instance_firewall
    check_stop_start_replumb
    check_fails_closed

    spike::section "SP2 complete"
    spike::verdict_summary "$RESULTS/verdicts.tsv"
}

build_binaries() {
    spike::section "Building the hook and the plumbing daemon"
    command -v go >/dev/null 2>&1 || { echo "go is not on PATH" >&2; return 1; }
    ( cd "$HERE" && CGO_ENABLED=0 GOOS=linux go build -o "$RESULTS/nephos-hook" ./hook )
    ( cd "$HERE" && CGO_ENABLED=0 GOOS=linux go build -o "$RESULTS/plumbd" ./plumbd )
    spike::log "  built nephos-hook and plumbd"
}

ensure_appliance() {
    spike::section "Ensuring the appliance is running"
    if ! docker ps -q --filter "name=^${SPIKE_APPLIANCE_NAME}$" | grep -q .; then
        spike::run_retry 3 docker build -q -t "$SPIKE_APPLIANCE_IMAGE" \
            -f "$HERE/../sp1-appliance/Dockerfile.appliance" "$HERE/../sp1-appliance"
        spike::appliance_up
    else
        spike::log "  reusing the running appliance"
    fi
    spike::cleanup_instances
    spike::appliance_exec podman image exists "$SPIKE_AMI_REF" 2>/dev/null || spike::load_ami
}

install_hook() {
    spike::section "Registering the OCI hook with Podman"
    docker cp "$RESULTS/nephos-hook" "$SPIKE_APPLIANCE_NAME:/usr/local/bin/nephos-hook"
    docker cp "$RESULTS/plumbd" "$SPIKE_APPLIANCE_NAME:/usr/local/bin/plumbd"

    # The hook applies ONLY to containers carrying the Nephos annotation, and
    # runs at createRuntime: after the namespaces exist, before PID 1
    # (ARCHITECTURE §3.3).
    spike::appliance_exec sh -c 'mkdir -p /usr/share/containers/oci/hooks.d && cat > /usr/share/containers/oci/hooks.d/nephos.json <<JSON
{
  "version": "1.0.0",
  "hook": { "path": "/usr/local/bin/nephos-hook" },
  "when": { "annotations": { "io.nephos.instance-id": ".*" } },
  "stages": [ "createRuntime" ]
}
JSON'
    spike::assert "the hook is registered in Podman's hooks directory" \
        "spike::appliance_exec test -f /usr/share/containers/oci/hooks.d/nephos.json" \
        "/usr/share/containers/oci/hooks.d/nephos.json"
}

prepare_vpc() {
    spike::section "Preparing the VPC router namespace"
    local setup_out setup_rc=0
    setup_out="$(spike::appliance_exec sh -c "
        mkdir -p /run/netns || exit 1
        ip netns delete $VPC_NS 2>/dev/null
        ip netns add $VPC_NS || exit 2
        ip netns exec $VPC_NS sysctl -qw net.ipv4.ip_forward=1 || exit 3
        ip netns exec $VPC_NS ip link add $ROUTER_IF type dummy || exit 4
        ip netns exec $VPC_NS ip link set $ROUTER_IF up || exit 5
        ip netns exec $VPC_NS ip addr add $GATEWAY/32 dev $ROUTER_IF || exit 6
        echo prepared
    " 2>&1)" || setup_rc=$?
    spike::log "  setup: ${setup_out:-no output} (rc=$setup_rc)"

    local addrs
    addrs="$(spike::appliance_exec ip netns exec "$VPC_NS" ip -o addr show "$ROUTER_IF" 2>&1 | tr -d '\r' | head -1)"
    spike::assert "the VPC router namespace exists with its gateway address" \
        "echo '$addrs' | grep -q '$GATEWAY'" \
        "${addrs:-no address found}"
}

start_plumbd() {
    spike::section "Starting the plumbing daemon"
    # pkill -x matches the process NAME. `pkill -f /usr/local/bin/plumbd` would
    # also match the `sh -c` running it, so it kills its own shell and the
    # caller sees SIGTERM.
    spike::appliance_exec sh -c 'pkill -x plumbd 2>/dev/null; rm -f /run/nephos/registry.json; exit 0' || true
    # docker exec -d rather than nohup inside an attached exec: the attached
    # form keeps the session open and the caller hangs waiting for it.
    docker exec -d "$SPIKE_APPLIANCE_NAME" \
        sh -c '/usr/local/bin/plumbd >/tmp/plumbd.log 2>&1'
    sleep 2
    spike::assert "plumbd is listening on the hook socket" \
        "spike::appliance_exec test -S /run/nephos/hook.sock" \
        "/run/nephos/hook.sock"
}

register_eni() {
    local instance_id="$1" iface="$2" ip="$3"
    spike::appliance_exec /usr/local/bin/plumbd register "{
        \"instance_id\": \"$instance_id\",
        \"spec\": {
            \"Iface\": \"$iface\",
            \"PrivateIP\": \"$ip\",
            \"PrefixLen\": 24,
            \"Gateway\": \"$GATEWAY\",
            \"MTU\": 1500,
            \"RouterNS\": \"$VPC_NS\"
        }
    }"
}

# --- 1. eth0 exists before PID 1 (ADR-0004 R3, validation 5) ---------------

check_plumbed_before_pid1() {
    spike::section "eth0 is present before the first process runs"
    register_eni i-sp2-a vesp2a "$INSTANCE_IP"

    # The container's ONLY command dumps its interfaces. If eth0 is there, the
    # hook ran before PID 1 — which is what stops cloud-init and sshd racing an
    # unconfigured network.
    spike::appliance_exec podman rm -f sp2-boot >/dev/null 2>&1 || true
    spike::appliance_exec podman run --name sp2-boot \
        --userns=auto --network none --cap-add NET_ADMIN \
        --annotation io.nephos.instance-id=i-sp2-a \
        --entrypoint '["/bin/sh","-c","ip -o addr show eth0; ip route show default"]' \
        "$SPIKE_AMI_REF" > "$RESULTS/first-process-output.txt" 2>&1 || true

    local out
    out="$(cat "$RESULTS/first-process-output.txt" 2>/dev/null | tr -d '\r')"
    spike::log "  first process saw: $(echo "$out" | head -2 | tr '\n' ' ')"

    spike::assert "the first process in the instance already sees eth0 (ADR-0004 R3)" \
        "echo '$out' | grep -q 'eth0'" \
        "no boot race: the hook completed before PID 1"

    spike::assert "eth0 already carries its private address" \
        "echo '$out' | grep -q '$INSTANCE_IP'" \
        "address $INSTANCE_IP/24"

    spike::assert "the default route via the subnet gateway is already installed" \
        "echo '$out' | grep -q 'default via $GATEWAY'" \
        "default via $GATEWAY"

    # The router-side veth is deliberately NOT inspected here. This container
    # exits as soon as it has printed, and its network namespace goes with it,
    # taking both ends of the veth pair. That is correct teardown, not a leak;
    # the router end is checked against the long-lived instance below.
}

# --- 2. The netns belongs to the instance's user namespace -----------------

check_netns_ownership() {
    spike::section "Network namespace ownership"
    register_eni i-sp2-b vesp2b 10.50.1.5

    spike::appliance_exec podman rm -f sp2-live >/dev/null 2>&1 || true
    spike::appliance_exec podman run -d --name sp2-live \
        --systemd=always --userns=auto --network none --cap-add NET_ADMIN \
        --memory 512m --pids-limit 512 \
        --annotation io.nephos.instance-id=i-sp2-b \
        "$SPIKE_AMI_REF" >/dev/null 2>&1 || true

    sleep 3
    local host_uid
    host_uid="$(spike::instance_host_uid sp2-live || echo "")"
    spike::metric "instance_host_uid" "${host_uid:-unknown}"

    spike::assert "the instance is running with its own user namespace" \
        "[ -n '${host_uid:-}' ] && [ '${host_uid:-0}' -gt 0 ]" \
        "instance root maps to appliance uid ${host_uid:-unknown}"

    spike::assert "the instance's eth0 survived into the running container" \
        "spike::appliance_exec podman exec sp2-live ip -o addr show eth0 | grep -q 10.50.1.5" \
        "eth0 holds 10.50.1.5"

    # The router side, checked while the instance is actually running.
    local routes proxy_arp
    routes="$(spike::appliance_exec ip netns exec "$VPC_NS" ip route show 2>&1 | tr -d '\r' | tr '\n' ' ')"
    spike::assert "the router end of the veth is in the VPC namespace with a /32 route" \
        "echo '$routes' | grep -q '10.50.1.5'" \
        "routes in $VPC_NS: ${routes:-none}"

    # No layer-2 domain exists, so the router answers ARP on behalf of every
    # address the instance asks about and forwards by host route (ADR-0005).
    proxy_arp="$(spike::appliance_exec sh -c "ip netns exec $VPC_NS cat /proc/sys/net/ipv4/conf/vesp2b/proxy_arp 2>/dev/null" | tr -d '\r\n')"
    spike::assert "proxy ARP is enabled on the router end (no layer-2 domain)" \
        "[ '${proxy_arp:-0}' = '1' ]" \
        "proxy_arp=${proxy_arp:-missing} on vesp2b"

    local redirects
    redirects="$(spike::appliance_exec sh -c "ip netns exec $VPC_NS cat /proc/sys/net/ipv4/conf/vesp2b/send_redirects 2>/dev/null" | tr -d '\r\n')"
    spike::assert "ICMP redirects are disabled on the router end" \
        "[ '${redirects:-1}' = '0' ]" \
        "send_redirects=${redirects:-missing}: without this the router could tell an instance to bypass it, bypassing enforcement"
}

# --- 3. Instance root can firewall its own namespace -----------------------

check_instance_firewall() {
    spike::section "Instance root owns its own network namespace"
    # This is the point of R3: the netns is owned by the instance's user
    # namespace, so the learner can lock themselves out with ufw and recover
    # through the serial console. RISKS S3 accepts the kernel surface.
    spike::assert "instance root can add an nftables rule in its own netns (ADR-0004 R3)" \
        "spike::appliance_exec podman exec sp2-live nft add table inet sp2test" \
        "ran nft as instance root"

    spike::assert "instance root can bring its own interface down (real CAP_NET_ADMIN)" \
        "spike::appliance_exec podman exec sp2-live ip link set eth0 down && spike::appliance_exec podman exec sp2-live ip link set eth0 up" \
        "ip link set eth0 down/up succeeded inside the instance"

    spike::assert "none of that reached the appliance's own namespace" \
        "! spike::appliance_exec nft list ruleset 2>/dev/null | grep -q sp2test" \
        "the appliance ruleset is unaffected"
}

# --- 4. Stop/start re-plumbs (ADR-0004 validation 6) -----------------------

check_stop_start_replumb() {
    spike::section "Stop/start re-plumbing"
    spike::appliance_exec podman stop -t 15 sp2-live >/dev/null 2>&1 || true

    # The veth is destroyed with the container's namespace, so the router end
    # must be gone too. A leaked veth here would become an orphan the leak
    # checker has to find (ARCHITECTURE §7).
    local leaked
    leaked="$(spike::appliance_exec sh -c "ip netns exec $VPC_NS ip -o link show 2>/dev/null | grep -c vesp2b || true" | tr -d '\r\n')"
    spike::metric "router_veths_after_stop" "${leaked:-unknown}"

    spike::appliance_exec podman start sp2-live >/dev/null 2>&1 || true
    sleep 3

    spike::assert "the instance is re-plumbed on start, with the same address (validation 6)" \
        "spike::appliance_exec podman exec sp2-live ip -o addr show eth0 | grep -q 10.50.1.5" \
        "eth0 holds 10.50.1.5 again after stop/start"

    spike::assert "the router end is re-created in the VPC namespace" \
        "spike::appliance_exec ip netns exec $VPC_NS ip route show | grep -q 10.50.1.5" \
        "host route restored"
}

# --- 5. Failing closed ----------------------------------------------------

check_fails_closed() {
    spike::section "An instance that cannot be plumbed must not start"
    # No ENI is registered for this ID, so plumbd returns 404 and the hook
    # exits non-zero. Podman must refuse to start the container.
    #
    # This is the honesty rule in executable form: an instance that booted
    # without its network would look healthy and be unreachable, and Nephos
    # would be lying about what it had configured.
    spike::appliance_exec podman rm -f sp2-unregistered >/dev/null 2>&1 || true

    local rc=0
    spike::appliance_exec podman run -d --name sp2-unregistered \
        --systemd=always --userns=auto --network none \
        --annotation io.nephos.instance-id=i-sp2-does-not-exist \
        "$SPIKE_AMI_REF" >/dev/null 2>&1 || rc=$?

    spike::assert "podman refuses to start an instance whose ENI cannot be plumbed" \
        "[ $rc -ne 0 ]" \
        "podman run exited $rc (non-zero means the hook failed the container closed)"

    local state
    state="$(spike::appliance_exec podman inspect --format '{{.State.Status}}' sp2-unregistered 2>/dev/null | tr -d '\r\n' || echo absent)"
    spike::metric "unplumbed_container_state" "${state:-absent}"
    spike::assert "the unplumbed instance is not running" \
        "[ '${state:-absent}' != 'running' ]" \
        "state: ${state:-absent}"

    spike::appliance_exec sh -c 'cat /tmp/plumbd.log' > "$RESULTS/plumbd.log" 2>&1 || true
}

main "$@"
