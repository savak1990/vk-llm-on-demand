---
id: "CORE-270"
status: "READY"
updated: "2026-11-26"
depends_on: ["CORE-230"]
adrs: ["0009", "0020"]
---

# 270 — "Sign in with Google" for the chat front end

**Status note:** READY. Design agreed with the user on 2026-11-26 (sign-up
flow, password form, consent-screen mode, notifications). Not implemented.
The settings change this repository's `chat/` chart (ADR 0020); the two new
secrets are a pull request against `vk-lab-platform`.

**Complexity:** S — six settings, one ConfigMap key, two secrets, one
manual Google Cloud setup.
**Risk:** low — a pending account sees nothing and cannot reach the model;
the admin password login stays as break-glass.
**Estimated effort:** half a session (2–3 h) plus about 10 minutes of
operator time in Google Cloud.

## Problem

Local accounts mean the operator creates every account, hands out a
starting password, and handles every "I forgot my password". Household
members and friends already have Google accounts. The operator wants to
send them the chat link: they sign in with Google, and only the accounts
the operator activates can chat.

## Outcome

A person opens `https://chat.<fqdn>` in a browser or in Conduit and clicks
"Continue with Google". Open WebUI creates their account as `pending`; they
see "account activation pending" and nothing else. The admin sets the role
to `user` in Admin → Users, and from then on they chat. An account that
already exists with the same email (for example one the admin created
earlier) is linked to the Google identity, not duplicated. The admin keeps
signing in with email and password.

## What exists today (verified 2026-11-26, Open WebUI v0.13.4)

- `utils/oauth.py:2095-2237`: after a Google login Open WebUI looks up the
  account by OAuth `sub`; if none and `OAUTH_MERGE_ACCOUNTS_BY_EMAIL` is on,
  by email (then it stores the `sub` on that account); if still none and
  `ENABLE_OAUTH_SIGNUP` is on, it creates the account with role
  `DEFAULT_USER_ROLE`, else it answers 403.
- `config.py:1752`: `DEFAULT_USER_ROLE` defaults to `pending`.
- `utils/auth.py:570-579`: only roles `user` and `admin` pass
  `get_verified_user`, so a pending account reaches no chat and no model —
  it cannot start or keep the GPU busy.
- `config.py:2618-2625`: `GOOGLE_CLIENT_ID`, `GOOGLE_CLIENT_SECRET`,
  `GOOGLE_OAUTH_SCOPE` (default `openid email profile`), `GOOGLE_REDIRECT_URI`.
- `main.py:2996-2997`: callback `/oauth/{provider}/login/callback` (and the
  legacy `/oauth/{provider}/callback`).
- `config.py:1692` `ENABLE_LOGIN_FORM` only hides the password fields in the
  UI; `auths.py:667` `ENABLE_PASSWORD_AUTH` is what refuses password
  sign-in on the server.
- `auths.py:99,764`: password sign-in is limited to 15 attempts per email
  per 3 minutes (in memory; one replica); the platform adds 20 req/s on
  `/api/v1/auths/`.
- `ENABLE_SIGNUP` (password self-registration) is already `false`
  (CORE-230) and stays so.
- Conduit supports OAuth/SSO sign-in to Open WebUI.

## Design and contracts

### Settings (this repository, `chat/values.yaml`)

| Setting | Value | Why |
|---|---|---|
| `ENABLE_OAUTH_SIGNUP` | `true` | a person signs in with Google and an account appears |
| `DEFAULT_USER_ROLE` | `pending` | set explicitly; a new account sees nothing until the admin activates it |
| `OAUTH_MERGE_ACCOUNTS_BY_EMAIL` | `true` | an account the admin created with the same email is linked, not refused as "email taken"; Google verifies its emails |
| `ENABLE_LOGIN_FORM` | `true` | the admin's email/password login stays (break-glass) |
| `ENABLE_PASSWORD_AUTH` | `true` (default, not set) | the server keeps accepting the admin password |
| `GOOGLE_CLIENT_ID`, `GOOGLE_CLIENT_SECRET` | from Secret `open-webui-env` | the Google OAuth client |
| `GOOGLE_REDIRECT_URI` | `https://chat.<fqdn>/oauth/google/login/callback`, in ConfigMap `open-webui-config` | needs `fqdn`, which is never committed; explicit because behind Envoy the app could otherwise build an `http://` callback, which Google rejects |

`GOOGLE_OAUTH_SCOPE` keeps its default `openid email profile`.
`ENABLE_SIGNUP=false` stays: there is no password self-registration.

### Secrets (`vk-lab-platform`)

Two parameters on the existing path (KMS ciphertext → `persistent-secrets`
Terraform → SSM `SecureString` → External Secrets), global scope, each with
an explicit ARN in `rolesanywhere` and `external-secrets-pod-identity`, in
both statements:

| SSM parameter | Ciphertext | Consumer |
|---|---|---|
| `/<project>/persistent/chat/google_client_id` | `secrets/chat-google-client-id.enc` | `open-webui-env` key `GOOGLE_CLIENT_ID` |
| `/<project>/persistent/chat/google_client_secret` | `secrets/chat-google-client-secret.enc` | `open-webui-env` key `GOOGLE_CLIENT_SECRET` |

