---
id: "CORE-230"
status: "READY"
updated: "2026-11-23"
depends_on: ["CORE-060", "CORE-110", "CORE-130"]
adrs: ["0009", "0010", "0012"]
---

# 230 — Open WebUI and SearXNG: a household chat front end

**Status note:** READY. Design agreed with the user on 2026-11-22/23 and
re-checked on 2026-11-23 (authorization, hosting, features, logs, limits,
acceptance). Not implemented. Like CORE-130, the spec lives here and most
changes are a pull request against `vk-lab-platform`.

**Complexity:** M — one upstream chart, one small Deployment, one new
database, five secrets, two repositories.
**Risk:** medium — a public login page on the internet; secrets in five
places each; cluster capacity comes from a separate platform spec.
**Estimated effort:** one session (4–6 h) plus operator time for the five
secret values, two `terragrunt apply` runs and one capped GPU session.

## Outcome

A household member opens `https://chat.<fqdn>` in a browser, or the Conduit
app on a phone, signs in with an account the admin created, and chats with
the model on the rented GPU, with web search through a self-hosted SearXNG.
Chats and accounts live in the platform's backed-up Postgres and survive a
cluster rebuild. The GPU is still started by hand (`vk-llm start`,
`gpu-control`); automatic start is CORE-220.

## Scope

In `vk-lab-platform` (one pull request, designed here):

- A namespace `chat` with an Argo `Application` for the upstream Open WebUI
  chart, a SearXNG `Deployment`/`Service`/`ConfigMap`, an `HTTPRoute` and a
  `BackendTrafficPolicy`, under `gitops/templates/platform/shared/chat/`,
  guarded off the `local` target.
- A Postgres role and database `openwebui` in the existing `lab-postgres`.
- Five SSM parameters with their ciphertext, Terraform, IAM ARNs and
  ExternalSecrets.
- The `client-keys` value of the existing `vk-llm-api-token` ExternalSecret
  assembled from two SSM values.
- Render-check lists and the aws golden.

In this repository:

- `config.gateway.maxTokensCap: 16384` and
  `config.gateway.rateLimitPerKeyRPM: 100` in `gitops/`.
- `docs/requirements.md` §13 amended: it lists "OpenHands and Open WebUI
  integrations" as out of scope; Open WebUI moves in scope.
- This spec.

Not in this spec:

- Node capacity. HETZ-176 in `vk-lab-platform` (a separate session) makes
  room for these pods; see Capacity.
- File and image uploads, document retrieval (RAG). Image input gets its own
  spec.
- Voice (CORE-240), code execution (CORE-250), image generation (CORE-260),
  Google sign-in (CORE-270), automatic GPU start (CORE-220).
- Per-user API keys, per-person quotas, a metrics dashboard for Open WebUI.
- Any change to the gateway's `backend_unavailable` text.

## Design and contracts

### Components

```
Phone (Conduit / home-screen app) ─┐
Laptop browser ───────────────────┴─▶ https://chat.<fqdn>  (platform-gateway, wildcard TLS, external-dns)
                                          │  1200 s; /api/v1/auths/ also 20 req/s
                                   Open WebUI  (namespace chat, 1 replica)
                                    │        │                 │
             lab-postgres database "openwebui"  SearXNG         http://gpu-on-demand.llm.svc/v1
             (barman to S3 daily)            (ClusterIP only)  (own inference key)
```

| Component | Source | Notes |
|---|---|---|
| Open WebUI | Helm repo `https://helm.openwebui.com/`, chart `open-webui`, version **18.4.0** (app v0.13.4), pinned; image tag `v0.13.4-slim` | Argo `Application` in `argocd`, `project: default`, like Grafana and Loki. Ollama, Pipelines and Tika subcharts off; no Redis; `persistence.enabled: false`; strategy Recreate (one replica, one database), readiness and startup probes on /health |
| SearXNG | upstream image, pinned by digest | `Deployment` + `Service` (ClusterIP, port 8080) + `ConfigMap` `settings.yml` with `search.formats: [html, json]` and the limiter off; `GRANIAN_WORKERS=1`; no `HTTPRoute` |
| Database | `lab-postgres` (CNPG, `cnpg-system`) | `spec.managed.roles` gains `openwebui` (password from an ExternalSecret in `cnpg-system`); a CNPG `Database` `openwebui` owned by it |
| Route | `HTTPRoute` `chat.<fqdn>` → Open WebUI service, parent `platform-gateway`/`envoy`/`https` | DNS and certificate come from the existing wildcard and external-dns |
| Login route | `HTTPRoute` `open-webui-auth`, same hostname, `PathPrefix /api/v1/auths/` | the longer prefix wins over the main route; every `/api/v1/auths/*` endpoint matches, including the session check on page load |
| Traffic policies | `BackendTrafficPolicy` on each route | main route: `timeout.http.requestTimeout: 1200s`, the same bound as the GPU route: in on-demand mode a request can wait up to 600 s for the GPU to start, then generate for up to 600 s; login route: local rate limit 20 req/s plus the same timeout |

