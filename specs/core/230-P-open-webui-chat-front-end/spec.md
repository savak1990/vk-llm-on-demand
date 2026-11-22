---
id: "CORE-230"
status: "DRAFT"
updated: "2026-11-23"
depends_on: ["CORE-060", "CORE-110", "CORE-130"]
adrs: ["0009", "0010", "0012"]
---

# 230 — Open WebUI and SearXNG: a household chat front end

**Status note:** DRAFT. Design agreed with the user on 2026-11-22/23 in
three sections (components, configuration, operations); not implemented.
Like CORE-130, the spec lives here and most changes are pull requests
against `vk-lab-platform`.

**Complexity:** M — one upstream chart, one small Deployment, one new
database, four secrets, two repositories.
**Risk:** medium — a public login page on the internet; secrets in four
places each; the cluster's only worker node is at 88% memory use.
**Estimated effort:** one session (4–6 h) plus operator time for the four
secret values and two `terragrunt apply` runs.

## Outcome

A household member opens `https://chat.<fqdn>` in a browser, or the Conduit
app on a phone, signs in with an account the admin created, and chats with
the model on the rented GPU, with web search through a self-hosted SearXNG.
Chats and accounts live in the platform's backed-up Postgres and survive a
cluster rebuild. The GPU is still started by hand (`vk-llm start`,
`gpu-control`); automatic start is CORE-220.

## Scope

In `vk-lab-platform` (pull requests designed here):

- An Argo `Application` for the upstream Open WebUI chart, a SearXNG
  `Deployment`/`Service`/`ConfigMap`, a namespace `chat`, an `HTTPRoute`
  and a `BackendTrafficPolicy`, under `gitops/templates/platform/shared/chat/`,
  guarded off the `local` target.
- A Postgres role and database `openwebui` in the existing `lab-postgres`.
- Five SSM parameters with their ciphertext, Terraform, IAM ARNs and
  ExternalSecrets.
- The `client-keys` value of the existing `vk-llm-api-token` ExternalSecret
  assembled from two SSM values.
- Render-check lists and the aws golden.

In this repository:

- `config.gateway.maxTokensCap` raised to 16384 in `gitops/`.
- `docs/requirements.md` §13 amended: it lists "OpenHands and Open WebUI
  integrations" as out of scope; Open WebUI moves in scope.
- This spec.

Excludes: document upload and retrieval (RAG), voice (CORE-240), code
execution (CORE-250), image generation (CORE-260), Google sign-in
(CORE-270), automatic GPU start (CORE-220), per-person quotas, a metrics
dashboard for Open WebUI.

## Design and contracts

### Components

```
Phone (Conduit / home-screen app) ─┐
Laptop browser ───────────────────┴─▶ https://chat.<fqdn>  (platform-gateway, wildcard TLS, external-dns)
                                          │
                                   Open WebUI  (namespace chat, 1 replica)
                                    │        │                 │
             lab-postgres database "openwebui"  SearXNG         http://gpu-on-demand.llm.svc/v1
             (barman to S3 daily)            (ClusterIP only)  (own inference key)
```

| Component | Source | Notes |
|---|---|---|
| Open WebUI | Helm repo `https://helm.openwebui.com/`, chart `open-webui`, version **18.4.0** (app v0.13.4), pinned | Argo `Application` in `argocd`, `project: default`, like Grafana and Loki. Ollama, Pipelines and Tika subcharts off; no Redis; `persistence.enabled: false` |
| SearXNG | upstream image, pinned by digest | `Deployment` + `Service` (ClusterIP, port 8080) + `ConfigMap` `settings.yml` with `search.formats: [html, json]` and the limiter off; no `HTTPRoute` |
| Database | `lab-postgres` (CNPG, `cnpg-system`) | `spec.managed.roles` gains `openwebui` (password from an ExternalSecret in `cnpg-system`); a CNPG `Database` `openwebui` owned by it |
| Route | `HTTPRoute` `chat.<fqdn>` → Open WebUI service, parent `platform-gateway`/`envoy`/`https` | DNS and certificate come from the existing wildcard and external-dns |
| Traffic policy | `BackendTrafficPolicy` on that route | `timeout.http.requestTimeout: 660s`, the same bound as the GPU route |

Why no volume: Hetzner volumes have no snapshots and are deleted on
`make down`; only `lab-postgres` survives a rebuild (barman, daily 03:00,
2-day retention). This version stores no uploads, so `DATA_DIR` on the
container's own filesystem is enough.

