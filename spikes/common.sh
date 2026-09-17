#!/usr/bin/env bash
# Shared helpers for the M0 spikes.
#
# A spike's job is to produce a verdict with evidence, so the reporting here is
# deliberately blunt: every assertion prints PASS or FAIL with the measurement
# beside it, and a failing spike is a result worth having, not an error to
# paper over (ROADMAP M0).

SPIKE_APPLIANCE_IMAGE="${SPIKE_APPLIANCE_IMAGE:-nephos-spike-appliance:latest}"
SPIKE_AMI_IMAGE="${SPIKE_AMI_IMAGE:-nephos-spike-ami:ubuntu-24.04}"
# `docker save | podman load` lands the image under docker.io/library/..., and
# Podman refuses to resolve a bare short name. Instances are always started by
# this fully-qualified local reference.
SPIKE_AMI_REF="${SPIKE_AMI_REF:-localhost/nephos-ami:ubuntu-24.04}"
SPIKE_APPLIANCE_NAME="${SPIKE_APPLIANCE_NAME:-nephos-spike}"
SPIKE_VOLUME="${SPIKE_VOLUME:-nephos-spike-data}"

SPIKE_PASS_COUNT=0
SPIKE_FAIL_COUNT=0
SPIKE_VERDICTS=()

# --- output ---------------------------------------------------------------

spike::log() {
    printf '%s\n' "$*" | tee -a "${RESULTS:-.}/$(basename "${BASH_SOURCE[1]:-spike}" .sh).log" 2>/dev/null \
        || printf '%s\n' "$*"
}

spike::section() {
    printf '\n\033[1m=== %s ===\033[0m\n' "$*"
}

spike::metric() {
    printf '  \033[36mmetric\033[0m  %-44s %s\n' "$1" "$2"
    SPIKE_VERDICTS+=( "METRIC	$1	$2" )
}

# spike::assert <description> <command> [evidence]
#
# The command runs under `eval` so that callers can pass pipelines and
# negations. A non-zero exit is a FAIL, never a script abort: the point of a
# spike is to finish and report everything it learned.
spike::assert() {
    local desc="$1" cmd="$2" evidence="${3:-}"
    local out rc
    set +e
    out="$(eval "$cmd" 2>&1)"
    rc=$?
    set -e
    if [ $rc -eq 0 ]; then
        printf '  \033[32mPASS\033[0m    %s\n' "$desc"
        [ -n "$evidence" ] && printf '          %s\n' "$evidence"
        SPIKE_PASS_COUNT=$(( SPIKE_PASS_COUNT + 1 ))
        SPIKE_VERDICTS+=( "PASS	$desc	$evidence" )
    else
        printf '  \033[31mFAIL\033[0m    %s\n' "$desc"
        [ -n "$evidence" ] && printf '          %s\n' "$evidence"
        [ -n "$out" ] && printf '          output: %s\n' "$(printf '%s' "$out" | head -3 | tr '\n' ' ')"
        SPIKE_FAIL_COUNT=$(( SPIKE_FAIL_COUNT + 1 ))
        SPIKE_VERDICTS+=( "FAIL	$desc	$evidence" )
    fi
    return 0
}

spike::verdict_summary() {
    local out="${1:-}"
    printf '\n  \033[32m%d passed\033[0m, \033[31m%d failed\033[0m\n' \
        "$SPIKE_PASS_COUNT" "$SPIKE_FAIL_COUNT"
    if [ -n "$out" ]; then
        mkdir -p "$(dirname "$out")"
        printf '%s\n' "${SPIKE_VERDICTS[@]}" > "$out"
        printf '  verdicts written to %s\n' "$out"
    fi
    [ "$SPIKE_FAIL_COUNT" -eq 0 ]
}

spike::run() {
    printf '  \033[90m$ %s\033[0m\n' "$*"
    "$@"
}