The client ID is not a credential, but it belongs to the operator's own
Google project and this repository is public, so it follows the same path.

### Google Cloud (operator, by hand, once)

1. `console.cloud.google.com` → new project (any name).
2. Google Auth Platform → Branding / Audience: user type **External**, app
   name, support email; Data access: scopes `openid`, `.../auth/userinfo.email`,
   `.../auth/userinfo.profile` only; Audience → **Publish app** (status
   **In production**). Only non-sensitive scopes, so Google requires no app
   verification and shows no warning.
3. Clients → Create client → **Web application**; Authorized redirect URI
   `https://chat.<fqdn>/oauth/google/login/callback`.
4. Encrypt the client ID and secret:
   `make secret-encrypt NAME=chat-google-client-id VALUE=... SCOPE=global`,
   `make secret-encrypt NAME=chat-google-client-secret VALUE=... SCOPE=global`.

### Rollout order

1. Operator: Google setup, two `.enc` files.
2. Platform PR (Terraform, IAM, ExternalSecret keys); `make persistent-up`
   and `make bootstrap-up`; merge.
3. This repository's PR (settings, ConfigMap key, gitops-check). If it
   merged first, the Secret has no Google keys yet and Open WebUI shows no
   Google button: nothing breaks.

## Requirements

- R1. Google sign-in MUST create a new account with role `pending`; a
  pending account MUST NOT reach any chat or model.
- R2. An existing account with the same email MUST be linked to the Google
  identity, not duplicated.
- R3. The admin MUST keep a password login that does not depend on Google:
  `ENABLE_LOGIN_FORM` true, `ENABLE_PASSWORD_AUTH` not false.
- R4. Password self-registration MUST stay off (`ENABLE_SIGNUP=false`).
- R5. The Google client ID and secret MUST reach the pod through the
  platform's secret path, with explicit ARNs, and MUST NOT appear in Git in
  plain text.
- R6. `GOOGLE_REDIRECT_URI` MUST be `https://chat.<fqdn>/oauth/google/login/callback`,
  rendered from `fqdn`, never committed.
- R7. The consent screen MUST use only `openid`, `email` and `profile` and
  be published (In production); the operator steps are listed in this spec.
- R8. `make gitops-check` MUST fail if the rendered settings of R1, R3, R4
  or R6 change.
- R9. Removing a person MUST be one admin action (delete the user or set
  role `pending`) and MUST end their access at their next request.

## Implementation steps

1. Operator: Google setup (Design → Google Cloud, steps 1–3) and the two
   `.enc` files. Check: both files decrypt.
2. Platform PR: two SSM parameters + variables + terragrunt inputs, ARNs in
   both IAM modules, `require-persistent-secrets.sh` names, two `data`
   entries and two template keys in ExternalSecret `open-webui-env`. Check:
   `make terraform-check scripts-check gitops-check`.
3. Operator: `make persistent-up` (2 to add), `make bootstrap-up` (ESO
   policy), merge the platform PR. Check: Secret `open-webui-env` has
   `GOOGLE_CLIENT_ID` and `GOOGLE_CLIENT_SECRET`.
4. This repository: settings in `chat/values.yaml`, `GOOGLE_REDIRECT_URI`
   in `chat/templates/config.yaml`, gitops-check assertions. Check:
   `make gitops-check specs-check`.
5. Merge; roll the pod (`kubectl -n chat rollout restart deploy/open-webui`
   if the Secret changed after the pod started). Acceptance below.

## Tests / acceptance criteria

- AC1. The login page shows "Continue with Google" and the email/password
  form. (R3, R7)
- AC2. The operator's wife signs in with Google on a laptop; her
  pre-existing account (same Gmail address) is linked: Admin → Users still
  shows one account for her. (R2)
- AC3. A second Google account that has no account yet signs in and sees
  "activation pending"; `GET /api/models` with its session returns 401/403;
  after the admin sets role `user`, it can chat. (R1)
- AC4. The same person signs in with Google in Conduit on a phone. (R1, R2)
- AC5. The admin signs in with email and password while Google sign-in is
  configured. (R3)
- AC6. `POST /api/v1/auths/signup` is still refused. (R4)
- AC7. Setting a user back to `pending` (or deleting them) ends their access
  at their next request. (R9)
- AC8. `make gitops-check` fails when `ENABLE_OAUTH_SIGNUP`,
  `DEFAULT_USER_ROLE`, `ENABLE_LOGIN_FORM`, `ENABLE_SIGNUP` or the redirect
  URI change. (R8)

## Not in this spec

- Apple sign-in: Open WebUI has only generic OIDC for it, Apple's flow is
  non-standard, and it needs a paid Apple Developer account.
- Notifications about pending sign-ups (`WEBHOOK_URL`): the admin checks
  Admin → Users.
- Hiding the password form (`ENABLE_LOGIN_FORM=false`).
- A platform-wide identity provider for Grafana and Argo CD.

## Evidence

None yet.
