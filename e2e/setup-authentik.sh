#!/bin/bash
# Configures the throwaway authentik at 127.0.0.1:9000 as the README describes, with the default
# hashed subject mode, plus groups and users to test with. Writes what the test needs to $E2E_DIR/env.
set -euo pipefail

AK=http://127.0.0.1:9000/api/v3
CALLBACK=http://localhost:8080/
OUT=$E2E_DIR/env

api() {
    local method=$1 path=$2
    shift 2
    curl -sS -f -X "$method" -H "Authorization: Bearer e2e-bootstrap-token" -H "Content-Type: application/json" "$AK$path" "$@"
}

flow_pk() { api GET "/flows/instances/?slug=$1" 2>/dev/null | jq -r '.results[0].pk'; }
mapping_pk() { api GET "/propertymappings/provider/scope/?managed=$1" 2>/dev/null | jq -r '.results[0].pk'; }

# authentik creates these from blueprints for a while after it reports ready.
for attempt in $(seq 1 90); do
    authz_flow=$(flow_pk default-provider-authorization-implicit-consent || true)
    inval_flow=$(flow_pk default-provider-invalidation-flow || true)
    openid=$(mapping_pk goauthentik.io/providers/oauth2/scope-openid || true)
    profile=$(mapping_pk goauthentik.io/providers/oauth2/scope-profile || true)
    offline=$(mapping_pk goauthentik.io/providers/oauth2/scope-offline_access || true)
    default_email=$(mapping_pk goauthentik.io/providers/oauth2/scope-email || true)
    signing_key=$(api GET "/crypto/certificatekeypairs/?has_key=true" 2>/dev/null | jq -r '.results[0].pk' || true)
    missing=
    for v in authz_flow inval_flow openid profile offline default_email signing_key; do
        [ -n "${!v}" ] && [ "${!v}" != null ] || missing+=" $v"
    done
    [ -n "$missing" ] || break
    [ "$attempt" -lt 90 ] || { echo "authentik never created:$missing" >&2; exit 1; }
    sleep 2
done

# The documented replacement for the default email mapping, which always sends email_verified: false.
email=$(api POST /propertymappings/provider/scope/ -d "$(jq -n '{name: "obot e2e email", scope_name: "email", expression: "return {\"email\": request.user.email, \"email_verified\": True}"}')" | jq -r .pk)

# The README's groups mapping: each group the user is in, directly or through a child group.
groups_expression='return {"obot_groups": [{"id": str(group.pk), "name": group.name} for group in request.user.all_groups()]}'
groups_mapping=$(api POST /propertymappings/provider/scope/ -d "$(jq -n --arg e "$groups_expression" '{name: "obot groups", scope_name: "obot_groups", expression: $e}')" | jq -r .pk)

client_secret=$(head -c 32 /dev/urandom | base64 | tr -dc 'A-Za-z0-9')
provider=$(api POST /providers/oauth2/ -d "$(jq -n \
    --arg af "$authz_flow" --arg if "$inval_flow" --arg sk "$signing_key" --arg cs "$client_secret" --arg cb "$CALLBACK" \
    --arg o "$openid" --arg p "$profile" --arg e "$email" --arg off "$offline" --arg g "$groups_mapping" \
    '{name: "obot", authorization_flow: $af, invalidation_flow: $if, client_type: "confidential", client_id: "obot-e2e",
      client_secret: $cs, redirect_uris: [{matching_mode: "strict", url: $cb}], signing_key: $sk,
      grant_types: ["authorization_code", "refresh_token"], access_token_validity: "seconds=30",
      issuer_mode: "per_provider", property_mappings: [$o, $p, $e, $off, $g]}')" | jq -r .pk)
api POST /core/applications/ -d "$(jq -n --argjson p "$provider" '{name: "Obot", slug: "obot", provider: $p}')" >/dev/null

group() {
    api POST /core/groups/ -d "$(jq -n --arg n "$1" --argjson parents "$2" '{name: $n, parents: $parents}')" | jq -r .pk
}
staff=$(group staff '[]')
other=$(group other '[]')
tech=$(group technology "[\"$staff\"]")
eng=$(group engineering "[\"$tech\", \"$other\"]")
ops=$(group operations "[\"$tech\"]")
for i in $(seq 1 7); do group "bulk-$i" '[]' >/dev/null; done

user() {
    local pk
    pk=$(api POST /core/users/ -d "$(jq -n --arg u "$1" --argjson g "$2" '{username: $u, name: ($u | ascii_upcase), email: ($u + "@example.com"), groups: $g, path: "users"}')" | jq -r .pk)
    api POST "/core/users/$pk/set_password/" -d '{"password": "e2e-user-password"}' >/dev/null
    echo "$pk"
}
alice=$(user alice "[\"$eng\"]")
bob=$(user bob "[\"$ops\"]")
carol=$(user carol "[\"$ops\"]")

issuer=$(curl -sSf http://127.0.0.1:9000/application/o/obot/.well-known/openid-configuration | jq -r .issuer)
cat > "$OUT" <<EOF
PROVIDER_PK=$provider
DEFAULT_EMAIL_MAPPING=$default_email
CUSTOM_EMAIL_MAPPING=$email
GROUPS_MAPPING=$groups_mapping
ISSUER=$issuer
CLIENT_ID=obot-e2e
CLIENT_SECRET=$client_secret
ALICE_PK=$alice
BOB_PK=$bob
CAROL_PK=$carol
ENG=$eng
OPS=$ops
TECH=$tech
STAFF=$staff
OTHER=$other
EOF
