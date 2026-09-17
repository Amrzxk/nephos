#!/bin/sh
# SP1 spike appliance entrypoint.
#
# The real appliance (M1) starts nephosd here. The spike only has to prove the
# environment is sane before handing over, and to fail loudly if it is not —
# "fail closed" is a Nephos principle, not just a runtime behaviour (ADR-0003).
set -eu

fail() { echo "appliance: FATAL: $*" >&2; exit 1; }

# cgroup v2 is a hard prerequisite (ADR-0003, RISKS T1). WSL2 has historically
# shipped mixed v1/v2 hierarchies, so check rather than assume.
[ -f /sys/fs/cgroup/cgroup.controllers ] || fail "cgroup v2 is not available; Nephos needs a unified hierarchy"

# The appliance must never be sharing the host's network namespace (ADR-0003).
# In its own namespace it sees only loopback plus its Docker-provided uplink.
if [ "$(ip -o link show | wc -l)" -gt 10 ]; then
    echo "appliance: WARNING: unexpectedly many links; is this --network host?" >&2
fi

# Delegate the cgroup v2 controllers to nested containers.
#
# cgroup v2 forbids a cgroup from holding processes AND enabling controllers for
# its children ("no internal processes"). The appliance's own processes start at
# the root of its cgroup namespace, so Podman's children inherit a cgroup with
# no controllers, and instance memory/pids limits fail with
#   "controller `pids` is not available under .../cgroup.controllers".
#
# Move everything into a leaf and then delegate. This is the same thing kind and
# minikube do, and it stays entirely inside the appliance's own namespace: no
# host cgroup is touched (ADR-0003).
cgroup_delegate() {
    local root=/sys/fs/cgroup controller
    [ -w "$root/cgroup.subtree_control" ] || fail "cgroup root is not writable; is --privileged set?"

    mkdir -p "$root/init"
    while read -r pid; do
        [ -n "$pid" ] && echo "$pid" > "$root/init/cgroup.procs" 2>/dev/null || true
    done < "$root/cgroup.procs"

    for controller in $(cat "$root/cgroup.controllers"); do
        if ! echo "+$controller" > "$root/cgroup.subtree_control" 2>/dev/null; then
            echo "appliance: WARNING: could not delegate the '$controller' controller" >&2
        fi
    done

    # memory and pids are what instance types are actually enforced with
    # (ADR-0004 R5). Without them Nephos would be limiting nothing while
    # claiming to limit something, which principle 2 forbids.
    for controller in memory pids cpu; do
        grep -qw "$controller" "$root/cgroup.subtree_control" \
            || fail "the '$controller' cgroup controller could not be delegated; instance limits could not be enforced"
    done
    echo "appliance:   delegated  $(cat "$root/cgroup.subtree_control")"
}

cgroup_delegate

mkdir -p /var/lib/nephos/containers /var/lib/nephos/runroot /run/nephos

# nftables must work in here, or none of the VPC plane can be enforced.
nft list ruleset >/dev/null 2>&1 || fail "nftables is not usable inside the appliance"

echo "appliance: ready"
echo "appliance:   kernel     $(uname -r)"
echo "appliance:   podman     $(podman --version 2>/dev/null || echo missing)"
echo "appliance:   crun       $(crun --version 2>/dev/null | head -1 || echo missing)"
echo "appliance:   cgroup     v2"

exec "$@"
