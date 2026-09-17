#!/usr/bin/env bash
# SP4 — cloud-init against a Nephos-served metadata service.
#
# Mitigates RISKS T2 (systemd and cloud-init quirks inside containers).
#
# The question: will STOCK cloud-init, unmodified, accept a metadata service
# Nephos serves at 169.254.169.254 inside a VPC namespace? If it will not, user
# data and SSH key injection have to be built some other way and M2 changes
# shape entirely.
#
#   1. cloud-init reaches the Ec2 datasource and reports done.
#   2. User data runs on first boot.
#   3. The SSH public key is installed for the default user.
#   4. The IMDSv2 token flow works, and with http_tokens=required an IMDSv1
#      request gets 401. (ARCHITECTURE §5.1 teaches IMDSv2.)
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=../common.sh
source "$HERE/../common.sh"

RESULTS="$HERE/results"
VPC_NS="nx-vpc-sp4"
ROUTER_IF="nxr0"
GATEWAY="10.60.1.1"
INSTANCE_IP="10.60.1.4"
IFACE="vesp4a"

main() {
    mkdir -p "$RESULTS"
    spike::section "SP4: cloud-init against a Nephos IMDS"
    spike::record_host "$RESULTS/host.txt"

    build_imds
    ensure_appliance
    prepare_vpc_and_imds

    check_metadata_endpoints
    check_imdsv2_token_flow
    boot_instance_with_user_data
    check_cloud_init_result

    spike::section "SP4 complete"
    spike::verdict_summary "$RESULTS/verdicts.tsv"
}

build_imds() {
    spike::section "Building the metadata service"
    command -v go >/dev/null 2>&1 || { echo "go is not on PATH" >&2; return 1; }
    ( cd "$HERE" && CGO_ENABLED=0 GOOS=linux go build -o "$RESULTS/imds" ./imds )
    spike::log "  built imds"
}

ensure_appliance() {
    spike::section "Ensuring the appliance and the hook are available"
    if ! docker ps -q --filter "name=^${SPIKE_APPLIANCE_NAME}$" | grep -q .; then
        spike::run_retry 3 docker build -q -t "$SPIKE_APPLIANCE_IMAGE" \
            -f "$HERE/../sp1-appliance/Dockerfile.appliance" "$HERE/../sp1-appliance"
        spike::appliance_up
    fi
    spike::cleanup_instances
    spike::appliance_exec podman image exists "$SPIKE_AMI_REF" 2>/dev/null || spike::load_ami

    # SP4 reuses SP2's hook and plumbing daemon: an instance has to have a
    # network before cloud-init can fetch anything.
    spike::appliance_exec test -x /usr/local/bin/plumbd 2>/dev/null || {
        ( cd "$HERE/../sp2-eni-hook" && CGO_ENABLED=0 GOOS=linux go build -o "$RESULTS/plumbd" ./plumbd )
        ( cd "$HERE/../sp2-eni-hook" && CGO_ENABLED=0 GOOS=linux go build -o "$RESULTS/nephos-hook" ./hook )
        docker cp "$RESULTS/plumbd" "$SPIKE_APPLIANCE_NAME:/usr/local/bin/plumbd"
        docker cp "$RESULTS/nephos-hook" "$SPIKE_APPLIANCE_NAME:/usr/local/bin/nephos-hook"
        spike::appliance_exec sh -c 'mkdir -p /usr/share/containers/oci/hooks.d && cat > /usr/share/containers/oci/hooks.d/nephos.json <<JSON
{
  "version": "1.0.0",
  "hook": { "path": "/usr/local/bin/nephos-hook" },
  "when": { "annotations": { "io.nephos.instance-id": ".*" } },
  "stages": [ "createRuntime" ]
}
JSON'
    }
}

