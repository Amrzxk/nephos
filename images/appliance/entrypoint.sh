#!/bin/sh
# Appliance-only preflight. A failed check must prevent nephosd from starting.
set -eu

fail() { echo "nephos appliance: $*" >&2; exit 1; }

[ -f /sys/fs/cgroup/cgroup.controllers ] || fail "cgroup v2 is required"
[ -w /sys/fs/cgroup/cgroup.subtree_control ] || fail "private cgroup root is not writable"

# cgroup v2 cannot delegate controllers from a cgroup holding processes.
# Move the appliance's own processes into a leaf first, inside its private
# cgroup namespace. The M0 SP1 report validated this on both target hosts.
mkdir -p /sys/fs/cgroup/init
while read -r pid; do
    [ -n "$pid" ] && echo "$pid" > /sys/fs/cgroup/init/cgroup.procs 2>/dev/null || true
done < /sys/fs/cgroup/cgroup.procs
for controller in $(cat /sys/fs/cgroup/cgroup.controllers); do
    echo "+$controller" > /sys/fs/cgroup/cgroup.subtree_control 2>/dev/null || true
done
for controller in memory pids cpu; do
    grep -qw "$controller" /sys/fs/cgroup/cgroup.subtree_control || fail "$controller controller could not be delegated"
done
memory_max=$(cat /sys/fs/cgroup/memory.max)
case "$memory_max" in
    ''|max|*[!0-9]*) fail "the appliance requires a finite memory limit" ;;
esac
[ "$memory_max" -ge 3221225472 ] || fail "at least 3 GiB of appliance memory is required"

mkdir -p /var/lib/nephos/containers /var/lib/nephos/runroot /run/nephos
available_kb=$(df -Pk /var/lib/nephos | awk 'NR == 2 {print $4}')
[ -n "$available_kb" ] && [ "$available_kb" -ge 2097152 ] || fail "at least 2 GiB free in nephos-data is required"

# Functional probes run only inside the appliance's own namespace. No module
# is explicitly loaded and no host network or firewall object is changed.
nft list ruleset >/dev/null 2>&1 || fail "nftables is unavailable"
ip link add nx-pf-dummy type dummy || fail "dummy interfaces are unavailable"
ip link delete nx-pf-dummy || fail "could not remove dummy preflight link"
ip link add nx-pf-v0 type veth peer name nx-pf-v1 || fail "veth interfaces are unavailable"
ip link delete nx-pf-v0 || fail "could not remove veth preflight link"
ip netns add nx-preflight || fail "network namespaces are unavailable"
ip netns delete nx-preflight || fail "could not remove preflight namespace"

if ! podman image exists nephos-ubuntu:dev; then
    podman load -i /usr/local/share/nephos/ubuntu-24.04.oci.tar || fail "development AMI import failed"
fi
podman image exists nephos-ubuntu:dev || fail "development AMI has the wrong tag"

exec "$@"