# spike::run_retry <attempts> <command...>
#
# Docker Desktop's credential helper reaches Windows over vsock and
# intermittently fails there with "error getting credentials - exit status 1",
# usually alongside a WSL "UtilAcceptVsock: accept4 failed" message. It is an
# interop hiccup, not a build error, and it succeeds on the next attempt. Worth
# noting as evidence for RISKS T1: this platform is genuinely flaky.
spike::run_retry() {
    local attempts="$1"; shift
    local i
    for i in $(seq 1 "$attempts"); do
        printf '  \033[90m$ %s\033[0m\n' "$*"
        if "$@"; then
            return 0
        fi
        if [ "$i" -lt "$attempts" ]; then
            printf '  \033[33mretrying (%d/%d)\033[0m\n' "$i" "$attempts"
            sleep 3
        fi
    done
    return 1
}

# --- measurement ----------------------------------------------------------

spike::now_ms() { date +%s%3N; }

spike::median() {
    [ $# -eq 0 ] && { printf ''; return; }
    printf '%s\n' "$@" | sort -n | awk '{a[NR]=$1} END {
        if (NR % 2) print a[(NR+1)/2];
        else print int((a[NR/2] + a[NR/2+1]) / 2);
    }'
}

spike::record_host() {
    local out="$1"
    mkdir -p "$(dirname "$out")"
    {
        echo "date:     $(date -u +%Y-%m-%dT%H:%M:%SZ)"
        echo "kernel:   $(uname -r)"
        echo "arch:     $(uname -m)"
        echo "cpus:     $(nproc)"
        echo "memory:   $(free -h 2>/dev/null | awk '/^Mem:/{print $2}' || echo unknown)"
        echo "docker:   $(docker version --format '{{.Server.Version}}' 2>/dev/null || echo unknown)"
        echo "cgroup:   $(docker info --format '{{.CgroupVersion}}' 2>/dev/null || echo unknown)"
        echo "storage:  $(docker info --format '{{.Driver}}' 2>/dev/null || echo unknown)"
        if grep -qi microsoft /proc/version 2>/dev/null; then
            echo "platform: Windows WSL2 + Docker Desktop"
        else
            echo "platform: native Linux"
        fi
    } | tee "$out"
}

# --- appliance lifecycle --------------------------------------------------

spike::appliance_up() {
    spike::appliance_down
    docker volume create "$SPIKE_VOLUME" >/dev/null
    # ADR-0003: privileged, own network and PID namespaces, private cgroup
    # namespace, a named volume, no host bind mounts, resource limits.
    spike::run docker run -d --name "$SPIKE_APPLIANCE_NAME" \
        --privileged \
        --cgroupns private \
        --memory 4g --cpus 2 --pids-limit 4096 \
        -v "$SPIKE_VOLUME:/var/lib/nephos" \
        "$SPIKE_APPLIANCE_IMAGE" sleep infinity >/dev/null

    local i
    for i in $(seq 1 30); do
        if docker logs "$SPIKE_APPLIANCE_NAME" 2>&1 | grep -q 'appliance: ready'; then
            docker logs "$SPIKE_APPLIANCE_NAME" 2>&1 | sed 's/^/  /'
            return 0
        fi
        if ! docker ps -q --filter "name=^${SPIKE_APPLIANCE_NAME}$" | grep -q .; then
            echo "  appliance exited during startup:" >&2
            docker logs "$SPIKE_APPLIANCE_NAME" 2>&1 | sed 's/^/  /' >&2
            return 1
        fi
        sleep 1
    done
    echo "  appliance did not report ready within 30 s" >&2
    docker logs "$SPIKE_APPLIANCE_NAME" 2>&1 | tail -20 | sed 's/^/  /' >&2
    return 1
}

spike::appliance_down() {
    docker rm -f "$SPIKE_APPLIANCE_NAME" >/dev/null 2>&1 || true
}

spike::appliance_purge() {
    spike::appliance_down
    docker volume rm -f "$SPIKE_VOLUME" >/dev/null 2>&1 || true
}

spike::appliance_exec() {
    docker exec "$SPIKE_APPLIANCE_NAME" "$@"
}

spike::appliance_exec_stdin() {
    docker exec -i "$SPIKE_APPLIANCE_NAME" "$@"
}