### Open WebUI settings

`ENABLE_PERSISTENT_CONFIG=false`: the values below win on every start, so
Git is the source of truth. A change made in the admin panel to one of
these settings reverts on restart.

| Setting | Value | Why |
|---|---|---|
| `WEBUI_URL` | `https://chat.<fqdn>` | links and redirects |
| `DATABASE_URL` | `postgresql://openwebui:<password>@lab-postgres-rw.cnpg-system:5432/openwebui` | assembled by an ExternalSecret template |
| `WEBUI_SECRET_KEY` | from SSM | signs sessions |
| `ENABLE_SIGNUP` | `false` | the admin creates every account |
| `WEBUI_ADMIN_EMAIL`, `WEBUI_ADMIN_PASSWORD` | from SSM | creates the admin account at first start |
| `ENABLE_OLLAMA_API` | `false` | |
| `OPENAI_API_BASE_URL` | `http://gpu-on-demand.llm.svc/v1` | in-cluster; no `NetworkPolicy` exists today |
| `OPENAI_API_KEY` | from SSM | Open WebUI's own `sk-vkllm-` key |
| `TASK_MODEL_EXTERNAL` | `qwen3.8-27b` | background tasks use the same model |
| `ENABLE_TITLE_GENERATION` | `true` | one short call per new chat |
| `ENABLE_SEARCH_QUERY_GENERATION` | `true` | web search needs a query |
| `ENABLE_TAGS_GENERATION`, `ENABLE_FOLLOW_UP_GENERATION`, `ENABLE_AUTOCOMPLETE_GENERATION` | `false` | each is a full thinking-model call that queues behind real chats on a one-slot GPU |
| `ENABLE_WEB_SEARCH` | `true` | |
| `WEB_SEARCH_ENGINE` | `searxng` | |
| `SEARXNG_QUERY_URL` | `http://searxng.chat.svc:8080/search?q=<query>` | |
| `WEB_SEARCH_RESULT_COUNT` | `3` | |
| `BYPASS_WEB_SEARCH_EMBEDDING_AND_RETRIEVAL` | `true` | results go to the model directly; no embedding model runs in the pod |

All setting names were checked in Open WebUI v0.13.4's `backend/open_webui/config.py`
and `env.py` on 2026-11-22. If a `-slim` image exists for v0.13.4, use it
(no bundled models, smaller memory); otherwise the standard image.

### Secrets

All five values follow the existing path (KMS ciphertext → `persistent-secrets`
Terraform → SSM `SecureString` → External Secrets), **global scope**,
because a project-scope file must exist for every project that applies the
unit.

| SSM parameter | Ciphertext | Consumers |
|---|---|---|
| `/<project>/persistent/chat/inference_key` | `secrets/chat-inference-key.enc` | Open WebUI (`OPENAI_API_KEY`); the gateway's `client-keys` |
| `/<project>/persistent/chat/db_password` | `secrets/chat-db-password.enc` | CNPG role (`cnpg-system`); Open WebUI `DATABASE_URL` (`chat`) |
| `/<project>/persistent/chat/webui_secret_key` | `secrets/chat-webui-secret-key.enc` | Open WebUI |
| `/<project>/persistent/chat/admin_email` | `secrets/chat-admin-email.enc` | Open WebUI (personal data, kept out of Git) |
| `/<project>/persistent/chat/admin_password` | `secrets/chat-admin-password.enc` | Open WebUI |

Each parameter needs its ARN in the ESO policy, in both `rolesanywhere` and
`external-secrets-pod-identity`, in both statements.

