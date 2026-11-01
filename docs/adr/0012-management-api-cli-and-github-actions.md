# ADR 0012: A separate management API, with `vk-llm` and GitHub Actions as thin clients

## Status

Accepted (2026-11-01). Replaces the `/admin/v1` endpoints of ADR 0007 §3.
Edits ADR 0009 (Proposed) in place for the token names.

## Context

Explicit provisioning (ADR 0010) needs a way to start, observe and stop
the GPU that is not the inference API. The brief asks for a versioned,
authenticated management API, a standalone `vk-llm` CLI that never holds
provider credentials, a manually triggered GitHub Actions workflow with
identical behaviour, and strict separation between inference and
management credentials.

## Decision

### 1. Management API

Served by the same binary on the same port as `/v1`, under `/api/v1/gpu`,
behind Envoy with the platform's TLS. JSON bodies, versioned path, stable
error shape `{"error": {"code", "message", "reason"?}}`.

| Method and path | Result | Codes |
|---|---|---|
| `POST /api/v1/gpu/start` | sets `desired=RUNNING`; returns the status document | 200 `already_running` (Ready); 202 `accepted` (Allocating…LoadingModel, or Draining with the new desired recorded, a new generation provisions after the destroy confirms); 503 `gpu_unavailable` with `reason: budget_exhausted \| attempts_exhausted \| no_offer \| balance_low \| balance_unknown` |
| `GET /api/v1/gpu/status` | status document | 200 |
| `POST /api/v1/gpu/stop` | sets `desired=STOPPED`; begins draining at once (ADR 0011 §3); `{"force": true}` reserved | 200 `already_stopped`; 202 `accepted` |
| `GET /api/v1/gpu/cost` | estimated instance and month spend, budget, hourly price, uptime; marked as estimates | 200 |

Status document:

```json
{
  "provider": "vastai", "state": "READY", "desired": "RUNNING", "generation": 7,
  "instance": {"gpu": "L40S", "machineId": 12345, "priceUSDPerHour": 0.71,
               "createdAt": "…", "readyAt": "…"},
  "model": {"alias": "qwen3.6-35b-a3b", "contextSize": 32768},
  "ready": true, "uptimeSeconds": 1830,
  "lastInferenceFinishedAt": "…", "idleDeadline": "…", "secondsUntilIdleShutdown": 1440,
  "cost": {"estimatedInstanceUSD": 0.36, "estimatedMonthUSD": 3.42, "budgetMonthUSD": 30},
  "unavailable": "", "nextCreateAt": null,
  "installationId": "4f2c1a9b0e77", "labelPrefix": "vkllm:4f2c1a9b0e77:"
}
```

Start and stop return the status document plus a top-level
`"result": "accepted" | "already_running" | "already_stopped"` field, so the
CLI can print a sentence without inspecting codes. Start returns within
200 ms: it records the desired state; the offer search runs asynchronously
and a `no_offer` outcome appears in `unavailable` on the next status. A 503
`gpu_unavailable` on start is returned only when the snapshot already holds
that reason from a recent attempt (within the attempt backoff) or when the
budget or balance gate denies at call time. Both are idempotent.
No management call touches the inactivity deadline (ADR 0011 §2).

Mapping from the superseded admin API: `/admin/v1/warmup` → `start`;
`/admin/v1/status` → `status`; `/admin/v1/scale {desired:0}` → `stop`;
`/admin/v1/leases*` → removed; `cost` is new.

### 2. Controller interface behind the API (binding for specs 050, 055, 060)

```go
Start(ctx context.Context) (Snapshot, error)              // idempotent
Stop(ctx context.Context, force bool) (Snapshot, error)   // idempotent; force reserved
Status() Snapshot
Admit(gen int64) (http.RoundTripper, error)               // ErrBackendUnavailable | ErrDraining
RequestEnd(gen int64)                                     // sets lastInferenceFinishedAt, recomputes idleDeadline
ObserveFirstToken(gen int64, d time.Duration)
ReportUpstreamFailure(gen int64)
```