Why `chat` and not `llm`: either works (gpu-on-demand has its own
ServiceAccount and no NetworkPolicy is active). A separate namespace keeps
the public app apart from the controller and its state.

Why no volume: Hetzner volumes have no snapshots and are deleted on
`make down`; only `lab-postgres` survives a rebuild (barman, daily 03:00,
2-day retention). This version stores no uploads, so `DATA_DIR` on the
container's own filesystem is enough.

The rate limit covers the login path only. On the whole route it would
return 429 for part of a cold page load, which fetches dozens of assets in
parallel. It is per Envoy instance, not per client: it slows a password
guessing script, it does not stop one.

### Authorization

- Local accounts only. `ENABLE_SIGNUP=false` from the first start; the admin
  creates every household account in the admin panel.
- `WEBUI_ADMIN_EMAIL` and `WEBUI_ADMIN_PASSWORD` come from SSM through the
  normal secret path. Open WebUI reads them only when the database has no
  users. Later password changes happen in the admin panel; a new SSM value
  does not change an existing password.
- After a cluster rebuild the database is restored from the last daily
  backup, so the password is the last one in use before that backup.
- Lost admin password: set a new SSM value, drop the `openwebui` database,
  restart Open WebUI. This deletes every chat and account.

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
| `DEFAULT_MODELS` | `qwen3.8-27b` | the only model; nobody has to pick |
| `TASK_MODEL_EXTERNAL` | `qwen3.8-27b` | background tasks use the same model |
| `ENABLE_TITLE_GENERATION` | `true` | required; one short call per new chat |
| `ENABLE_SEARCH_QUERY_GENERATION` | `true` | required; web search needs a query |
| `ENABLE_TAGS_GENERATION`, `ENABLE_FOLLOW_UP_GENERATION`, `ENABLE_AUTOCOMPLETE_GENERATION` | `false` | each is a full thinking-model call that queues behind real chats on a one-slot GPU |
| `ENABLE_WEB_SEARCH` | `true` | required |
| `WEB_SEARCH_ENGINE` | `searxng` | |
| `SEARXNG_QUERY_URL` | `http://searxng.chat.svc:8080/search?q=<query>` | |
| `WEB_SEARCH_RESULT_COUNT` | `3` | |
| `BYPASS_WEB_SEARCH_EMBEDDING_AND_RETRIEVAL` | `true` | results go to the model directly; no embedding model runs in the pod |
| `RAG_EMBEDDING_ENGINE` | `openai` | the local SentenceTransformers model is never loaded (it costs 500–800 MB); nothing calls it, because uploads are off and search bypasses embedding |
| `USER_PERMISSIONS_CHAT_FILE_UPLOAD` | `false` | no volume; a file over 1 MiB gets 413 from the gateway; admins bypass user permissions |
| `ENABLE_API_KEYS`, `USER_PERMISSIONS_FEATURES_API_KEYS` | `false` | one static key reaches the gateway |
| `GLOBAL_LOG_LEVEL` | `INFO` | `DEBUG` logs request bodies, and Alloy ships every pod log to Loki |

All names were checked in v0.13.4's `config.py` and `env.py` on 2026-11-23;
the tag `v0.13.4-slim` exists on GHCR. Slim images exist since v0.6.26:
about 200 MB against about 1.6 GB, with no bundled models.

The worker runs llama-server with `--reasoning-format deepseek`, so the
model's thinking arrives in `reasoning_content` and Open WebUI shows it as
a collapsible block.

When the GPU is stopped the gateway answers 503 `backend_unavailable` with
"GPU backend is stopped; start it with `vk-llm start`". Open WebUI shows that
text. Only the operator starts the GPU until CORE-220 ships. With CORE-220,
Open WebUI's background calls (title, search query) can also start the GPU.

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

The template MUST set `mergePolicy: Merge`. With a template and the default
policy, ESO 2.9 writes only the templated keys, so `management-tokens` and
`worker-seed` vanish and `vk-llm stop` and gpu-control stop working.

```yaml
target:
  template:
    mergePolicy: Merge
    data:
      client-keys: |
        {{ .api_token }}
        {{ .chat_inference_key }}
```

### Gateway adjustment (this repository)

`gitops/values.yaml` and the child `Application`'s `valuesObject` set:

- `config.gateway.maxTokensCap: 16384`. Open WebUI usually sends no
  `max_tokens`, so the gateway injects the cap; at 8192 a long reasoning
  answer can be cut off.
- `config.gateway.rateLimitPerKeyRPM: 100`. Every household member shares
  Open WebUI's key, and one message can take three calls (answer, search
  query, title) plus `/v1/models` on page load. Over the limit the gateway
  answers 429 with `Retry-After`. The value applies to every inference key.

The 1 MiB request limit stays: chat without uploads stays far below it.

### Capacity

Sized for 1–2 concurrent users: at that load the idle baseline is almost
all of the memory, and the model work runs on the GPU.

