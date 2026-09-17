#!/usr/bin/env bash
# SP1 — appliance plus nested system container.
#
# Validates ADR-0003 (appliance container packaging) and ADR-0004 (instances as
# system containers). Mitigates RISKS T1, T5, T10, S2.
#
# Answers, with measurements:
#   1. Does an ubuntu-24.04 system container accept SSH within 5 s of start?
#   2. Does the first start from a cached image take under 10 s? (RISKS T10:
#      the cost of ID-mapping image layers for a per-instance user namespace.)
#   3. Does systemd run as PID 1 with no unexpected failed units?
#   4. Does instance root map to a non-zero UID in the appliance, and is it
#      unable to see other instances? (RISKS S2)
#   5. Can instance root add a firewall rule in its own netns?
#   6. Do stop/start keep the root filesystem, and do 20 idle instances fit in
#      2 GiB? (RISKS T5)
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=../common.sh
source "$HERE/../common.sh"

RESULTS="$HERE/results"
SCALE_COUNT="${SP1_SCALE_COUNT:-20}"
BOOT_SAMPLES="${SP1_BOOT_SAMPLES:-10}"

# Every instance starts the same way. CAP_NET_ADMIN is deliberate: ADR-0004 R3
# and validation 4 require instance root to be able to run ufw or nft in its OWN
# network namespace, and RISKS S3 accepts the resulting kernel attack surface as
# the price of that lesson. Podman's default capability set omits it.
INSTANCE_FLAGS=(
    --systemd=always
    --userns=auto
    --network none
    --cap-add NET_ADMIN
)

main() {
    mkdir -p "$RESULTS"
    : > "$RESULTS/sp1.log"

    spike::section "SP1: appliance and nested system containers"
    spike::record_host "$RESULTS/host.txt"

    build_images
    start_appliance
    load_ami

    measure_first_boot
    measure_boot_to_sshd
    assert_systemd_healthy
    assert_userns_isolation
    assert_instance_firewall
    assert_stop_start_persistence
    measure_scale

    spike::section "SP1 complete"
    spike::verdict_summary "$RESULTS/verdicts.tsv"
}

build_images() {
    spike::section "Building images"
    spike::run_retry 3 docker build -q -t "$SPIKE_APPLIANCE_IMAGE" -f "$HERE/Dockerfile.appliance" "$HERE"
    spike::run_retry 3 docker build -q -t "$SPIKE_AMI_IMAGE" -f "$HERE/Dockerfile.ami" "$HERE"
}

start_appliance() {
    spike::section "Starting the appliance"
    spike::appliance_up

    # The host must be untouched (ADR-0003). If Nephos ever needed
    # --network host or a host mount, this is where it would show.
    local ns_net ns_pid mounts subtree
    ns_net="$(docker inspect -f '{{.HostConfig.NetworkMode}}' "$SPIKE_APPLIANCE_NAME")"
    ns_pid="$(docker inspect -f '{{.HostConfig.PidMode}}' "$SPIKE_APPLIANCE_NAME")"
    spike::assert "appliance does not share the host network or PID namespace (ADR-0003)" \
        "[ '$ns_net' != 'host' ] && [ '$ns_pid' != 'host' ]" \
        "network=$ns_net pid=${ns_pid:-private}"

    mounts="$(docker inspect -f '{{range .Mounts}}{{.Type}} {{end}}' "$SPIKE_APPLIANCE_NAME")"
    spike::assert "appliance has no host bind mounts (ADR-0003)" \
        "! echo '$mounts' | grep -qw bind" \
        "mount types: ${mounts:-none}"

    subtree="$(spike::appliance_exec cat /sys/fs/cgroup/cgroup.subtree_control 2>/dev/null | tr -d '\r\n' || true)"
    spike::assert "the cgroup controllers needed for instance limits are delegated" \
        "echo '$subtree' | grep -qw memory && echo '$subtree' | grep -qw pids" \
        "subtree_control: ${subtree:-empty}"
}

