#!/usr/bin/env bash
# M0 spike driver.
#
#   ./spikes/run.sh all | sp1 | sp2 | sp3 | sp4 | clean
#
# The spikes validate the riskiest assumptions in ADR-0003, ADR-0004, and
# ADR-0005 before M1 starts (ROADMAP M0). They are throwaway by design: their
# reports in docs/spikes/ are the deliverable, not this code.
#
# They need Docker and will start a privileged container. Nothing touches the
# host's network namespace, firewall, or filesystem outside the spike volume.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$HERE/common.sh"

usage() {
    sed -n '2,12p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
    exit "${1:-0}"
}

require_docker() {
    command -v docker >/dev/null 2>&1 || {
        echo "docker is not on PATH. See docs/DEVELOPMENT.md." >&2; exit 1; }
    docker info >/dev/null 2>&1 || {
        echo "cannot reach the Docker daemon. On WSL2, enable Docker Desktop's WSL integration." >&2; exit 1; }
}

run_one() {
    local name="$1" script="$HERE/$1"*/run.sh
    # shellcheck disable=SC2086
    set -- $script
    [ -f "$1" ] || { echo "no such spike: $name" >&2; return 2; }
    bash "$1"
}

main() {
    local target="${1:-all}"
    case "$target" in
        -h|--help|help) usage 0 ;;
        clean)
            echo "Removing the spike appliance, its volume, and its images."
            spike::appliance_purge
            docker rmi -f "$SPIKE_APPLIANCE_IMAGE" "$SPIKE_AMI_IMAGE" >/dev/null 2>&1 || true
            echo "Done."
            return 0
            ;;
    esac

    require_docker

    local failed=()
    case "$target" in
        all)
            for s in sp1 sp2 sp3 sp4; do
                run_one "$s" || failed+=( "$s" )
            done
            ;;
        sp1|sp2|sp3|sp4)
            run_one "$target" || failed+=( "$target" )
            ;;
        *)
            usage 2
            ;;
    esac

    # Whatever happened, do not leave a privileged container running.
    spike::appliance_down

    if [ ${#failed[@]} -gt 0 ]; then
        printf '\n\033[31mSpikes with failing assertions: %s\033[0m\n' "${failed[*]}"
        echo "A failing spike is a result. Record it in docs/spikes/ and, if it"
        echo "invalidates a decision, write the superseding ADR before starting M1."
        return 1
    fi
    printf '\n\033[32mAll requested spikes passed.\033[0m\n'
}

main "$@"