# spike::wait_for_sshd <container> <timeout-seconds>
#
# SP1 has no ENI plumbing yet (that is SP2), so reachability is checked from
# inside the instance rather than over the network. "running" is not "ready":
# AWS behaves the same way, and ADR-0004 measures time to sshd, not to start.
#
# It waits for an actual SSH protocol banner, NOT merely for something to be
# listening on port 22. systemd socket-activates ssh.socket, which binds :22
# immediately and hands over to ssh.service later — so a port check reports
# "ready" in milliseconds even when sshd subsequently fails to start. That is
# precisely the kind of "looks fine, is not" result Nephos must never produce,
# and here it would have silently invalidated the boot-time measurement.
spike::wait_for_sshd() {
    local name="$1" timeout="${2:-60}" deadline
    deadline=$(( $(date +%s) + timeout ))
    while [ "$(date +%s)" -lt "$deadline" ]; do
        # bash, not sh: /dev/tcp is a bash builtin and Ubuntu's /bin/sh is dash,
        # where the redirect silently fails and every instance looks unreachable.
        if spike::appliance_exec podman exec "$name" \
                bash -c 'exec 3<>/dev/tcp/127.0.0.1/22 && head -c 4 <&3 | grep -q "^SSH-"' \
                >/dev/null 2>&1; then
            return 0
        fi
        sleep 0.2
    done
    return 1
}

# spike::load_ami — copy the AMI from the host's Docker into the appliance's
# Podman. No registry: the spike is self-contained, and this mirrors how the
# real appliance caches an AMI on its volume.
spike::load_ami() {
    docker save "$SPIKE_AMI_IMAGE" | spike::appliance_exec_stdin podman load >/dev/null
    spike::appliance_exec podman tag "docker.io/library/$SPIKE_AMI_IMAGE" "$SPIKE_AMI_REF"
    spike::appliance_exec podman image exists "$SPIKE_AMI_REF"
}

# spike::instance_host_uid <container>
#
# The UID an instance's root maps to in the appliance. `podman top huser` prints
# one row per process and renders unmapped entries as "?", which silently defeats
# a "not zero" assertion — reading the container's PID and its /proc status is
# unambiguous. A non-zero answer is what RISKS S2 depends on.
spike::instance_host_uid() {
    local name="$1" pid
    pid="$(spike::appliance_exec podman inspect --format '{{.State.Pid}}' "$name" 2>/dev/null | tr -d '\r\n')"
    [ -n "$pid" ] && [ "$pid" != "0" ] || return 1
    spike::appliance_exec sh -c "awk '/^Uid:/ {print \$2; exit}' /proc/$pid/status" 2>/dev/null | tr -d '\r\n'
}

# spike::instance_fleet_memory <name-prefix>
#
# Total bytes charged to the matching instances' cgroups. Reading
# memory.current directly is exact and avoids parsing podman's human-readable
# units — and avoids `bc`, which the appliance image does not ship.
spike::instance_fleet_memory() {
    local prefix="$1"
    spike::appliance_exec sh -c "
        total=0
        for id in \$(podman ps -q --filter name=$prefix); do
            pid=\$(podman inspect --format '{{.State.Pid}}' \"\$id\" 2>/dev/null) || continue
            [ -n \"\$pid\" ] && [ \"\$pid\" != '0' ] || continue
            cg=\$(sed -n 's|^0::||p' /proc/\$pid/cgroup)
            # PID 1 of the container lives in the init.scope LEAF, which
            # accounts for systemd alone (about 1 MiB) rather than the whole
            # instance. The container's own cgroup is its parent.
            cg=\${cg%/init.scope}
            f=/sys/fs/cgroup\${cg}/memory.current
            [ -r \"\$f\" ] && total=\$(( total + \$(cat \"\$f\") ))
        done
        echo \$total" 2>/dev/null | tr -d '\r\n'
}

spike::cleanup_instances() {
    spike::appliance_exec sh -c 'podman rm -f $(podman ps -aq) >/dev/null 2>&1' || true
}
