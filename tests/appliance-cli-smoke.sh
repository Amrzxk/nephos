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

echo "appliance-cli-smoke: CLI bootstrap, auth, and restart reuse passed"
