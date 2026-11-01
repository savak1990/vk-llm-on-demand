---
id: "CORE-160"
status: "DRAFT"
updated: "2026-11-01"
depends_on: ["CORE-055"]
adrs: ["0012", "0011", "0013"]
---

# 160 — GitHub Actions remote control

**Status note:** DRAFT, written 2026-11-01 before any code exists.

**Complexity:** S — one manually triggered workflow, `curl` and `jq`
against an API CORE-055 already contracts; no new server-side behaviour.
**Risk:** low — the workflow can only start or stop the one installation
it is pointed at, and every call it makes is already idempotent and
budget-gated by CORE-050/100; the only sharp edge is leaking the token.
**Estimated effort:** half a session (2–4 h)
**Recommended model:** standard — a bash script and a workflow file.
**Depends on:** [CORE-055](../055-P-management-api/spec.md) (the four
`/api/v1/gpu/*` routes and their status/error documents)

## Outcome

From the GitHub Actions tab, a maintainer runs `gpu-control` with
`action: start`, `status`, or `stop` and sees the GPU's state change in the
run log and job summary, with the same lifecycle semantics `vk-llm` gives
on a laptop: `start` waits for `ready: true` or 20 minutes; `status` prints
one snapshot; `stop` drains and waits for `STOPPED`. No GitHub Actions run
ever extends the idle deadline (ADR 0011 §2), so a GPU started from Actions
idles out on schedule exactly like one started from the CLI (FR-7.8,
FR-7.9).

## Scope

- `.github/workflows/gpu-control.yml`: `workflow_dispatch`, one job, one
  step that runs the script below.
- `scripts/gpu-control.sh`: the `start`/`status`/`stop` logic, `curl` +
  `jq` against the management API, polling, job summary, exit codes.
- Repository setup doc (secret, variable, access options).

