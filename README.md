# obot-authentik-provider

An [Obot](https://obot.ai) auth provider for [authentik](https://goauthentik.io). Users sign in to
Obot with authentik, and Obot uses their authentik groups for access control.

It ships as a container image that holds an Obot provider registry. You mount the image into the
Obot container, and Obot loads the provider next to its built-in ones. You don't need to change or
rebuild Obot.

- Groups come from the ID token: a user is a member of their authentik groups and of every parent
  of those groups, through all parents. The provider needs no access to authentik's API.
- Users deactivated in authentik lose access to Obot when their access token expires. See
  [Sessions and changes](#sessions-and-changes).
- The provider needs no Obot license.

## Requirements

- Obot v0.27.0. Other versions may work. See [Compatibility](#compatibility).
- authentik 2025.12 or later.
- To mount the image on Kubernetes, image volumes, which need Kubernetes 1.35 or later, or 1.33
  or later with the `ImageVolume` feature gate on, and a container runtime that supports them,
  such as containerd 2.1 or later.

## Install on Kubernetes

Two changes to the Obot deployment are needed:

1. Mount the image, for example at `/obot-providers/authentik`.
2. Set the container command to `start-obot`, from the image. It adds the directory it's in to
   the provider registries that Obot loads, then starts Obot as usual. Obot's image sets the
   registries itself when it starts, so an environment variable doesn't work.

With the [Obot Helm chart](https://charts.obot.ai), the mount goes in the chart values. The chart
can't change the container command, so a post-renderer patches it. For example, with a Flux
`HelmRelease`:

```yaml
spec:
  values:
    extraVolumes:
      - name: authentik-provider
        image:
          reference: ghcr.io/soliddowant/obot-authentik-provider:0.3.0
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
                value: [/obot-providers/authentik/start-obot]
```

With plain Helm, apply the same patch with `helm install --post-renderer`, or render the chart and
patch it with Kustomize.

The container's root filesystem must be writable, which is the chart's default: `start-obot` adds
a line to `/obot-providers/.envrc.providers`, which Obot's image reads when it starts. If the file
isn't there, `start-obot` stops with an error rather than starting Obot without the provider.

To run more than one provider image this way, give `start-obot` the next one's `start-obot` as
arguments, for example `[/obot-providers/authentik/start-obot, /obot-providers/other/start-obot]`.
`start-obot` runs its arguments in place of Obot's usual startup.

To check the install, look for this line in the Obot log. Obot loads registries about a minute
after it starts.

```text
"message":"Loaded provider registry","registry":"/obot-providers/authentik","providers":1
```

**authentik** then appears in **Identity & Access > Auth Providers**.

## Set up authentik

You create a scope mapping that puts users' groups in their ID tokens, and an application with an
OAuth2/OpenID provider, which lets users sign in to Obot.

### Create the groups scope mapping

Go to **Customization > Property Mappings**, and create a **Scope Mapping** with the scope name
`obot_groups` and this expression:

```python
return {
    "obot_groups": [
        {"id": str(group.pk), "name": group.name}
        for group in request.user.all_groups()
    ]
}
```

`all_groups()` returns the user's groups and every parent of them. Obot identifies groups by their
UUIDs, so renaming a group in authentik doesn't break access rules in Obot.

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
   - In **Scopes**, keep `openid`, `email`, and `profile`, and add `offline_access` and your
     `obot_groups` mapping.
   - Set **Access token validity** to `minutes=5`. Deactivated users keep access until their
     access token expires.
   - Any **Subject mode** works. Don't change it after users have signed in: the subject is the
     user's identity in Obot, so a new subject makes a new Obot user.
   - Keep **Include claims in id_token** on, which is the default. The provider reads groups
     from the ID token.
   - Keep **Issuer mode** at **Each provider has a different issuer**.
7. Note the **Client ID** and **Client Secret**. The provider's **OpenID Configuration Issuer**,
   shown on its overview page, is the Issuer URL.

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
| Allowed E-Mail Domains | Allowed domains, or `*` for all domains |
| Allow Unverified Emails (optional) | `true` to skip the email check. See [Set up email verification](#set-up-email-verification). |
| Token Refresh Duration (optional) | How often Obot refreshes users' tokens, and so their groups. Set it shorter than the provider's **Access token validity**, for example `4m`. The default is `1h`. |

If sign-in fails, the reason is in the Obot log, in lines that start with
`authentik-auth-provider`.

## Sessions and changes

- **Groups** come from users' ID tokens. The provider saves each user's groups when they sign in
  and whenever their token is refreshed, in Obot's database. Obot rechecks a user's groups every
  10 minutes. A change in authentik therefore reaches Obot within the Token Refresh Duration plus
  10 minutes.
- **Deactivating** a user in authentik revokes their tokens. Their session can't be refreshed, so
  it ends when their access token expires, within the provider's **Access token validity**.
- **Without a groups claim,** because the scope mapping is missing from the provider, users sign
  in with no groups, and the Obot log says why. A malformed claim refuses the sign-in.
- **Without Postgres,** if Obot runs on SQLite, groups are kept in memory. Users then have no groups
  after a restart until they sign in again, and the log warns about it.

## Upgrade from 0.2.0

0.3.0 reads groups from ID tokens instead of authentik's API.

1. Create the [groups scope mapping](#create-the-groups-scope-mapping), and add it to the
   provider's **Scopes**.
2. Set the provider's **Access token validity** to `minutes=5`, and Obot's **Token Refresh
   Duration** to `4m`.
3. After upgrading, users get their groups as they sign in or their tokens refresh. Until then,
   they have none.
4. Delete the service account, its role, and its API token in authentik. Obot no longer uses the
   API Token setting.

The subject mode no longer needs to be the user's UUID, but keep it unchanged.

## Upgrade from 0.1.0

0.1.0 had a two-part container patch: a `/bin/sh -c` command and an `args` script that edited
`/obot-providers/.envrc.providers`. Replace both with the one `command` above, and remove the
`args`.

## Limitations

These come from running as a separately installed provider. A provider built into Obot wouldn't
have them.

- **No display names or avatars.** Obot only copies these from providers that it knows by name.
  Users appear by their email address.
- **English only.** Obot translates the configuration form only for its own providers.
- **Allow Unverified Emails is a text field,** not a switch. Type `true` to turn it on.
- **Group pickers only show groups that someone is in.** Where Obot lets admins pick a group, for
  role assignments and access rules, it offers only the groups of users who have signed in. To
  grant something to a new group, have one of its members sign in first.
- **No SCIM.** Group changes take a while to apply, as [Sessions and
  changes](#sessions-and-changes) describes. Users removed from authentik keep an Obot account,
  which can't be used but counts toward Obot's user limit, until you delete it in Obot.

## Compatibility

The provider relies on how Obot runs providers, which is internal to Obot and can change in any
release. Each release of this provider is tested against the Obot version under
[Requirements](#requirements). Test a new Obot version before upgrading to it.

## Development

- `make test` runs the unit tests and `make lint` runs golangci-lint. To also test the Postgres
  group store, set `OBOT_AUTHENTIK_TEST_POSTGRES_DSN` to a database the tests may write to.
- `make e2e` runs the end-to-end test. It runs Obot's released image with this provider mounted
  as an image volume, against a throwaway authentik, and signs users in. It needs a Docker that
  supports image mounts (`docker run --mount type=image`). `KEEP=1 make e2e` leaves everything
  running afterwards.
- `make build` builds the registry into `build/registry`. To run Obot from source with it, set
  `OBOT_SERVER_PROVIDER_REGISTRIES` to that directory.

## License

MIT. Parts are adapted from Obot's Keycloak provider, which is also MIT licensed.