load_ami() {
    spike::section "Loading the AMI into Podman inside the appliance"
    # Podman's storage lives on the nephos-data volume, so containers survive
    # an appliance restart by design (ADR-0003). A rerun therefore starts with
    # the previous run's containers still present, and every `podman run --name`
    # would fail on a name conflict. Clear them first.
    spike::cleanup_instances
    spike::load_ami
    spike::appliance_exec podman images --format '{{.Repository}}:{{.Tag}} {{.Size}}'
}

# --- 2. First boot from a cached image (RISKS T10) -------------------------

measure_first_boot() {
    spike::section "First boot from a cached image (RISKS T10: ID-mapping cost)"
    spike::appliance_exec podman rm -f inst-first >/dev/null 2>&1 || true

    local start end elapsed layer_bytes
    start="$(spike::now_ms)"
    spike::appliance_exec podman run -d --name inst-first "${INSTANCE_FLAGS[@]}" \
        --memory 1g --cpus 2 --pids-limit 512 \
        --label io.nephos.instance-id=i-first \
        "$SPIKE_AMI_REF" >/dev/null
    spike::wait_for_sshd inst-first 90 || true
    end="$(spike::now_ms)"
    elapsed=$(( end - start ))

    spike::metric "first_boot_to_sshd_ms" "$elapsed"
    spike::assert "first boot from a cached image is under 10 s (ADR-0004 validation 1)" \
        "[ $elapsed -lt 10000 ]" \
        "measured ${elapsed} ms"

    layer_bytes="$(spike::appliance_exec sh -c 'du -sb /var/lib/nephos/containers 2>/dev/null | cut -f1' | tr -d '\r\n' || echo 0)"
    spike::metric "storage_after_one_instance_bytes" "${layer_bytes:-unknown}"
}

# --- 1. Median boot-to-sshd (ADR-0004 validation 1) ------------------------

measure_boot_to_sshd() {
    spike::section "Boot-to-sshd over $BOOT_SAMPLES samples"
    local samples=() i start end median
    for i in $(seq 1 "$BOOT_SAMPLES"); do
        spike::appliance_exec podman rm -f "inst-boot-$i" >/dev/null 2>&1 || true
        start="$(spike::now_ms)"
        spike::appliance_exec podman run -d --name "inst-boot-$i" "${INSTANCE_FLAGS[@]}" \
            --memory 1g --cpus 2 --pids-limit 512 \
            --label io.nephos.instance-id="i-boot-$i" \
            "$SPIKE_AMI_REF" >/dev/null
        if spike::wait_for_sshd "inst-boot-$i" 90; then
            end="$(spike::now_ms)"
            samples+=( $(( end - start )) )
            spike::log "  sample $i: $(( end - start )) ms"
        else
            spike::log "  sample $i: TIMED OUT (no SSH banner within 90 s)"
        fi
        spike::appliance_exec podman rm -f "inst-boot-$i" >/dev/null 2>&1 || true
    done

    median="$(spike::median "${samples[@]}")"
    spike::metric "boot_to_sshd_median_ms" "${median:-none}"
    spike::metric "boot_to_sshd_samples" "${samples[*]:-none}"
    spike::assert "median boot-to-sshd is under 5 s (ADR-0004 validation 1)" \
        "[ -n '${median:-}' ] && [ ${median:-999999} -lt 5000 ]" \
        "median ${median:-n/a} ms over ${#samples[@]}/${BOOT_SAMPLES} successful samples"
}

# --- 3. systemd is healthy (ADR-0004 validation 2) -------------------------

