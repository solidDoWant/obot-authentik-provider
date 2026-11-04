#!/bin/bash
# End-to-end test: upstream obot with this provider mounted as an image volume, signing users in
# through a real authentik. Set PROVIDER_IMAGE to test a published image instead of building one,
# and KEEP=1 to leave the environment running.
set -euo pipefail

cd "$(dirname "$0")"
export E2E_DIR=${E2E_DIR:-$(mktemp -d)}
export PROVIDER_IMAGE=${PROVIDER_IMAGE:-obot-authentik-provider:e2e}
OBOT=http://localhost:8080
# renovate: datasource=docker depName=ghcr.io/obot-platform/obot
OBOT_IMAGE=ghcr.io/obot-platform/obot:v0.27.0@sha256:06b6b506631c2bf20f7ed795ffb12d4f448319754cd1893a58315dc145262da0
OBOT_CONTAINER=obot-authentik-e2e-obot
AK=http://127.0.0.1:9000/api/v3
BOOTSTRAP="Authorization: Bearer e2e-obot-bootstrap"
AK_ADMIN="Authorization: Bearer e2e-bootstrap-token"
failures=0

compose() { docker compose -f compose.yaml "$@"; }
pass() { echo "PASS: $1"; }
fail() { echo "FAIL: $1" >&2; failures=$((failures + 1)); }
check() {
    local name=$1
    shift
    if "$@"; then pass "$name"; else fail "$name"; fi
}
wait_for() {
    local name=$1 url=$2
    shift 2
    for _ in $(seq 1 90); do
        curl -sf -o /dev/null "$@" "$url" && return
        sleep 2
    done
    echo "$name did not become ready" >&2
    exit 1
}
ak() {
    local method=$1 path=$2
    shift 2
    curl -sSf -X "$method" -H "$AK_ADMIN" -H "Content-Type: application/json" "$AK$path" "$@"
}

cleanup() {
    local status=$?
    if [ "$status" -ne 0 ] || [ "$failures" -ne 0 ]; then
        docker logs "$OBOT_CONTAINER" > "$E2E_DIR/obot.log" 2>&1 || true
        echo "--- provider log lines from obot:" >&2
        grep -i "authentik" "$E2E_DIR/obot.log" | tail -30 >&2 || true
    fi
    if [ "${KEEP:-}" != 1 ]; then
        docker rm -f "$OBOT_CONTAINER" >/dev/null 2>&1 || true
        compose down -v --remove-orphans >/dev/null 2>&1 || true
    fi
    exit "$status"
}
trap cleanup EXIT

if [ "$PROVIDER_IMAGE" = obot-authentik-provider:e2e ]; then
    docker build -q -t "$PROVIDER_IMAGE" .. >/dev/null
fi

compose up -d postgresql authentik-server authentik-worker
# The worker creates the bootstrap token after the server reports ready.
wait_for authentik "$AK/core/users/me/" -H "$AK_ADMIN"
./setup-authentik.sh
# shellcheck source=/dev/null
source "$E2E_DIR/env"

# This is the README's Kubernetes install: the registry is an image volume, and its start-obot is
# the entrypoint. The host network lets obot and the test client reach authentik at the same
# issuer URL. obot needs a Docker API for MCP servers, or it won't start.
docker rm -f "$OBOT_CONTAINER" >/dev/null 2>&1 || true
docker run -d --name "$OBOT_CONTAINER" --network host \
    -e OBOT_SERVER_ENABLE_AUTHENTICATION=true -e OBOT_BOOTSTRAP_TOKEN=e2e-obot-bootstrap \
    -e OBOT_SERVER_HOSTNAME="$OBOT" \
    --mount "type=image,source=$PROVIDER_IMAGE,target=/obot-providers/authentik" \
    -v /var/run/docker.sock:/var/run/docker.sock \
    --entrypoint /obot-providers/authentik/start-obot "$OBOT_IMAGE" >/dev/null
wait_for obot "$OBOT/api/healthz"
wait_for "the provider registry" "$OBOT/api/auth-providers/authentik-auth-provider" -H "$BOOTSTRAP"

provider=$(curl -sSf -H "$BOOTSTRAP" "$OBOT/api/auth-providers/authentik-auth-provider")
check "obot loads the provider from the image volume" test "$(jq -r .name <<<"$provider")" = authentik
check "the provider needs no license" test "$(jq -r '.requiredEntitlements // [] | length' <<<"$provider")" = 0

