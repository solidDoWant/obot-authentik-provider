#!/bin/bash
# Signs in to obot through authentik as $1, as a browser would, and prints the obot user, or
# nothing when obot refuses the sign-in. Leaves the cookies in $E2E_DIR/jar-$1.
set -euo pipefail

USERNAME=$1
OBOT=http://localhost:8080
AK=http://127.0.0.1:9000
JAR=$E2E_DIR/jar-$USERNAME
rm -f "$JAR"

c() { curl -sS -b "$JAR" -c "$JAR" "$@"; }
location() { c -o /dev/null -w '%{redirect_url}' "$1"; }
csrf() { awk '$6 == "authentik_csrf" {print $7}' "$JAR" | tail -1; }

# Read the executor's current challenge. When the flow is done it redirects instead; report that
# as the redirect challenge.
challenge() {
    local out body to
    out=$(c -H 'Accept: application/json' -w '\n%{redirect_url}' "$1")
    # The executor redirects to itself between stages.
    for _ in 1 2 3; do
        to=${out##*$'\n'}
        case $to in "$AK"/api/v3/flows/executor/*) out=$(c -H 'Accept: application/json' -w '\n%{redirect_url}' "$to") ;; *) break ;; esac
    done
    to=${out##*$'\n'}
    body=${out%$'\n'*}
    if [ -n "$body" ]; then echo "$body"; else jq -n --arg to "$to" '{component: "xak-flow-redirect", to: $to}'; fi
}

# Run an authentik flow through its executor API, answering each stage, and print where it
# finally redirects.
run_flow() {
    local slug=$1 query=$2 url response component
    # The flow page sets the CSRF cookie that the executor API requires.
    c -o /dev/null "$AK/if/flow/$slug/?$query"
    url="$AK/api/v3/flows/executor/$slug/?query=$(jq -rn --arg q "$query" '$q|@uri')"
    response=$(challenge "$url")
    for _ in $(seq 1 10); do
        component=$(jq -r .component <<<"$response")
        case $component in
        xak-flow-redirect) jq -r .to <<<"$response"; return ;;
        ak-stage-identification) body=$(jq -n --arg u "$USERNAME" '{component: "ak-stage-identification", uid_field: $u}') ;;
        ak-stage-password) body='{"component": "ak-stage-password", "password": "e2e-user-password"}' ;;
        ak-stage-consent) body=$(jq -c '{component: "ak-stage-consent", token: .token}' <<<"$response") ;;
        *) echo "unexpected flow response: $response" >&2; exit 1 ;;
        esac
        # authentik redirects a request with an empty CSRF header, so send it only when there's a cookie.
        local headers=(-H 'Accept: application/json' -H 'Content-Type: application/json')
        [ -z "$(csrf)" ] || headers+=(-H "X-authentik-CSRF: $(csrf)")
        # An answer either returns the next challenge or redirects to the executor for it.
        response=$(c "${headers[@]}" -X POST "$url" -d "$body")
        [ -n "$response" ] || response=$(challenge "$url")
    done
    echo "flow $slug did not finish" >&2
    exit 1
}

# Follow redirects until the next hop is an authentik flow page or obot's callback has run.
follow() {
    local url=$1 next
    while :; do
        case $url in
        "$AK"/if/flow/*|/if/flow/*) echo "$url"; return ;;
        /*) url="$AK$url" ;;
        esac
        next=$(location "$url")
        [ -n "$next" ] || { echo "$url"; return; }
        case $url in "$OBOT"/oauth2/callback*) echo "$next"; return ;; esac
        url=$next
    done
}

url=$(follow "$OBOT/oauth2/start?obot-auth-provider=default/authentik-auth-provider&rd=/")
for _ in 1 2 3; do
    case $url in
    *"/if/flow/"*)
        slug=$(sed -E 's#.*/if/flow/([^/]+)/.*#\1#' <<<"$url")
        query=${url#*\?}
        url=$(follow "$(run_flow "$slug" "$query")")
        ;;
    *) break ;;
    esac
done
c "$OBOT/api/me" | jq -c 'select(.email) | {id, email, displayName}' 2>/dev/null || true