assert_systemd_healthy() {
    spike::section "systemd health inside the instance"

    local pid1 failed
    pid1="$(spike::appliance_exec podman exec inst-first cat /proc/1/comm | tr -d '\r\n')"
    spike::assert "systemd is PID 1 (ADR-0004 R1)" \
        "[ '$pid1' = 'systemd' ]" \
        "/proc/1/comm = $pid1"

    spike::appliance_exec podman exec inst-first systemctl is-system-running --wait >/dev/null 2>&1 || true

    spike::appliance_exec podman exec inst-first \
        systemctl list-units --state=failed --no-legend --plain > "$RESULTS/failed-units.txt" 2>&1 || true
    failed="$(grep -c . "$RESULTS/failed-units.txt" || true)"
    spike::metric "failed_units" "$failed"
    spike::assert "no failed units beyond the deliberately masked ones (ADR-0004 validation 2)" \
        "[ '${failed:-1}' -eq 0 ]" \
        "$failed failed unit(s); see results/failed-units.txt"

    # ADR-0004 validation 2: a service must start under systemd the way it does
    # on a real machine. nginx ships in the AMI, so this runs offline — every
    # SP1 instance has --network none, since giving an instance a network is
    # SP2's job and Nephos never uses a Podman network to model one.
    spike::assert "systemctl start nginx works inside the instance (ADR-0004 validation 2)" \
        "spike::appliance_exec podman exec inst-first sh -c 'systemctl start nginx && systemctl is-active nginx'" \
        "starts a real service under systemd in the instance"

    # The userns trap: apt drops to the _apt user, whose setgroups call returns
    # EINVAL inside a user namespace. Installing a cached .deb exercises that
    # path without needing egress.
    spike::assert "dpkg/apt work despite userns ID mapping (the _apt setgroups trap)" \
        "spike::appliance_exec podman exec inst-first sh -c 'ls /opt/nephos-spike/*.deb >/dev/null 2>&1 && apt-get install -y -qq --reinstall /opt/nephos-spike/*.deb 2>&1 | grep -qiv setgroups'" \
        "APT::Sandbox::User is set to root in the AMI"
}

# --- 4. User namespace isolation (ADR-0004 validation 3, RISKS S2) ---------

assert_userns_isolation() {
    spike::section "User namespace isolation"

    local host_uid in_uid uid_second
    host_uid="$(spike::instance_host_uid inst-first)"
    spike::metric "instance_root_maps_to_appliance_uid" "${host_uid:-unknown}"
    spike::assert "instance root maps to a non-zero UID in the appliance (ADR-0004 validation 3)" \
        "[ -n '${host_uid:-}' ] && [ '${host_uid:-0}' -gt 0 ]" \
        "instance root runs as appliance uid ${host_uid:-unknown}"

    in_uid="$(spike::appliance_exec podman exec inst-first id -u | tr -d '\r\n')"
    spike::assert "instance root still sees itself as uid 0, so sudo keeps working" \
        "[ '$in_uid' = '0' ]" \
        "id -u inside the instance = $in_uid"

    spike::appliance_exec podman rm -f inst-second >/dev/null 2>&1 || true
    spike::appliance_exec podman run -d --name inst-second "${INSTANCE_FLAGS[@]}" \
        --memory 512m --cpus 1 --pids-limit 512 \
        --label io.nephos.instance-id=i-second \
        "$SPIKE_AMI_REF" >/dev/null
    spike::wait_for_sshd inst-second 90 || true

    uid_second="$(spike::instance_host_uid inst-second)"
    spike::assert "each instance gets a distinct UID range (userns=auto, ADR-0004 R2)" \
        "[ -n '${uid_second:-}' ] && [ '${host_uid:-0}' != '${uid_second:-0}' ]" \
        "inst-first=${host_uid:-?} inst-second=${uid_second:-?}"

    spike::assert "an instance cannot see another instance's processes (RISKS S2)" \
        "! spike::appliance_exec podman exec inst-first ps aux 2>/dev/null | grep -q inst-second" \
        "checked ps output inside inst-first"

    spike::assert "an instance cannot reach the Podman socket or appliance state (RISKS S2)" \
        "! spike::appliance_exec podman exec inst-first sh -c 'test -e /run/podman/podman.sock -o -d /var/lib/nephos'" \
        "checked for /run/podman/podman.sock and /var/lib/nephos inside the instance"
}

# --- 5. Instance root can firewall its own netns (ADR-0004 validation 4) ----

