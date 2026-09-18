#!/usr/bin/env bash
# SP3 — the routed VPC network plane.
#
# Validates ADR-0005 (a Nephos-owned, routed network plane) and ADR-0002's
# namespace rules. Mitigates RISKS T3 (network behaviour diverging from AWS)
# and T8 (Go threads stranded in the wrong namespace).
#
# This is the highest-risk spike in M0: the custom network code is the largest
# piece of engineering in Nephos, and if proxy ARP plus policy routing does not
# behave on a WSL2 kernel, ADR-0005 needs revisiting before M1 starts.
#
# The Go program does the work; this script builds it, puts it inside the
# appliance, and collects the results.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=../common.sh
source "$HERE/../common.sh"

RESULTS="$HERE/results"

main() {
    mkdir -p "$RESULTS"
    spike::section "SP3: routed VPC plane"
    spike::record_host "$RESULTS/host.txt"

    build_binary
    ensure_appliance
    run_in_appliance
    run_unit_tests

    spike::section "SP3 complete"
    spike::verdict_summary "$RESULTS/verdicts.tsv"
}

build_binary() {
    spike::section "Building the spike binary"
    command -v go >/dev/null 2>&1 || {
        echo "go is not on PATH; see docs/DEVELOPMENT.md" >&2; return 1; }

    # CGO_ENABLED=0 so the binary runs in the appliance regardless of what libc
    # it has, exactly as nephosd will (ADR-0002).
    ( cd "$HERE" && CGO_ENABLED=0 GOOS=linux go build -o "$RESULTS/sp3" . )
    spike::log "  built $(ls -la "$RESULTS/sp3" | awk '{print $5}') bytes"
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
}

run_in_appliance() {
    spike::section "Running the routed-plane checks inside the appliance"
    docker cp "$RESULTS/sp3" "$SPIKE_APPLIANCE_NAME:/usr/local/bin/sp3"

    local rc=0
    spike::appliance_exec /usr/local/bin/sp3 || rc=$?

    # Copy the machine-readable verdicts out, whatever the outcome: a failing
    # spike is a result worth keeping (ROADMAP M0).
    docker cp "$SPIKE_APPLIANCE_NAME:/tmp/sp3-verdicts.tsv" "$RESULTS/sp3-verdicts.tsv" 2>/dev/null || true

    if [ -f "$RESULTS/sp3-verdicts.tsv" ]; then
        local passed failed
        passed="$(grep -c '^PASS' "$RESULTS/sp3-verdicts.tsv" || true)"
        failed="$(grep -c '^FAIL' "$RESULTS/sp3-verdicts.tsv" || true)"
        SPIKE_PASS_COUNT=$(( SPIKE_PASS_COUNT + passed ))
        SPIKE_FAIL_COUNT=$(( SPIKE_FAIL_COUNT + failed ))
        while IFS=$'\t' read -r kind desc evidence; do
            SPIKE_VERDICTS+=( "$kind	$desc	$evidence" )
        done < "$RESULTS/sp3-verdicts.tsv"
    fi

    spike::assert "the SP3 binary ran to completion inside the appliance" \
        "[ $rc -eq 0 ] || [ $rc -eq 1 ]" \
        "exit code $rc (0 = all passed, 1 = some assertions failed)"
}

# The renderer is pure, so it is testable without a kernel at all. This is the
# golden-test discipline internal/network/firewall will inherit: desired state
# in, exact text out (ARCHITECTURE section 11).
run_unit_tests() {
    spike::section "Renderer unit and golden tests"
    local rc=0
    ( cd "$HERE" && go test ./... 2>&1 | tail -20 ) || rc=$?
    spike::assert "the pure renderer's tests pass" \
        "[ $rc -eq 0 ]" \
        "go test in $HERE"

    # The race detector needs cgo; this is a test run, not a shipped artifact.
    rc=0
    ( cd "$HERE" && CGO_ENABLED=1 go test -race -run 'TestNamespace|TestRender' ./... 2>&1 | tail -10 ) || rc=$?
    spike::assert "the renderer and namespace helpers are race-clean (RISKS T8)" \
        "[ $rc -eq 0 ]" \
        "go test -race"
}

main "$@"