prepare_vpc_and_imds() {
    spike::section "Preparing the VPC namespace and metadata service"

    # An SSH key pair whose public half IMDS serves, exactly as a Nephos key
    # pair would (ARCHITECTURE §5.1).
    rm -f "$RESULTS/sp4-key" "$RESULTS/sp4-key.pub"
    ssh-keygen -t ed25519 -N '' -C nephos-sp4 -f "$RESULTS/sp4-key" >/dev/null 2>&1

    cat > "$RESULTS/user-data.sh" <<'USERDATA'
#!/bin/bash
# SP4 user data: the analogue of a learner's --user-data file://install.sh
echo "nephos-sp4-user-data-ran" > /var/log/nephos-sp4-marker
hostname > /var/log/nephos-sp4-hostname
USERDATA

    spike::appliance_exec sh -c "
        mkdir -p /run/netns /opt/sp4
        ip netns delete $VPC_NS 2>/dev/null
        ip netns add $VPC_NS
        ip netns exec $VPC_NS sysctl -qw net.ipv4.ip_forward=1
        ip netns exec $VPC_NS ip link add $ROUTER_IF type dummy
        ip netns exec $VPC_NS ip link set $ROUTER_IF up
        ip netns exec $VPC_NS ip addr add $GATEWAY/32 dev $ROUTER_IF
        ip netns exec $VPC_NS ip addr add 169.254.169.254/32 dev $ROUTER_IF
        ip netns exec $VPC_NS ip link set lo up
        echo prepared
    " >/dev/null 2>&1 || true

    docker cp "$RESULTS/imds" "$SPIKE_APPLIANCE_NAME:/opt/sp4/imds"
    docker cp "$RESULTS/user-data.sh" "$SPIKE_APPLIANCE_NAME:/opt/sp4/user-data.sh"
    docker cp "$RESULTS/sp4-key.pub" "$SPIKE_APPLIANCE_NAME:/opt/sp4/key.pub"

    start_imds ""
    spike::assert "the VPC namespace holds the metadata address 169.254.169.254" \
        "spike::appliance_exec ip netns exec $VPC_NS ip -o addr show $ROUTER_IF | grep -q 169.254.169.254" \
        "169.254.169.254 on $ROUTER_IF in $VPC_NS"
}

# start_imds <extra-flags>
start_imds() {
    local extra="${1:-}"
    spike::appliance_exec sh -c 'pkill -x imds 2>/dev/null; exit 0' || true
    sleep 1
    docker exec -d "$SPIKE_APPLIANCE_NAME" sh -c "
        ip netns exec $VPC_NS /opt/sp4/imds \
            --user-data /opt/sp4/user-data.sh \
            --public-key /opt/sp4/key.pub \
            --private-ip $INSTANCE_IP $extra >/tmp/imds.log 2>&1"
    sleep 2
}

imds_curl() {
    spike::appliance_exec ip netns exec "$VPC_NS" curl -s --max-time 5 "$@" 2>&1 | tr -d '\r'
}

# --- 1. The metadata endpoints cloud-init walks ---------------------------

check_metadata_endpoints() {
    spike::section "Metadata endpoints"
    local iid az doc

    iid="$(imds_curl http://169.254.169.254/latest/meta-data/instance-id)"
    spike::assert "instance-id is served" \
        "echo '$iid' | grep -q '^i-'" \
        "instance-id: ${iid:-empty}"

    az="$(imds_curl http://169.254.169.254/latest/meta-data/placement/availability-zone)"
    spike::assert "the availability zone uses Nephos's own region naming" \
        "[ '$az' = 'local-1a' ]" \
        "availability-zone: ${az:-empty} (AWS names would imply affiliation; ADR-0006 and RISKS P4)"

    doc="$(imds_curl http://169.254.169.254/latest/dynamic/instance-identity/document)"
    spike::assert "the instance identity document parses as JSON" \
        "echo '$doc' | grep -q 'instanceId'" \
        "the Ec2 datasource probes this before it will accept the service"

    local key
    key="$(imds_curl http://169.254.169.254/latest/meta-data/public-keys/0/openssh-key)"
    spike::assert "the instance's SSH public key is served" \
        "echo '$key' | grep -q 'ssh-ed25519'" \
        "public-keys/0/openssh-key"

    local ud
    ud="$(imds_curl http://169.254.169.254/latest/user-data)"
    spike::assert "user data is served" \
        "echo '$ud' | grep -q 'nephos-sp4-user-data-ran'" \
        "/latest/user-data"
}

# --- 2. IMDSv2 (ARCHITECTURE §5.1) ----------------------------------------

check_imdsv2_token_flow() {
    spike::section "IMDSv2 token flow"
    local token v1 v2

    token="$(imds_curl -X PUT -H 'X-aws-ec2-metadata-token-ttl-seconds: 21600' \
        http://169.254.169.254/latest/api/token)"
    spike::assert "a PUT to /latest/api/token returns a session token" \
        "[ -n '$token' ]" \
        "token length ${#token}"

    v2="$(imds_curl -H "X-aws-ec2-metadata-token: $token" \
        http://169.254.169.254/latest/meta-data/instance-id)"
    spike::assert "a token-authenticated request succeeds (IMDSv2)" \
        "echo '$v2' | grep -q '^i-'" \
        "instance-id via token: ${v2:-empty}"

    # http_tokens=required is the setting the IMDSv2 lesson turns on.
    start_imds "--require-token"

    local code
    code="$(spike::appliance_exec ip netns exec "$VPC_NS" \
        curl -s -o /dev/null -w '%{http_code}' --max-time 5 \
        http://169.254.169.254/latest/meta-data/instance-id 2>&1 | tr -d '\r')"
    spike::assert "with http_tokens=required, an IMDSv1 request gets 401" \
        "[ '$code' = '401' ]" \
        "HTTP $code for a tokenless request"

    token="$(imds_curl -X PUT -H 'X-aws-ec2-metadata-token-ttl-seconds: 600' \
        http://169.254.169.254/latest/api/token)"
    v1="$(imds_curl -H "X-aws-ec2-metadata-token: $token" \
        http://169.254.169.254/latest/meta-data/instance-id)"
    spike::assert "with http_tokens=required, a token request still succeeds" \
        "echo '$v1' | grep -q '^i-'" \
        "instance-id: ${v1:-empty}"

    # Back to optional for the cloud-init boot: stock cloud-init negotiates
    # IMDSv2 itself, and this proves it is not silently falling back.
    start_imds ""
}