The gateway accepts Open WebUI's key because the `vk-llm-api-token`
ExternalSecret assembles `client-keys` from two parameters with a
`target.template`: `api_token` (the operator's key) and `chat/inference_key`,
one per line. Removing either parameter's line revokes that client alone;
the gateway reloads keys within about 10 s of the Secret changing.

### Gateway adjustment (this repository)

`gitops/values.yaml` and the child `Application`'s `valuesObject` set
`config.gateway.maxTokensCap: 16384`. Open WebUI usually sends no
`max_tokens`, so the gateway injects the cap; at 8192 a long reasoning
answer can be cut off. The 1 MiB request limit stays: chat without uploads
stays far below it.

### Capacity

Requests start at Open WebUI 512 Mi / limit 1 Gi and SearXNG 128 Mi /
256 Mi, then are pinned to measured use after one week. The worker node is
at 88% memory use (2026-11-22), so the autoscaler is expected to add its one
allowed extra CX33 (about €6–7 per month). After that the autoscaler's
maximum is reached.

## Requirements

- R1. Open WebUI MUST be deployed from the pinned upstream chart 18.4.0 by
  an Argo `Application` that never renders on the `local` target.
- R2. All accounts and chats MUST be stored in the `openwebui` database in
  `lab-postgres`; Open WebUI MUST NOT use a persistent volume.
- R3. Self-registration MUST be off from the first start; the admin account
  MUST come from `WEBUI_ADMIN_EMAIL`/`WEBUI_ADMIN_PASSWORD`.
- R4. Every value in the settings table MUST be set from Git with
  `ENABLE_PERSISTENT_CONFIG=false`.
- R5. SearXNG MUST NOT be reachable from outside the cluster.
- R6. Open WebUI MUST call the GPU service with its own key, and the
  gateway MUST accept both that key and the operator's key.
- R7. Secrets, personal email addresses and the real domain MUST NOT appear
  in either repository.
- R8. Each new SSM parameter MUST be readable by ESO through an explicit ARN,
  never a wildcard.
- R9. `chat.<fqdn>` MUST get DNS and TLS from the existing external-dns and
  wildcard certificate, with no new certificate.
- R10. Tags, follow-up and autocomplete generation MUST be off; title and
  search-query generation MUST be on.
- R11. The gateway's output cap MUST be 16384 tokens.
- R12. The platform's render check and aws golden MUST include the new
  objects.

## Implementation steps

1. Operator: generate the five values (`make keygen` for the inference key,
   `openssl rand -hex 32` for the others), encrypt them with
   `make secret-encrypt SCOPE=global`. Check: five `.enc` files decrypt.
2. Platform PR: Terraform (five SSM parameters, five ARNs in both policy
   modules). Check: `terragrunt plan` shows 5 to add in `persistent/secrets`,
   1 to change in `rolesanywhere`.
3. Same PR: `cnpg-system` ExternalSecret and managed role, `Database` CR.
   Check: render check green.
4. Same PR: namespace `chat`, ExternalSecrets, Open WebUI `Application`,
   SearXNG manifests, `HTTPRoute`, `BackendTrafficPolicy`; `client-keys`
   template. Check: render check green, golden updated.
5. This repository: `maxTokensCap` 16384. Check: `make gitops-check`.
6. Operator: apply `persistent-up` and `bootstrap-up`, merge, verify.

## Tests / acceptance criteria

- AC1. `https://chat.<fqdn>` shows the Open WebUI login page with a valid
  certificate; a self-registration attempt is refused. (R3, R9)
- AC2. The admin signs in, creates one account; that person signs in from
  Conduit on a phone and from a laptop browser. (R3)
- AC3. With the GPU `READY`, an answer streams; the chat gets a title; no
  tags or follow-up suggestions appear; the gateway log shows Open WebUI's
  key id. (R6, R10)
- AC4. A question about current news returns an answer with sources. No
  `HTTPRoute` in the cluster references the SearXNG service, and
  `searxng.<fqdn>` does not resolve. (R5)
- AC5. With the GPU stopped, the chat shows a readable error, not a blank
  answer.
- AC6. After the Open WebUI pod is deleted and recreated, every account and
  chat is still present. (R2)
- AC7. A request without `max_tokens` is clamped to 16384 (gateway log
  `max_tokens_clamped`). (R11)
- AC8. Argo shows the new Applications `Synced`/`Healthy`; the render check
  passes. (R1, R12)

## Definition of done

- [ ] AC1–AC8 pass on the Hetzner lab.
- [ ] Both repositories' PRs merged; CI green.
- [ ] Requests pinned to measured use after one week (follow-up commit).
- [ ] Status flipped to DONE; folder renamed `230-D-open-webui-chat-front-end`.
- [ ] Evidence section filled.

## Open questions

| Question | Why it matters | Default until checked |
|---|---|---|
| Does a `-slim` image exist for v0.13.4? | Memory | use the standard image |
| Exact chart keys for the subchart and Redis toggles in 18.4.0 | R1 | read `helm show values` at implementation |
| Does Open WebUI's live connection (socket.io) work through Envoy Gateway v1.2.1 without extra settings? | streaming in the UI | assume yes; AC3 proves it |
| Does Conduit work with Open WebUI v0.13.4? | AC2 | assume yes |
| Measured memory of Open WebUI with no embedding model | capacity | 512 Mi request |

## Evidence

None yet.