Excludes: the management API itself and its status/cost/error documents
(CORE-055); the GPU state machine, drain and `force` semantics (CORE-050);
the `vk-llm` CLI binary and its release (CORE-150, ADR 0013 §3) — this
workflow deliberately does not download it (ADR 0012 §5, "Alternatives
considered"); the repo-wide `actionlint`/`gitleaks`/`govulncheck` CI jobs
and `SECURITY.md` (CORE-170); the scripted acceptance run against a real
instance (CORE-140); on-demand mode (ADR 0010 §4, not reachable from this
workflow).

## Design and contracts

### Workflow shape (ADR 0012 §5)

```yaml
name: gpu-control
on:
  workflow_dispatch:
    inputs:
      action:
        description: What to do to the GPU backend
        type: choice
        options: [start, status, stop]
        default: status
permissions: {}
concurrency:
  group: gpu-control
  cancel-in-progress: false
jobs:
  control:
    runs-on: ubuntu-latest
    timeout-minutes: 25
    permissions:
      contents: read
    env:
      ACTION: ${{ inputs.action }}
      VK_LLM_TOKEN: ${{ secrets.VK_LLM_TOKEN }}
      ENDPOINT: ${{ vars.VK_LLM_API_ENDPOINT }}
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with:
          persist-credentials: false
      - run: scripts/gpu-control.sh
```

The committed script needs `actions/checkout` to exist on the runner, so
this is the one action the job uses, and it is the one place ADR 0013 §1's
pinning rule bites: SHA-pinned with a version comment, `persist-credentials:
false` (no token left on disk after checkout), job-level `permissions:
contents: read` (nothing else — no write scope anywhere in this
workflow). The SHA and version are the pin `vk-ahorro` uses for
`actions/checkout@v7.0.1` today — see
`/Users/margo/Proj/Lab/vk-ahorro/.github/workflows/release.yml` line 36
for the same `uses:` line and `persist-credentials: false`
convention, and `docs/research.md` §6 ("copy the SHAs from the source
files at implementation time, not from this page") for re-verifying it
when this spec is implemented. `permissions: {}` at workflow level,
`concurrency.cancel-in-progress: false` so a `stop` dispatched mid-`start`
queues instead of racing it, and `timeout-minutes: 25` follow the same
`workflow_dispatch` shape as `release.yml`'s `permissions: {}` / scoped
per-job `permissions:` / non-cancelling `concurrency.group` (same file,
lines 13–30), adapted here to a single job instead of release.yml's chain.
`action` reaches the script only through `env.ACTION`, never interpolated
into a `run:` string (NFR-8.8); same for `VK_LLM_TOKEN` and `ENDPOINT`.
`::add-mask::` is not added for the token: GitHub Actions already masks
any literal value of a secret (`secrets.VK_LLM_TOKEN`) that reaches a log
line, so an explicit mask would only duplicate that for a value which, by
construction, this script never prints (next section). `ENDPOINT` is a
*variable*, not a secret, so GitHub does not auto-mask it; the script's
first action is `echo "::add-mask::${ENDPOINT#https://}"` so a `curl`
connection failure (e.g. "Could not resolve host") never prints the
installation's hostname into a public run log (NFR-8.6, architecture §6).

### `scripts/gpu-control.sh` contract

```
set -euo pipefail

: "${ACTION:?}" "${VK_LLM_TOKEN:?}" "${ENDPOINT:?}"
: "${POLL_INTERVAL:=20}"     # seconds (ADR 0012 §5); overridable for tests
: "${START_MAX_WAIT:=1200}"  # 20 min (ADR 0012 §5)
: "${STOP_MAX_WAIT:=300}"    # 5 min, mirrors `vk-llm stop --timeout` (ADR 0012 §4)

req() {                      # req METHOD PATH [BODY] -> curl, auth via stdin
  local method=$1 path=$2 body=${3-}
  local extra=()
  [[ -n "$body" ]] && extra=(-d "$body")
  printf 'Authorization: Bearer %s\n' "$VK_LLM_TOKEN" |
    curl -sS --max-time 30 --fail-with-body -X "$method" -H @- \
         -H 'Content-Type: application/json' \
         "${extra[@]}" \
         "$ENDPOINT/api/v1/gpu/$path"
}
```

The three poll constants are `${VAR:=default}`, not plain assignments, so
a test can set `POLL_INTERVAL=1` in its own environment before invoking
the script without editing it (AC4–AC6 use this). `--fail-with-body`
(curl ≥ 7.76; see Open questions) replaces plain `-f`: a non-2xx response
still makes `req` exit non-zero, but the error document's body — the one
place `jq` needs to read `.error.code`/`.error.reason` from — reaches
stdout first instead of being discarded, which plain `-f` would do. `extra`
is a bash array so an empty `$body` disappears from the argument list
cleanly rather than through unquoted word-splitting.

`-H @-` reads the single `Authorization` header from stdin, piped in by
`printf`; the request body (`-d`, empty or `{}`) is a plain argument, so
there is no stdin conflict. The token is therefore never a literal `argv`
element of the `curl` process and never appears in a process listing or an
echoed command — the choice `req` makes between the brief's two options
(`-H @-` vs. `--config -`): `-H @-` composes more simply with the `-X`/`-d`
flags the three actions already need, while `--config -` would require the
method, URL and body to move into the same stdin stream.

Every call's JSON response is parsed with `jq`; the script never writes
the raw response body or the response headers to stdout, stderr or the
job summary — only named fields it has explicitly selected, both on
success and on a `4xx`/`5xx` error body (`.error.code`, `.error.reason`).
`curl --fail-with-body` makes any non-2xx response exit `req` non-zero
after printing the body (the brief's "failure on non-2xx"); the caller
reads `.error.code`/`.error.reason` from that body before re-raising
under `set -e`, so the case arm still exits non-zero overall.

| `$ACTION` | Calls | Poll until | Prints |
|---|---|---|---|
| `start` | `POST start`, then `GET status` every `POLL_INTERVAL` | `.ready == true` or `START_MAX_WAIT` elapsed | one line per distinct `.state` seen, in order; never the full document |
| `status` | `GET status` once | — | `provider`, `state`, `instance.gpu`, `model.alias`, `ready`, `uptimeSeconds`, `instance.priceUSDPerHour`, `cost.estimatedInstanceUSD`, `cost.estimatedMonthUSD`, `secondsUntilIdleShutdown` — the same fields `vk-llm status` shows (ADR 0012 §4) |
| `stop` | `POST stop`, then `GET status` every `POLL_INTERVAL` | `.state == "STOPPED"` or `STOP_MAX_WAIT` elapsed | one line per distinct `.state` seen |

A timed-out poll (ready never reached / `STOPPED` never reached) exits 1
with the last known `.state` and `.unavailable`. `secondsUntilIdleShutdown`
is `null` on every state but `READY`; the script renders `null` as `n/a`.
`$GITHUB_STEP_SUMMARY` gets one line on completion: `state=<final state>
secondsUntilIdleShutdown=<value or n/a>`, for every action including
`status`.

### Credentials and access (ADR 0012 §3, §5, §6)

| Item | Kind | Set by |
|---|---|---|
| `VK_LLM_TOKEN` | repository secret | a `management-tokens` entry (ADR 0012 §3) issued for this workflow, distinct from the one a human puts in `~/.config/vk-llm/config.yaml`, so leaking one does not force rotating the other |
| `VK_LLM_API_ENDPOINT` | repository variable | `https://llm.<fqdn>` (placeholder; never the real domain in Git, ADR 0013 §1) |

Access options, documented, not all defaults:

| Option | When | Note |
|---|---|---|
| HTTPS via Envoy (default) | the installation is reachable from the internet | token is the only gate; rate-limited and budget-capped the same as every other management client |
| VPN | the installation is kept off the public internet | the workflow needs a self-hosted runner reachable over the VPN (next row) |
| Self-hosted runner | `ENDPOINT` is not publicly routable | `runs-on` changes; `permissions: {}` and the token/variable split are unchanged |

### What this workflow cannot do

It cannot touch `lastInferenceFinishedAt`, `readyAt` or `idleDeadline`:
every call it makes (`start`, `status`, `stop`) is on ADR 0011 §2's list of
calls that never count as activity, because the management API's own
`Controller` dependency has no `Admit`/`RequestEnd` method (CORE-055
"Controller dependency"). It cannot hold or discover a provider credential
— it never talks to anything but the management API (ADR 0012's
"Alternatives considered": CLI/workflow credentials are rejected
repo-wide).

## Requirements

- R1. `gpu-control.yml` MUST declare `permissions: {}` at workflow level; the job's `permissions` MUST be exactly `contents: read` and no other scope. (source: NFR-8.8, ADR 0013 §4)
- R2. The `action` input MUST be `type: choice` with options exactly `start`, `status`, `stop` and default `status`, and MUST reach the script only through `env:`, never string-interpolated into a `run:` block. (source: ADR 0012 §5, NFR-8.8)
- R3. The job MUST declare `concurrency: {group: gpu-control, cancel-in-progress: false}` and `timeout-minutes: 25`. (source: ADR 0012 §5, FR-7.9)
- R4. `scripts/gpu-control.sh` MUST pass `shellcheck -s bash` with zero findings and MUST quote every variable expansion. (source: task brief; ADR 0013 §1 pattern)
- R5. The script MUST send `VK_LLM_TOKEN` to `curl` only via a header read from stdin (`-H @-`), never as a literal command-line argument. (source: ADR 0009 §5, C-16)
- R6. The script MUST NOT print the raw JSON response body or any response header, on success or on error; it MUST print only the named fields listed in the action table. (source: ADR 0012 §1, C-18)
- R7. A non-2xx response from any call MUST make the script exit non-zero without retrying the call. (source: task brief)
- R8. `start` MUST poll `GET status` every 20 s until `.ready == true` or 1200 s have elapsed; `stop` MUST poll every 20 s until `.state == "STOPPED"` or 300 s have elapsed; `status` MUST make exactly one call. (source: ADR 0012 §5; §4 for the stop bound)
- R9. No call this script makes MUST write `lastInferenceFinishedAt`, `readyAt`, or `idleDeadline`; the GPU's `idleDeadline` MUST be unchanged across any number of `status` or `stop`-on-already-stopped runs. (source: ADR 0011 §2, FR-3.5, FR-7.9)
- R10. The job summary MUST contain a line with the final `state` and `secondsUntilIdleShutdown` (or `n/a`) for every action. (source: task brief)
- R11. The workflow MUST use exactly one GitHub Action, `actions/checkout`, SHA-pinned with a version comment and `persist-credentials: false`; every other step MUST be `run:`. (source: ADR 0013 §1, ADR 0012 §5 "Alternatives considered")
- R12. The script MUST mask `ENDPOINT`'s hostname via `::add-mask::` before making any call, so a `curl` failure message cannot print the installation's domain into the run log. (source: NFR-8.6, architecture §6)

## Implementation steps

1. Write `.github/workflows/gpu-control.yml` per the Workflow shape. Check: `actionlint` exits 0.
2. Write `scripts/gpu-control.sh`'s `req` helper and argument validation (`: "${ACTION:?}"` style). Check: `shellcheck -s bash` exits 0; running with an unset `VK_LLM_TOKEN` exits non-zero before any network call.
3. Implement `status`: one `req GET status`, `jq` field selection, summary line. Check: against a scripted fake server (tier 2), stdout matches the fixed field set, exit 0.
4. Implement `start`: `req POST start`, then the poll loop. Check: fake server returns `ALLOCATING`, `STARTING`, `LOADING_MODEL`, `READY` across four successive `GET status` calls (≤ 80 s wall time at the real 20 s interval); script prints four distinct state lines in order and exits 0.
5. Implement `stop`: `req POST stop`, then the poll loop to `STOPPED`. Check: fake server returns `DRAINING`, `DELETING`, `STOPPED` across three calls; exit 0; summary shows `secondsUntilIdleShutdown=n/a`.
6. Add the 503/401 failure paths. Check: fake server returns `503 {"error":{"code":"gpu_unavailable","reason":"no_offer", ...}}` on `start`'s first call; script exits 1, stderr contains `gpu_unavailable` and `no_offer`, and does not contain the substring `{"error"` (proving the raw body was not dumped).
7. Wire `scripts/gpu-control.sh` into the existing CI lint job's shellcheck step (CORE-010, the same wiring CORE-070 used for `onstart.sh`) and confirm the repo-wide `actionlint` glob (CORE-170) already covers `.github/workflows/gpu-control.yml`. Check: CI green on a PR touching only this file.

## Tests / acceptance criteria

Tier 1/2 use the shared fake management server (`docs/testing-strategy.md`,
also used by CORE-150); no real provider or network beyond `httptest`.

- AC1. Lint (R4, NFR-8.8). `actionlint .github/workflows/gpu-control.yml` exits 0; `shellcheck -s bash scripts/gpu-control.sh` exits 0, 0 findings.
- AC2. Static shape (R1, R2, R3, R11). Parse the workflow YAML: top-level `permissions` equals `{}`; job `permissions` equals `{contents: read}`; `concurrency` equals `{group: gpu-control, cancel-in-progress: false}`; `timeout-minutes` equals 25; the `action` input has `type: choice`, `options: [start, status, stop]`, `default: status`; exactly one `uses:` key, matching `actions/checkout@[0-9a-f]{40}`, with `persist-credentials: false`.
- AC3. `status` (FR-7.8). Fake server returns a fixed READY document (`price 0.71`, `uptime 1830`, `idleDeadline` = now+1440s). Action: `ACTION=status scripts/gpu-control.sh`. Expected: exit 0; stdout contains all ten fields of the status row with the fixture's values; `secondsUntilIdleShutdown=1440` in the job summary; fake server call count: 1.
- AC4. `start` polling (FR-7.8, FR-3.5, ADR 0011 §2). Fake server's `GET status` returns `ALLOCATING`, `STARTING`, `LOADING_MODEL`, `READY` on its 1st–4th call respectively (state fixed to `READY` thereafter), each with a fixed `idleDeadline`. Action: `ACTION=start POLL_INTERVAL=1 scripts/gpu-control.sh`. Expected: exit 0; stdout shows exactly those four states, each once, in order; wall time ≤ 10 s at `POLL_INTERVAL=1`; `idleDeadline` byte-identical across all 4 `GET` calls recorded by the fake server.
- AC5. `stop` polling (R8). Fake server's `GET status` returns `DRAINING`, `DELETING`, `STOPPED` on its 1st–3rd call. Action: `ACTION=stop POLL_INTERVAL=1 scripts/gpu-control.sh`. Expected: exit 0; three state lines; summary line `state=STOPPED secondsUntilIdleShutdown=n/a`.
- AC6. Timeout (R8). Fake server's `GET status` always returns `ALLOCATING`. Action: `ACTION=start POLL_INTERVAL=1 START_MAX_WAIT=2 scripts/gpu-control.sh`. Expected: exit 1 after 2 polls (~2 s); last printed state is `ALLOCATING`.
- AC7. Start denial (R6, R7). Fake server's `POST start` returns `503 {"error":{"code":"gpu_unavailable","reason":"no_offer","message":"…"}}`. Action: `ACTION=start`. Expected: exit 1; 0 `GET status` calls made (the denial is on `POST start` itself); stderr contains `gpu_unavailable` and `no_offer` (read from the body `--fail-with-body` preserved on disk); stderr does not contain the literal substring `{"error"` (the curated fields are printed, not the raw body).
- AC8. Token never on argv (R5). Wrap `curl` with a recording stub on `PATH`. Action: run AC3 and AC7. Expected: in every recorded invocation, no argv element equals or contains the token value; the token appears only on the stub's captured stdin.
- AC9. Deadline isolation (R9, FR-3.5, FR-7.9). Run AC3 ten times and AC5's stop-when-already-`STOPPED` case ten times against one fake server instance that persists `idleDeadline`. Expected: `idleDeadline` unchanged across all 20 calls; fake server records 0 writes to `lastInferenceFinishedAt`.
- AC10. Concurrency (FR-7.9; static half of the requirements.md "Concurrency group test"). Covered by AC2's `concurrency` assertion; the dynamic half (two real dispatches, one queuing behind the other) is a documented manual check, run once as part of CORE-140's acceptance pass, not a tier 1/2 check.
- AC11. Endpoint masking (R12, NFR-8.6). `ENDPOINT=https://llm.lab.example.com` and an unreachable fake server (connection refused). Action: `ACTION=status`. Expected: exit 1; the captured log contains `::add-mask::llm.lab.example.com` before the first `curl` invocation; the literal string `llm.lab.example.com` does not otherwise appear unmasked in stdout or stderr.

## Definition of done

- [ ] AC1–AC9 and AC11 pass in CI (tier 1/2).
- [ ] AC10's static half passes in CI; its manual half, and one real `ACTION=start` dispatch against the platform installation (FR-7.8's "acceptance run for start"), are recorded once in CORE-140's evidence.
- [ ] `make specs-check` passes.
- [ ] `scripts/gpu-control.sh` is in CORE-010's shellcheck lint step; `.github/workflows/gpu-control.yml` is covered by the repo-wide `actionlint` job.
- [ ] Repository setup doc (secret, variable, access options) is published alongside the workflow.
- [ ] Status flipped to DONE in the same PR; folder renamed `160-D-github-actions-remote-control`.
- [ ] Evidence section filled.

## Open questions

| Question | Why it matters | Settled by | Default |
|---|---|---|---|
| Exact shared fixture/package for the "fake management server" (`docs/testing-strategy.md` tier 2) that both this spec and CORE-150 use | avoids two incompatible fakes for the same contract | whichever of CORE-150/160 lands first names the Go package and its Make target; the other imports it | a small `httptest`-backed server, scripted per-call, started by one Make target before the shellcheck/CI step runs |
| Do `ubuntu-latest` runners still ship `curl` and `jq` with no install step at implementation time | the script has no fallback if either is missing | verified against `actions/runner-images`'s current manifest when CORE-160 is implemented (not in `docs/research.md`) | add a defensive `command -v jq >/dev/null \|\| { sudo apt-get update -qq && sudo apt-get install -y -qq jq; }` guard; `curl` ships with every runner image to date and needs no guard |
| Does the `ubuntu-latest` runner's bundled `curl` support `--fail-with-body` (added curl 7.76, 2021) | `req`'s error-body path depends on it | verified the same way, same runner-image check, when CORE-160 is implemented (not in `docs/research.md`) | assume yes (every `ubuntu-latest` image since has shipped a newer curl); if a future minimal image regresses, fall back to `-fsS` plus a second unauthenticated-looking retry without `-f` only to read the error body |
| 5-minute stop poll cap | ADR 0012 §5 gives `start`'s 20 min cap explicitly but says nothing about a `stop` cap | this spec's default stands unless ADR 0012 is amended | 300 s, mirroring `vk-llm stop --timeout`'s default (ADR 0012 §4) |
| Rendering of `secondsUntilIdleShutdown: null` in script/job-summary output | not specified by any ADR; CORE-150's CLI makes an equivalent choice independently | align with CORE-150 in review if its rendering differs | `n/a` |

## Evidence

None yet.