# --- 3 and 4. A real instance boots and runs its user data ----------------

boot_instance_with_user_data() {
    spike::section "Booting an instance against the metadata service"

    spike::appliance_exec /usr/local/bin/plumbd register "{
        \"instance_id\": \"i-sp4\",
        \"spec\": {
            \"Iface\": \"$IFACE\",
            \"PrivateIP\": \"$INSTANCE_IP\",
            \"PrefixLen\": 24,
            \"Gateway\": \"$GATEWAY\",
            \"MTU\": 1500,
            \"RouterNS\": \"$VPC_NS\"
        }
    }" >/dev/null 2>&1 || true

    spike::appliance_exec sh -c 'pkill -x plumbd 2>/dev/null; exit 0' || true
    docker exec -d "$SPIKE_APPLIANCE_NAME" sh -c '/usr/local/bin/plumbd >>/tmp/plumbd.log 2>&1'
    sleep 2

    spike::appliance_exec podman rm -f sp4-instance >/dev/null 2>&1 || true
    spike::appliance_exec podman run -d --name sp4-instance \
        --systemd=always --userns=auto --network none --cap-add NET_ADMIN \
        --memory 1g --pids-limit 512 \
        --annotation io.nephos.instance-id=i-sp4 \
        "$SPIKE_AMI_REF" >/dev/null 2>&1 || spike::log "  instance failed to start"

    spike::assert "the instance started and reached the metadata service" \
        "spike::appliance_exec podman exec sp4-instance ip -o addr show eth0 | grep -q $INSTANCE_IP" \
        "eth0 holds $INSTANCE_IP"

    spike::log "  waiting for cloud-init (up to 120 s)"
    local i
    for i in $(seq 1 60); do
        if spike::appliance_exec podman exec sp4-instance \
                sh -c 'cloud-init status 2>/dev/null | grep -qE "done|error"' >/dev/null 2>&1; then
            break
        fi
        sleep 2
    done
}

check_cloud_init_result() {
    spike::section "cloud-init results"
    local status marker key_installed

    status="$(spike::appliance_exec podman exec sp4-instance cloud-init status 2>&1 | tr -d '\r\n')"
    spike::metric "cloud_init_status" "${status:-unknown}"
    spike::assert "cloud-init completed without error (RISKS T2)" \
        "echo '$status' | grep -q 'done'" \
        "cloud-init status: ${status:-unknown}"

    local ds
    ds="$(spike::appliance_exec podman exec sp4-instance \
        sh -c 'grep -o "DataSourceEc2[A-Za-z]*" /run/cloud-init/cloud-init-generator.log /var/log/cloud-init.log 2>/dev/null | head -1' | tr -d '\r\n')"
    spike::metric "cloud_init_datasource" "${ds:-unknown}"
    spike::assert "cloud-init selected the Ec2 datasource" \
        "echo '${ds:-}' | grep -q 'DataSourceEc2'" \
        "datasource: ${ds:-not detected}"

    marker="$(spike::appliance_exec podman exec sp4-instance \
        cat /var/log/nephos-sp4-marker 2>/dev/null | tr -d '\r\n')"
    spike::assert "user data ran on first boot" \
        "[ '$marker' = 'nephos-sp4-user-data-ran' ]" \
        "marker: ${marker:-missing}"

    key_installed="$(spike::appliance_exec podman exec sp4-instance \
        sh -c 'cat /home/ubuntu/.ssh/authorized_keys 2>/dev/null' | tr -d '\r\n')"
    spike::assert "the SSH public key was installed for the default user" \
        "echo '$key_installed' | grep -q 'ssh-ed25519'" \
        "authorized_keys for ubuntu: ${key_installed:0:48}..."

    local hostname
    hostname="$(spike::appliance_exec podman exec sp4-instance hostname 2>/dev/null | tr -d '\r\n')"
    spike::metric "instance_hostname" "${hostname:-unknown}"

    spike::appliance_exec podman exec sp4-instance \
        sh -c 'tail -60 /var/log/cloud-init.log' > "$RESULTS/cloud-init.log" 2>&1 || true
    spike::appliance_exec sh -c 'cat /tmp/imds.log' > "$RESULTS/imds.log" 2>&1 || true
}

main "$@"