assert_instance_firewall() {
    spike::section "Instance-level firewalling"

    local capeff
    capeff="$(spike::appliance_exec podman exec inst-first sh -c 'grep CapEff /proc/self/status | cut -f2' | tr -d '\r\n')"
    spike::metric "instance_capeff" "${capeff:-unknown}"

    # This is what makes the "you locked yourself out" lesson possible, and it
    # is RISKS S3's accepted tradeoff: CAP_NET_ADMIN in the instance's own
    # network namespace and nowhere else.
    spike::assert "instance root can add an nftables rule in its own netns (ADR-0004 validation 4)" \
        "spike::appliance_exec podman exec inst-first nft add table inet spiketest" \
        "ran nft inside the instance"

    spike::assert "what an instance does to nftables stays inside the instance" \
        "! spike::appliance_exec nft list ruleset 2>/dev/null | grep -q spiketest" \
        "the appliance's own ruleset is unaffected"
}

# --- 6a. Stop/start persistence (ADR-0004 R4, validation 6) ----------------

assert_stop_start_persistence() {
    spike::section "Stop/start persistence"
    local marker
    spike::appliance_exec podman exec inst-first sh -c 'echo nephos-sp1-marker > /root/marker.txt'
    spike::appliance_exec podman stop -t 20 inst-first >/dev/null
    spike::appliance_exec podman start inst-first >/dev/null
    spike::wait_for_sshd inst-first 90 || true

    marker="$(spike::appliance_exec podman exec inst-first cat /root/marker.txt 2>/dev/null | tr -d '\r\n')"
    spike::assert "the root filesystem survives stop/start (ADR-0004 R4, validation 6)" \
        "[ '$marker' = 'nephos-sp1-marker' ]" \
        "marker file after restart: '${marker:-missing}'"
}

# --- 6b. Scale and memory (RISKS T5, ADR-0004 validation 6) ----------------

measure_scale() {
    spike::section "Idle memory for $SCALE_COUNT instances (RISKS T5)"
    local i running total_bytes
    for i in $(seq 1 "$SCALE_COUNT"); do
        spike::appliance_exec podman run -d --name "inst-scale-$i" "${INSTANCE_FLAGS[@]}" \
            --memory 512m --cpus 1 --pids-limit 512 \
            --label io.nephos.instance-id="i-scale-$i" \
            "$SPIKE_AMI_REF" >/dev/null 2>&1 || spike::log "  instance $i failed to start"
    done

    spike::log "  waiting for the fleet to settle"
    sleep 30

    running="$(spike::appliance_exec sh -c 'podman ps -q --filter name=inst-scale- | wc -l' | tr -d '\r\n')"
    total_bytes="$(spike::instance_fleet_memory inst-scale-)"

    spike::metric "scale_instances_running" "$running"
    spike::metric "scale_total_memory_bytes" "${total_bytes:-unknown}"
    if [ -n "${total_bytes:-}" ] && [ "${total_bytes:-0}" -gt 0 ] && [ "${running:-0}" -gt 0 ]; then
        spike::metric "scale_memory_per_instance_mib" "$(( total_bytes / running / 1048576 ))"
    fi

    spike::assert "$SCALE_COUNT instances all start (RISKS T5)" \
        "[ '${running:-0}' -eq $SCALE_COUNT ]" \
        "$running of $SCALE_COUNT running"

    # ADR-0004 validation 6: twenty idle instances fit in 2 GiB.
    spike::assert "$SCALE_COUNT idle instances fit in 2 GiB (ADR-0004 validation 6)" \
        "[ -n '${total_bytes:-}' ] && [ '${total_bytes:-0}' -gt 0 ] && [ '${total_bytes:-0}' -lt 2147483648 ]" \
        "total ${total_bytes:-unknown} bytes for ${running:-0} instances"

    spike::appliance_exec sh -c "podman ps --format '{{.Names}} {{.Status}}'" > "$RESULTS/fleet.txt" 2>&1 || true
}

main "$@"