| Pod | CPU request | Memory request | Memory limit | Basis |
|---|---|---|---|---|
| Open WebUI | 25m | 256 Mi | 768 Mi | slim image, no local embedding model, one uvicorn worker; an optimized install idles near 200 MB |
| SearXNG | 10m | 128 Mi | 256 Mi | one Granian worker; the image default of 5 workers idles near 500 MiB |

No CPU limit (repository rule). Total requests 384 Mi and 35m. The limits
cover startup and a search; an OOM kill at the limit restarts the pod with
no data loss, because nothing lives on its filesystem. Requests are pinned
to measured use after one week. Neither pod has a volume, so both can run
on a node in any location.

On 2026-11-23 the only worker (`cx33`, fsn1) used 88% of its memory, and a
create of `cx33` and `cx43` in fsn1 returned `resource_unavailable`. HETZ-176
in `vk-lab-platform` (PR #116, separate session) gives the autoscaler one
group per location so a node can come from nbg1 or hel1. CORE-230 is
deployed only after that session confirms room on the cluster.

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
- R10. Tags, follow-up and autocomplete generation MUST be off; title,
  search-query generation and web search MUST be on.
- R11. The gateway's output cap MUST be 16384 tokens.
- R12. The platform's render check and aws golden MUST include the new
  objects.
- R13. Both chat routes MUST carry a 1200 s request timeout; only the login
  route (`/api/v1/auths/`) MUST carry a 20 req/s local rate limit.
- R14. File uploads and per-user API keys MUST be off.
- R15. Prompts and completions MUST NOT appear in logs; the log level MUST
  be `INFO`.
- R16. The gateway's per-key rate limit MUST be 100 requests per minute.
- R17. The `client-keys` template MUST keep `management-tokens` and
  `worker-seed` in the Secret.

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
   template with `mergePolicy: Merge`. Check: render check green, golden
   updated; the rendered `vk-llm-api-token` keeps `management-tokens` and
   `worker-seed`.
5. This repository: `maxTokensCap` 16384, `rateLimitPerKeyRPM` 100,
   requirements §13. Check: `make gitops-check specs-check`.
6. Wait for HETZ-176's confirmation of room on the cluster.
7. Operator: apply `persistent-up` and `bootstrap-up`, merge both PRs.
8. Acceptance without the GPU: AC1, AC2 (browser), AC5, AC6, AC8, AC10.
9. One GPU session of 60 minutes or less (`vk-llm start` … `vk-llm stop`,
   cost at most $0.80): AC2 (Conduit), AC3, AC4, AC7, AC9, AC11. Paste the
   evidence below.

## Tests / acceptance criteria

- AC1. `https://chat.<fqdn>` shows the Open WebUI login page with a valid
  certificate; a self-registration attempt is refused. (R3, R9)
- AC2. The admin signs in, creates one account; that person signs in from
  Conduit on a phone and from a laptop browser. (R3)
- AC3. With the GPU `READY`, an answer streams and its thinking shows as a
  collapsible block; the chat gets a title; no tags or follow-up suggestions
  appear; the gateway log shows Open WebUI's key id. (R6, R10)
- AC4. A question about current news returns an answer with sources. No
  `HTTPRoute` in the cluster references the SearXNG service, and
  `searxng.<fqdn>` does not resolve. (R5, R10)
- AC5. With the GPU stopped, the chat shows the `backend_unavailable` text,
  not a blank answer.
- AC6. After the Open WebUI pod is deleted and recreated, every account and
  chat is still present. (R2)
- AC7. A request without `max_tokens` is clamped to 16384 (gateway log
  `max_tokens_clamped`). (R11)
- AC8. Argo shows the new Applications `Synced`/`Healthy`; the render check
  passes; `vk-llm status` still works with the management token. (R1, R12,
  R17)
- AC9. After a chat that contains a unique word, a Loki query for that word
  over the `chat` and `llm` namespaces returns no lines. (R15)
- AC10. The upload control is absent or refused; the settings page offers
  no API key; more than 20 req/s to `/api/v1/auths/signin` gets 429 from
  Envoy; a cold load of the start page in a private window shows no 429 in
  the browser's network tab. (R13, R14)
- AC11. A normal chat with web search gets no 429 from the gateway. (R16)

## Definition of done

- [ ] AC1–AC11 pass on the Hetzner lab.
- [ ] Both repositories' PRs merged; CI green.
- [ ] Requests pinned to measured use after one week (follow-up commit).
- [ ] Status flipped to DONE; folder renamed `230-D-open-webui-chat-front-end`.
- [ ] Evidence section filled.

## Open questions

| Question | Why it matters | Default until checked |
|---|---|---|
| Exact chart keys for the subchart and Redis toggles in 18.4.0 | R1 | read `helm show values` at implementation |
| Does Open WebUI's live connection (socket.io) work through Envoy Gateway v1.2.1 without extra settings? | streaming in the UI | assume yes; AC3 proves it |
| Does Conduit work with Open WebUI v0.13.4? | AC2 | assume yes |
| Measured memory of Open WebUI slim with no embedding model | capacity | 256 Mi request, 768 Mi limit |

## Evidence

None yet.