`Demand` exists only for on-demand mode (ADR 0010 §4).

### 3. Credentials

Secret `<release>-api-keys` holds three keys: `client-keys` (inference, one
per line), `management-tokens` (one per line), `worker-seed` (32 random
bytes; the per-instance worker API key is derived from it, ADR 0006 §4).
An inference key gets 403 on `/api/v1/*`; a management token gets 403 on
`/v1/*`. Both are compared in constant time and never logged. Rotation is
per list.

### 4. `vk-llm` CLI

A standalone Go binary (`cmd/vk-llm`, stdlib `flag` subcommands) that
speaks only to the management API.

| Command | Behaviour |
|---|---|
| `vk-llm start` | POST start; waits for `ready: true` by default, printing state changes; `--detach`; `--timeout` (default 15m); exit 1 on failure or timeout; never changes the deadline of a running GPU |
| `vk-llm status` | prints provider, state, GPU, model, readiness, price, uptime, estimated cost, last inference, seconds until idle shutdown; `--json` |
| `vk-llm stop` | POST stop; waits for `STOPPED` by default; `--detach`; exit 1 if the destroy does not confirm within `--timeout` (5m) |

Configuration precedence: flags > environment > file
`~/.config/vk-llm/config.yaml`. The file holds `api.endpoint` and
`api.tokenEnv` (default `VK_LLM_TOKEN`); it never holds a token.
Environment: `VK_LLM_API_ENDPOINT`, `VK_LLM_TOKEN`. Binaries for
darwin/linux × amd64/arm64 are published on GitHub Releases with checksums
(ADR 0013).

### 5. GitHub Actions remote control

`.github/workflows/gpu-control.yml`, `workflow_dispatch` with a `choice`
input `action: start | status | stop`. The workflow is `curl` + `jq`
against the management API: `permissions: {}`, concurrency group
`gpu-control` without cancel, `timeout-minutes: 25`, token from the
repository secret `VK_LLM_TOKEN`, endpoint from the repository
variable `VK_LLM_API_ENDPOINT`, the input passed through `env:` and never
interpolated into shell. `start` polls status every 20 s until ready or
timeout. Polling is status only, so the GPU started from Actions idles out
exactly like one started from the CLI.

### 6. Exposure

Default: HTTPS through the platform Envoy on the same host as the
inference API, protected by the management token. VPN or a self-hosted
runner are documented alternatives, not defaults. Port 9090 (health,
metrics) is never routed.

## Alternatives considered

- **Keep `/admin/v1` with one admin key.** The brief requires separate
  credentials and a versioned contract; renaming now is cheaper than later.
- **A separate management Deployment or port.** One binary and one port
  keep the chart and Envoy route simple; path-level authorization gives
  the separation. Revisit if the management API needs a different network
  exposure.
- **CLI with cobra/viper.** Three subcommands and one YAML file do not
  justify two dependencies; `flag` plus `gopkg.in/yaml.v3`.
- **GitHub Actions downloading the `vk-llm` binary.** Adds a release
  dependency and a checksum step for two HTTP calls; `curl` is enough.
- **Provider credentials in the CLI.** Rejected: the brief forbids it, and
  it would bypass budget and reconcile.

## Rationale

One API, three clients. The controller stays the only component that
touches the provider; every client is a thin, idempotent, pollable
consumer.

## Consequences

- Spec 055 (new) owns the API; spec 060 keeps the inference surface only;
  spec 150 (CLI) and 160 (GitHub Actions) are new; spec 110 renames the
  token keys; spec 080 adds the management token to the Secret contract.
- `/admin/v1` disappears from every spec and doc.
- The README gains the three-command workflow and the OpenCode provider
  snippet.

## Risks

- A leaked management token starts GPUs. Mitigation: budget caps, rate
  limiting on `/api/v1`, per-list rotation, no token in URLs or logs.
- The GitHub workflow runs on public runners and reaches a public endpoint.
  Mitigation: TLS, token in Secrets, no echo of responses containing
  tokens (the API never returns them).
