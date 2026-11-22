---
id: "CORE-270"
status: "DRAFT"
updated: "2026-11-22"
depends_on: ["CORE-230"]
adrs: []
---

# 270 — "Sign in with Google" for the chat front end

**Status note:** DRAFT, problem and requirements only. No design yet.
Written 2026-11-22. The Open WebUI front end (CORE-230) ships with local
accounts that the admin creates and signup switched off; this spec explores
replacing passwords with Google sign-in.

## Problem

Local accounts mean the operator creates every account, hands out a
starting password, and handles every "I forgot my password". Household
members already have Google accounts on their phones. Signing in with
Google removes the passwords, but a public sign-in button must not let any
Google user on the internet in.

## What exists today (verified 2026-11-22)

- Open WebUI v0.13.4 supports Google OAuth through `GOOGLE_CLIENT_ID` and
  `GOOGLE_CLIENT_SECRET`, with `ENABLE_OAUTH_SIGNUP`,
  `OAUTH_MERGE_ACCOUNTS_BY_EMAIL`, `OAUTH_ALLOWED_DOMAINS`,
  `ENABLE_LOGIN_FORM` and `WEBUI_URL` (names confirmed in its
  `config.py`/`env.py`; exact semantics to confirm at design time).
- `OAUTH_ALLOWED_DOMAINS` restricts by email domain. Household members use
  `gmail.com`, so a domain filter alone admits every Gmail user.
- The platform has no identity provider of its own. Ahorro's Cognito pool
  exists but is built for Ahorro and has no OAuth code flow configured.
- Conduit (the phone app) supports SSO and OAuth sign-in to Open WebUI.
- Secrets reach the cluster through SSM and External Secrets; a Google
  client secret would follow the same path (KMS ciphertext, SSM parameter,
  IAM ARN, ExternalSecret).

## Candidate approaches (to evaluate)

1. **Google sign-in for pre-created accounts only.** Signup stays off; the
   admin creates each account by email; Google sign-in attaches to the
   existing account with the same email. Strangers get no account.
2. **Google sign-in with signup on, new users pending.** Anyone can sign
   in and lands as "pending" until the admin approves. Needs watching.
3. **An identity provider in front** (Cognito, Authentik, oauth2-proxy at
   the gateway) that allows a list of emails. More pieces; reusable for
   other platform apps such as Grafana and Argo CD.

## Requirements

- R1. A household member MUST be able to sign in with their Google account
  on a phone (Conduit or the home-screen web app) and on a laptop.
- R2. A Google account that the admin did not allow MUST NOT get access to
  any chat, and MUST NOT create an account that sees anything.
- R3. The admin MUST keep a way to sign in that does not depend on Google
  (break-glass).
- R4. The Google client secret MUST reach the cluster through the
  platform's secret path and MUST NOT be committed in plain text.
- R5. Removing a person MUST be one admin action and MUST end their access.
- R6. The design MUST state the Google Cloud setup steps the operator does
  by hand (project, OAuth consent screen, redirect URI) and whether the app
  needs Google's verification for its consent screen.

## Open questions

| Question | Why it matters |
|---|---|
| Does `OAUTH_MERGE_ACCOUNTS_BY_EMAIL` with signup off give exactly approach 1? | Smallest approach if it does |
| Is a platform-wide identity provider worth it now? | Grafana and Argo CD have no SSO either |
| Does Google's "testing" consent mode (100 test users, 7-day tokens) suffice? | Avoids Google's verification process |

## Evidence

None. Problem and requirements only.