# Sessions end when authentik's access token expires and can't be refreshed. setup-authentik.sh
# makes tokens last 30 seconds, so that a deactivation takes effect within the test.
TOKEN_SECONDS=30
REFRESH=15
config=$(jq -n --arg i "$ISSUER" --arg c "$CLIENT_ID" --arg s "$CLIENT_SECRET" --arg r "${REFRESH}s" \
    --arg cs "$(head -c 32 /dev/urandom | base64)" \
    '{OBOT_AUTHENTIK_AUTH_PROVIDER_ISSUER_URL: $i, OBOT_AUTHENTIK_AUTH_PROVIDER_CLIENT_ID: $c,
      OBOT_AUTHENTIK_AUTH_PROVIDER_CLIENT_SECRET: $s, OBOT_AUTH_PROVIDER_TOKEN_REFRESH_DURATION: $r,
      OBOT_AUTH_PROVIDER_COOKIE_SECRET: $cs, OBOT_AUTH_PROVIDER_EMAIL_DOMAINS: "*"}')
curl -sSf -o /dev/null -X POST -H "$BOOTSTRAP" -H "Content-Type: application/json" -d "$config" \
    "$OBOT/api/auth-providers/authentik-auth-provider/configure"

user_groups() {
    curl -sSf -H "$BOOTSTRAP" "$OBOT/api/users/$1" | jq -c '.authProviderGroups // [] | sort'
}
group_ids() {
    jq -nc '$ARGS.positional | map("authentik/" + .) | sort' --args "$@"
}

alice=$(./login.sh alice)
check "alice signs in" test "$(jq -r .email <<<"$alice")" = alice@example.com
got=$(user_groups "$(jq -r .id <<<"$alice")")
check "alice has her group and every ancestor, through both parents ($got)" test "$got" = "$(group_ids "$ENG" "$TECH" "$STAFF" "$OTHER")"

provider_patch() { ak PATCH "/providers/oauth2/$PROVIDER_PK/" -d "$1" >/dev/null; }
mappings=$(ak GET "/providers/oauth2/$PROVIDER_PK/" | jq -c .property_mappings)

provider_patch "$(jq -c --arg c "$CUSTOM_EMAIL_MAPPING" --arg d "$DEFAULT_EMAIL_MAPPING" \
    '{property_mappings: map(if . == $c then $d else . end)}' <<<"$mappings")"
check "authentik's default email mapping is refused" test -z "$(./login.sh bob)"
provider_patch "{\"property_mappings\": $mappings}"

bob=$(./login.sh bob)
check "bob signs in once fixed" test "$(jq -r .email <<<"$bob")" = bob@example.com
got=$(user_groups "$(jq -r .id <<<"$bob")")
check "bob has his groups ($got)" test "$got" = "$(group_ids "$OPS" "$TECH" "$STAFF")"

provider_patch "$(jq -c --arg g "$GROUPS_MAPPING" '{property_mappings: map(select(. != $g))}' <<<"$mappings")"
carol=$(./login.sh carol)
check "without the groups mapping, carol signs in" test "$(jq -r .email <<<"$carol")" = carol@example.com
got=$(user_groups "$(jq -r .id <<<"$carol")")
check "without the groups mapping, carol has no groups ($got)" test "$got" = "[]"
provider_patch "{\"property_mappings\": $mappings}"

found=$(curl -sSf -b "$E2E_DIR/jar-alice" "$OBOT/api/groups?name=oper" | jq -r '[.items[].name] | join(",")')
check "group search finds operations ($found)" test "$found" = operations

names=()
cursor=
for _ in $(seq 1 10); do
    page=$(curl -sSf -b "$E2E_DIR/jar-alice" "$OBOT/api/groups?limit=2&cursor=$cursor")
    mapfile -t -O "${#names[@]}" names < <(jq -r '.items[].name' <<<"$page")
    cursor=$(jq -r '.nextCursor // empty' <<<"$page")
    [ -n "$cursor" ] || break
done
listed=$(printf '%s\n' "${names[@]}" | sort | paste -sd,)
check "paging lists the groups of signed-in users ($listed)" test "$listed" = engineering,operations,other,staff,technology

ak PATCH "/core/users/$BOB_PK/" -d '{"is_active": false}' >/dev/null
sleep $((TOKEN_SECONDS + 10))
# Signed out, obot redirects to sign in instead of returning the user.
check "a deactivated user is signed out when their access token expires" \
    test -z "$(curl -s -b "$E2E_DIR/jar-bob" "$OBOT/api/me" | jq -r 'select(.email) | .email' 2>/dev/null)"

check "groups are kept in obot's Postgres database" \
    test -z "$(docker logs "$OBOT_CONTAINER" 2>&1 | grep 'kept in memory')"

if [ "$failures" -ne 0 ]; then
    echo "$failures check(s) failed" >&2
    exit 1
fi
echo "all checks passed"
