# obot-authentik-provider

An [Obot](https://obot.ai) auth provider for [authentik](https://goauthentik.io). Users sign in to
Obot with authentik, and Obot uses their authentik groups for access control.

It ships as a container image that holds an Obot provider registry. You mount the image into the
Obot container, and Obot loads the provider next to its built-in ones. You don't need to change or
rebuild Obot.

- A user is a member of their authentik groups and of every parent of those groups, through all
  parents.
- Users deactivated in authentik lose access to Obot within about 10 minutes, when Obot next
  refreshes their groups.
- The provider needs no Obot license.

## Requirements

- Obot v0.27.0. Other versions may work. See [Compatibility](#compatibility).
- authentik 2025.12 or later.
- To mount the image on Kubernetes, image volumes, which need Kubernetes 1.35 or later, or 1.33
  or later with the `ImageVolume` feature gate on, and a container runtime that supports them,
  such as containerd 2.1 or later.

## Install on Kubernetes

Two changes to the Obot deployment are needed:

1. Mount the image at `/obot-providers/authentik`.
2. Add that directory to the provider registries. Obot's image sets the list of registries when it
   starts, from `/obot-providers/.envrc.providers`, so an environment variable doesn't work. The
   container command adds a line to that file and then starts Obot as usual.

With the [Obot Helm chart](https://charts.obot.ai), the mount goes in the chart values. The chart
can't change the container command, so a post-renderer patches it. For example, with a Flux
`HelmRelease`:

```yaml
spec:
  values:
    extraVolumes:
      - name: authentik-provider
        image:
          reference: ghcr.io/soliddowant/obot-authentik-provider:0.1.0
          pullPolicy: IfNotPresent
    extraVolumeMounts:
      - name: authentik-provider
        mountPath: /obot-providers/authentik
        readOnly: true
  postRenderers:
    - kustomize:
        patches:
          - target:
              kind: Deployment
              labelSelector: app.kubernetes.io/name=obot,app.kubernetes.io/component=gateway
            patch: |
              - op: add
                path: /spec/template/spec/containers/0/command
                value: [/bin/sh, -c]
              - op: add
                path: /spec/template/spec/containers/0/args
                value:
                  - |
                    echo 'export OBOT_SERVER_PROVIDER_REGISTRIES="${OBOT_SERVER_PROVIDER_REGISTRIES},/obot-providers/authentik"' >> /obot-providers/.envrc.providers
                    exec run.sh
```

With plain Helm, apply the same patch with `helm install --post-renderer`, or render the chart and
patch it with Kustomize.

The container's root filesystem must be writable, which is the chart's default.

To check the install, look for this line in the Obot log. Obot loads registries about a minute
after it starts.

```text
"message":"Loaded provider registry","registry":"/obot-providers/authentik","providers":1
```

**authentik** then appears in **Identity & Access > Auth Providers**.

## Set up authentik

You create an application with an OAuth2/OpenID provider, which lets users sign in to Obot, and
an API token, which lets Obot read users and groups.

### Create the application and provider

1. In authentik, go to **Applications > Applications** and click **Create with provider**.
2. Choose **OAuth2/OpenID Provider**.
3. Set **Client type** to **Confidential**.
4. Add a **Strict** redirect URI with the callback URL that Obot shows when you configure the
   provider. It's Obot's address followed by `/`, such as `https://obot.example.com/`.
5. Make sure the **Authorization Code** and **Refresh Token** grant types are enabled. authentik
   rejects every sign-in through a provider with no grant types, which is how providers created
   through its API or with blueprints start.
6. Under **Advanced protocol settings**:
   - In **Scopes**, keep `openid`, `email`, and `profile`, and add `offline_access`.
   - Set **Subject mode** to **Based on the User's UUID**. Obot uses the subject to find the
     user's groups, and the default hashed ID can't be looked up.
   - Keep **Issuer mode** at **Each provider has a different issuer**.
7. Note the **Client ID** and **Client Secret**. The provider's **OpenID Configuration Issuer**,
   shown on its overview page, is the Issuer URL.

### Create the API token

1. Go to **Directory > Users** and click **New Service Account**. authentik shows a token for the
   account. You can ignore it: it's an app password, which can't call the API.
2. Go to **Directory > Roles** and create a role. Under **Assigned global permissions**, add
   **Can view User** and **Can view Group**. These are the only permissions it needs. They must be
   global: with per-object permissions, Obot would see only some users and groups.
3. Assign the role to the service account.
4. Go to **Directory > Tokens and App passwords** and create a token for the service account,
   with the intent **API**. If it expires, replace it in Obot before it does, or group lookups
   fail when users sign in.

### Set up email verification

By default, users must have a verified email address to sign in. authentik's default `email`
scope mapping always sends `email_verified: false`, so with it nobody can sign in until you do one
of the following:

- Create a scope mapping (**Customization > Property Mappings**, **Scope Mapping**) with the scope
  name `email` and an expression that returns `email_verified: True` only for addresses you trust.
  For example, if your administrators set every user's email:

  ```python
  return {
      "email": request.user.email,
      "email_verified": True,
  }
  ```

  In the provider's **Scopes**, replace the default `email` mapping with yours.
- Set **Allow Unverified Emails** to `true` in Obot.

Users who can change their own email address in authentik can claim any address. If your user
settings flow lets them, don't send `email_verified: True` for those addresses. Obot's email
domain restriction is only as trustworthy as the addresses.

## Configure Obot

In **Identity & Access > Auth Providers**, configure **authentik**:

| Field | Value |
| --- | --- |
| Issuer URL | The provider's OpenID Configuration Issuer, such as `https://auth.example.com/application/o/obot/` |
| Client ID / Client Secret | The provider's client credentials |
| API Token | The service account's API token |
| Allowed E-Mail Domains | Allowed domains, or `*` for all domains |
| Allow Unverified Emails (optional) | `true` to skip the email check. See [Set up email verification](#set-up-email-verification). |
| Token Refresh Duration (optional) | How often Obot refreshes users' tokens. Set it shorter than the provider's **Access token validity**, which is 1 hour by default, for example `50m`. |

Obot reads authentik's API at the issuer's origin, under `/api/v3/`, so it must be able to reach
authentik there.

If sign-in fails, the reason is in the Obot log, in lines that start with
`authentik-auth-provider`.

## Limitations

These come from running as a separately installed provider. A provider built into Obot wouldn't
have them.

- **No display names or avatars.** Obot only copies these from providers that it knows by name.
  Users appear by their email address.
- **English only.** Obot translates the configuration form only for its own providers.
- **Allow Unverified Emails is a text field,** not a switch. Type `true` to turn it on.
- **No SCIM.** Obot reads groups from authentik when users sign in, and then every 10 minutes
  while they're active. Group changes take up to 10 minutes to apply. Users removed from
  authentik keep an Obot account, which can't be used but counts toward Obot's user limit, until
  you delete it in Obot.

## Compatibility

The provider relies on how Obot runs providers, which is internal to Obot and can change in any
release. Each release of this provider is tested against the Obot version under
[Requirements](#requirements). Test a new Obot version before upgrading to it.

## Development

- `make test` runs the unit tests and `make lint` runs golangci-lint.
- `make e2e` runs the end-to-end test. It runs Obot's released image with this provider mounted
  as an image volume, against a throwaway authentik, and signs users in. It needs a Docker that
  supports image mounts (`docker run --mount type=image`). `KEEP=1 make e2e` leaves everything
  running afterwards.
- `make build` builds the registry into `build/registry`. To run Obot from source with it, set
  `OBOT_SERVER_PROVIDER_REGISTRIES` to that directory.

## License

MIT. Parts are adapted from Obot's Keycloak provider, which is also MIT licensed.
