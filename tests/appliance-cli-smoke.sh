#!/usr/bin/env bash
# The first-slice contributor path through the real CLI and Docker Engine API.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

fail() { echo "appliance-cli-smoke: $*" >&2; exit 1; }

if docker container inspect nephos >/dev/null 2>&1; then
    fail "container 'nephos' already exists; refusing to replace it"
fi
if docker volume inspect nephos-data >/dev/null 2>&1; then
    fail "volume 'nephos-data' already exists; refusing to replace it"
fi
test -f dist/ubuntu-24.04.oci.tar || fail "run make dev-ami first"
docker image inspect nephos-appliance:dev >/dev/null || fail "run make appliance first"
test -x bin/nephos || fail "run make build first"

# The contributor path must explain both preserving and discarding volume state.
grep -Fq 'docker rm nephos' docs/DEVELOPMENT.md || fail "appliance refresh instructions missing"
grep -Fq 'docker volume rm nephos-data' docs/DEVELOPMENT.md || fail "AMI refresh instructions missing"

test_home="$(mktemp -d /tmp/nephos-cli-smoke.XXXXXX)"
[[ "$test_home" == /tmp/nephos-cli-smoke.* ]] || fail "unexpected temporary home path"
test_started=false
cleanup() {
    result=$?
    if [ "$test_started" = true ]; then
        if [ "$(docker inspect -f '{{index .Config.Labels "io.nephos.appliance"}}' nephos 2>/dev/null || true)" = true ]; then
            if [ "$result" -ne 0 ]; then docker logs nephos >&2 || true; fi
            docker rm -f nephos >/dev/null || true
        fi
        if [ "$(docker volume inspect -f '{{index .Labels "io.nephos.appliance"}}' nephos-data 2>/dev/null || true)" = true ]; then
            docker volume rm nephos-data >/dev/null || true
        fi
    fi
    rm -r -- "$test_home"
    exit "$result"
}
trap cleanup EXIT

test_started=true
HOME="$test_home" bin/nephos up
[ "$(HOME="$test_home" bin/nephos status)" = ready ] || fail "CLI did not report ready"
credentials="$test_home/.nephos/credentials"
[ "$(stat -c '%a' "$credentials")" = 600 ] || fail "CLI credential is not private"
[ "$(curl -sS -o /dev/null -w '%{http_code}' http://127.0.0.1:7788/v1/health)" = 200 ] || fail "health is not ready"
[ "$(curl -sS -o /dev/null -w '%{http_code}' http://127.0.0.1:7788/v1/version)" = 401 ] || fail "version accepted an anonymous request"
awk '{printf "header = \"Authorization: Bearer %s\"\n", $0}' "$credentials" \
    | curl -fsS --config - http://127.0.0.1:7788/v1/version >/dev/null \
    || fail "copied credential cannot authenticate"

first_id="$(docker inspect -f '{{.Id}}' nephos)"
first_token_hash="$(sha256sum "$credentials" | cut -d' ' -f1)"
HOME="$test_home" bin/nephos down
[ "$(HOME="$test_home" bin/nephos status)" = stopped ] || fail "CLI did not report stopped"
docker volume inspect nephos-data >/dev/null || fail "down removed the data volume"
HOME="$test_home" bin/nephos up
[ "$(HOME="$test_home" bin/nephos status)" = ready ] || fail "CLI did not report ready after restart"
[ "$(docker inspect -f '{{.Id}}' nephos)" = "$first_id" ] || fail "up created a duplicate container"
[ "$(sha256sum "$credentials" | cut -d' ' -f1)" = "$first_token_hash" ] || fail "restart replaced the token"

if HOME="$test_home" bin/nephos up --memory=3g >/dev/null 2>&1; then
    fail "explicit resource change was silently ignored"
fi
[ "$(docker inspect -f '{{.Id}}' nephos)" = "$first_id" ] || fail "limit rejection replaced the container"

# The documented appliance-only refresh recreates the container, not its data.
HOME="$test_home" bin/nephos down
docker rm "$first_id" >/dev/null
HOME="$test_home" bin/nephos up --memory=3g --cpus=1 --pids-limit=512
second_id="$(docker inspect -f '{{.Id}}' nephos)"
[ "$second_id" != "$first_id" ] || fail "manual appliance refresh reused old container"
[ "$(sha256sum "$credentials" | cut -d' ' -f1)" = "$first_token_hash" ] || fail "container refresh replaced the token"
[ "$(docker inspect -f '{{.HostConfig.Memory}}' nephos)" = 3221225472 ] || fail "custom memory limit not applied"
[ "$(docker inspect -f '{{.HostConfig.NanoCpus}}' nephos)" = 1000000000 ] || fail "custom CPU limit not applied"
[ "$(docker inspect -f '{{.HostConfig.PidsLimit}}' nephos)" = 512 ] || fail "custom PID limit not applied"
HOME="$test_home" bin/nephos down
HOME="$test_home" bin/nephos up
[ "$(docker inspect -f '{{.Id}}' nephos)" = "$second_id" ] || fail "default restart recreated custom-limit container"

# The documented AMI refresh discards the test-owned data volume and token.
HOME="$test_home" bin/nephos down
docker rm "$second_id" >/dev/null
docker volume rm nephos-data >/dev/null
HOME="$test_home" bin/nephos up
[ "$(sha256sum "$credentials" | cut -d' ' -f1)" != "$first_token_hash" ] || fail "fresh volume retained old token"

echo "appliance-cli-smoke: auth, limit handling, restart, and image refresh paths passed"
