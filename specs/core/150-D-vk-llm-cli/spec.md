---
id: "CORE-150"
status: "DONE"
updated: "2026-11-15"
depends_on: ["CORE-055", "CORE-120"]
adrs: ["0012", "0013", "0011"]
---

# 150 — `vk-llm` CLI

**Status note:** DONE. The CLI is implemented; AC1-AC12 and `make
specs-check` all pass, confirmed in real CI on PR #29 (run 43734025623
and 43736491698). AC13 (a real release dispatch) stays open as a known,
accepted gap: the user chose to flip this spec to DONE now rather than
wait on a real `release.yml` dispatch, since `.goreleaser.yaml` is
already confirmed correct and the CLI itself is fully built and tested.
See Evidence.

**Complexity:** S
**Risk:** medium — the CLI holds a management token that can rent a GPU, so token handling and exit codes guard money
**Estimated effort:** one session (3–5 h), half of it the fake-server tests
**Recommended model:** standard — a thin HTTP client over the CORE-055 contract
**Depends on:** [CORE-055](../055-D-management-api/spec.md), [CORE-120](../120-D-release-pipeline/spec.md)

## Outcome

`vk-llm start` sends one `POST /api/v1/gpu/start`, prints each state change
(`ALLOCATING → STARTING → LOADING_MODEL → READY`) as it polls status every
5 s, prints the minutes until idle shutdown, and exits 0 at `READY`, 1 on
failure or timeout. `vk-llm start` on a READY GPU prints `already running,
idle shutdown in N min`, exits 0 and moves no deadline. `vk-llm stop` exits
0 once status shows `STOPPED`. `vk-llm status` prints one table or, with
`--json`, the raw status document. The token comes only from the
environment; a config file containing a `token` key is refused with exit 2.
Binaries for darwin/linux × amd64/arm64 come from GitHub Releases with
`checksums.txt`.

## Scope

- `cmd/vk-llm`: one `package main` with `main.go`, `config.go`,
  `client.go`, `commands.go`, `render.go` and their `_test.go` files.
- Subcommands `start`, `status`, `stop`, `budget`, `rentals`, `version`; stdlib `flag`;
  config file via `gopkg.in/yaml.v3` (ADR 0012 Alternatives, architecture §2).
- Install instructions with checksum verification in `README.md`.
- Tests: tier 1 against an `httptest` fake management server; tier 2 one
  `vk-llm status` against the kind install (docs/testing-strategy.md).

Excludes: the management API itself (CORE-055); the GoReleaser job, its
config and the release matrix (CORE-120, ADR 0013 §3); `gpu-control.yml`
(CORE-160); `vk-llm cache *` and on-demand mode (out of MVP,
docs/requirements.md); a Homebrew tap (open question); `vk-llm cost` (not in
ADR 0012 §4; `status` shows the estimate).

## Design and contracts

Bound by C-7a (no client can extend the idle deadline), C-14 (placeholders
only), C-16 (tokens never logged), C-25 (stdlib first), C-27 (every call
has a timeout), C-29 (fake clock, no sleeps).

### Entry point

```go
package main

var version = "dev" // set by -ldflags "-X main.version=X.Y.Z" in the release build (CORE-120)

func main() { os.Exit(run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr, clock.Real())) }

// run is the only seam: tests pass a fake env, buffers and a fake clock.
func run(args []string, getenv func(string) string, stdout, stderr io.Writer, c clock.Clock) int
```

`clock.Clock` is the CORE-010 interface; the poll loop waits with
`c.After(5 * time.Second)` and the deadline is `c.Now() + --timeout`, so
tests drive it with `fake.Advance` and never sleep. The 5 s interval is a
package constant, not a flag.

### Commands and flags

| Command | Flags | Behaviour |
|---|---|---|
| `start` | `--detach`, `--timeout` (default `defaults.startupTimeout`, 15m), `--json` | POST start; unless detached, poll status until a terminal outcome (below) |
| `status` | `--json` | GET status; table or raw body |
| `stop` | `--detach`, `--timeout` (default 5m), `--json` | POST stop; unless detached, poll until `STOPPED` |
| `budget` | `--json` | no args: GET status; `set <amount>`: POST /api/v1/gpu/budget; table (budget view) or raw body |
| `rentals` | `--json` | GET /api/v1/gpu/rentals; table or raw body |
| `version` | none | prints `vk-llm <version>`; no config, no network |

Global flags, accepted before or after the subcommand name: `--endpoint`,
`--token-env`, `--config` (default `~/.config/vk-llm/config.yaml`). There is
no flag that takes a token value (ADR 0013 §4: no credentials in flags), no
flag that disables TLS verification, and no provider flag of any kind.

### Configuration (ADR 0012 §4)

| Setting | Flag | Environment | File key | Default |
|---|---|---|---|---|
| endpoint | `--endpoint` | `VK_LLM_API_ENDPOINT` | `api.endpoint` | none: exit 2 `no endpoint configured` |
| token variable name | `--token-env` | — | `api.tokenEnv` | `VK_LLM_TOKEN` |
| token | — | value of the variable named above | never | none: exit 2 `<name> is not set` |
| start timeout | `--timeout` | — | `defaults.startupTimeout` | `15m` |
| wait for ready/stopped | `--detach` sets false | — | `defaults.waitForReady` | `true` |

Precedence per setting: flag > environment > file > default. A missing file
is not an error. The file is decoded with `yaml.v3` `KnownFields(true)` into
a struct holding only the four keys above. A `token` key anywhere in the
file (any depth, any case) fails with exit 2 and
`config file must not contain a token; set VK_LLM_TOKEN instead`, before any
network call; an unknown key also fails with exit 2. The endpoint MUST be
`https://`, except `http://` to `localhost`, `127.0.0.1` or `::1` (tests
and `kubectl port-forward`); any other `http://` endpoint fails with exit 2.
Example file (placeholder only):

```yaml
api:
  endpoint: https://llm.example.com
  tokenEnv: VK_LLM_TOKEN
defaults:
  startupTimeout: 15m
  waitForReady: true
```

### HTTP client

- `http.Client{Timeout: 15 * time.Second}`; default transport, TLS
  verification on; `CheckRedirect` returns `http.ErrUseLastResponse` so the
  bearer token never follows a redirect; a 3xx is a failure (exit 1).
- Headers: `Authorization: Bearer <token>`, `User-Agent: vk-llm/<version>`.
  `start` and non-forced `stop` POST with an empty body and no `Content-Type`;
  a forced `stop` POSTs `{"force":true}` with `Content-Type: application/json`.
- Responses are decoded into a struct that ignores unknown fields, so both
  the ADR 0012 §1 document (with `result`, `nextCreateAt`) and the CORE-055
  document parse. Body read is capped at 1 MiB.
- No error message, log line or `--json` output contains the token or the
  `Authorization` header; errors print the HTTP status, the `error.code`,
  `error.message` and `error.reason` from the CORE-055 error body.

### Exit codes

| Code | When |
|---|---|
| 0 | `start` reached `READY` (or `--detach` got 200/202); `stop` reached `STOPPED` (or `--detach` got 200/202); `status` got 200; `budget` got 200; `rentals` got 200; `version` |
| 1 | 503 `gpu_unavailable` (prints `reason` verbatim), 400/404/405/413/500, network or TLS error on the first call, timeout while polling, `start` sees `STOPPED` with non-empty `unavailable`, 3xx |
| 2 | usage error, bad flag, bad duration, missing endpoint or token, bad config file, token key in the file, plain `http://` to a non-loopback host |
| 3 | 401 `invalid_token` or 403 `permission_denied` on any call |

### Polling

After the POST (unless `--detach`), the CLI calls `GET status` every 5 s
until the outcome below or the deadline. It prints a line only when `state`
differs from the last printed one: `<HH:MM:SS> <STATE>` on stderr (stdout
stays clean for `--json`). With `--json` the final status document is
printed to stdout on exit.

| Command | Outcome | Exit |
|---|---|---|
| `start` | `state == READY` → prints `ready: <model.alias> on <instance.gpu>, idle shutdown in <N> min` (N = `secondsUntilIdleShutdown` / 60, rounded down) | 0 |
| `start` | `state == STOPPED` and `unavailable != ""` → prints `start failed: <unavailable>` | 1 |
| `start` | `FAILED`, `DRAINING`, `DELETING` → printed and polling continues (a start on a draining GPU walks `DRAINING → DELETING → STOPPED → ALLOCATING …`, CORE-055 AC1) | — |
| `stop` | `state == STOPPED` | 0 |
| both | deadline reached → prints `timed out after <d> in state <STATE>` | 1 |
| both | 401/403 during polling | 3 |
| both | 429 → wait `Retry-After` seconds (min 5 s); 5xx or network error → retried at the next tick | — |

A timeout or Ctrl-C does not send `stop`: the GPU keeps its idle deadline
(ADR 0011 §1) and the message says so: `the GPU is not stopped; run
vk-llm stop, or it stops after its idle timeout`. The CLI never hard-codes
the 30 min (C-7).

### Idempotency (FR-3.4)

`start` treats a 200 response with `state == READY` as already running:
it prints `already running, idle shutdown in <N> min`, makes no further
call, and exits 0. The CLI never reads or depends on the `result` field;
the HTTP status and `state` carry the outcome. `stop` on 200 with
`STOPPED` prints `already stopped` and exits 0. Neither command can move
the deadline: the only calls the CLI makes are start, status and stop,
none of which write `lastInferenceFinishedAt` (ADR 0011 §2, CORE-055 R11).

### Status table (FR-7.4, ADR 0012 §4)

| Row | Field | Empty rendering |
|---|---|---|
| Provider | `provider` | — |
| State | `state` (+ ` (desired <desired>)` when `desired` differs from what `state` implies) | — |
| GPU | `instance.gpu` | `—` when `instance` is null |
| Model | `model.alias`, `model.contextSize` (rendered `<alias> (<n> tokens per session)`, CORE-300) | — |
| Ready | `ready` as `yes`/`no` | — |
| Price | `instance.priceUSDPerHour` as `$0.71/h` | `—` |
| Uptime | `uptimeSeconds` as `30m30s` | `0s` |
| Est. cost | `cost.estimatedInstanceUSD` this instance, `cost.estimatedMonthUSD` / `cost.budgetMonthUSD` this month, suffixed `(estimate)` | — |
| Last inference | `lastInferenceFinishedAt` in local time | `never` |
| Idle shutdown in | `secondsUntilIdleShutdown` as `24m00s` | `—` |
| Unavailable | `unavailable` | row omitted when `""` |

Fixed two-column layout via `text/tabwriter`; no colour. `--json` prints
the response body byte for byte.

### Distribution (ADR 0013 §3)

CORE-120 builds `vk-llm` with GoReleaser for `darwin/arm64`,
`darwin/amd64`, `linux/amd64`, `linux/arm64` and publishes archives plus
`checksums.txt` on the GitHub Release `vX.Y.Z`. This spec adds the
`README.md` install section: download the archive and `checksums.txt` for
the version, run `sha256sum -c --ignore-missing checksums.txt` (`shasum -a
256 -c` on macOS), extract, put `vk-llm` on `PATH`, export
`VK_LLM_API_ENDPOINT=https://llm.example.com` and
`VK_LLM_TOKEN=sk-vkllm-REPLACE_ME`. No signing in the MVP (ADR 0013 §3).

### Observability

None: the CLI emits no metrics and no logs beyond its stdout/stderr lines.
Server-side, each call shows in `vkllm_mgmt_requests_total` (CORE-055).

## Requirements

- R1. `vk-llm` MUST speak only to `/api/v1/gpu/{start,status,stop,budget,rentals}` and MUST NOT hold, read or accept any provider credential. (source: ADR 0012 §4, FR-7.6)
- R2. Configuration MUST resolve flag > environment > file > default per setting. (source: ADR 0012 §4, FR-7.6)
- R3. The token MUST come only from the environment variable named by `--token-env`/`api.tokenEnv` (default `VK_LLM_TOKEN`); a config file containing a `token` key MUST be refused with exit 2 before any network call. (source: ADR 0012 §4, FR-7.6)
- R4. The token MUST NOT appear in any output, error message or flag. (source: C-16, ADR 0013 §4)
- R5. Every HTTP call MUST have a 15 s timeout, TLS verification on, no redirect following, and `User-Agent: vk-llm/<version>`. (source: C-27, ADR 0013 §4)
- R6. A non-loopback `http://` endpoint MUST be refused with exit 2. (source: ADR 0012 §6, C-16)
- R7. `start` MUST wait for `READY` by default, print each state change, honour `--detach` and `--timeout` (default 15m), and exit 1 on failure or timeout. (source: ADR 0012 §4, FR-7.3, FR-2.5)
- R8. `start` on a READY GPU MUST make exactly one call, print `already running, idle shutdown in N min`, and exit 0. (source: FR-3.4, ADR 0011 §2)
- R9. `stop` MUST wait for `STOPPED` by default, honour `--detach` and `--timeout` (default 5m), and exit 1 on timeout. (source: ADR 0012 §4, FR-7.5)
- R10. `status` MUST print every row of the Status table, or with `--json` the raw response body. (source: FR-7.4, FR-6.8)
- R11. Exit codes MUST follow the Exit codes table. (source: FR-7.3, FR-7.5)
- R12. The CLI MUST NOT send `stop` on timeout or interrupt. (source: ADR 0011 §1)
- R13. Polling MUST use `clock.Clock` with a 5 s interval and status calls only. (source: C-29, ADR 0011 §2)
- R14. The only third-party module the CLI adds MUST be `gopkg.in/yaml.v3`. (source: ADR 0012 Alternatives, C-25)
- R15. The README install section MUST verify the archive against `checksums.txt` and use placeholders only. (source: ADR 0013 §3, C-14, FR-7.7)

## Implementation steps

1. `config.go`: flag parsing, env, YAML file, precedence, token resolution, endpoint scheme check. Tests: `TestConfigPrecedence` (table: 4 settings × 4 sources), `TestTokenInFileRejected`, `TestUnknownKeyRejected`, `TestPlainHTTPRejected`.
2. `client.go`: request building, headers, timeout, redirect refusal, error-body decode, exit-code mapping. Tests: `TestHeaders`, `TestErrorMapping` (table over 200/202/3xx/400/401/403/429/500/503), `TestNoRedirect`.
3. `render.go`: status table and `--json`. Test: `TestStatusGolden` with golden files `testdata/status_{stopped,allocating,ready,draining,unavailable}.golden`.
4. `commands.go`: start/stop/status with the poll loop on a fake clock. Tests: `TestStartWalk`, `TestStartAlreadyRunning`, `TestStartTimeout`, `TestStartNoOffer`, `TestStopWalk`, `TestDetach`.
5. `main.go` and `version`. Test: `TestVersion`, `TestUsage`.
6. Token redaction sweep over every test's captured stdout/stderr. Test: `TestNoTokenInOutput`.
7. README install section; tier 2 `vk-llm status` step in the CORE-080 kind job.

## Tests / acceptance criteria

Tier 1: `go test ./cmd/vk-llm/...`; the fake server is `httptest.NewServer`
with a scripted list of `(status code, body)` per route and a call log;
fake clock from `internal/clock/fake`; no sleeps. Token in tests is
`sk-vkllm-REPLACE_ME`.

- AC1. Start walk (FR-7.3, FR-2.5). Fake: start → 202 `ALLOCATING`; status → `ALLOCATING`, `STARTING`, `STARTING`, `LOADING_MODEL`, `READY` with `secondsUntilIdleShutdown` 1800. Action: `start`, advancing the clock 5 s per poll. Expected: exit 0; stderr has exactly 4 state lines in order `ALLOCATING, STARTING, LOADING_MODEL, READY`; final line contains `idle shutdown in 30 min`; fake log = 1 POST start + 5 GET status, 0 stop.
- AC2. Already running (FR-3.4). Fake: start → 200 `READY`, `secondsUntilIdleShutdown` 1440. Expected: exit 0; stdout `already running, idle shutdown in 24 min`; fake log = exactly 1 request; fake status body byte-identical before and after.
- AC3. Detach. Fake: start → 202. Action: `start --detach`. Expected: exit 0, fake log = 1 request. Same for `stop --detach`.
- AC4. Start timeout (FR-7.3). Fake: status always `LOADING_MODEL`. Action: `start --timeout 1m`. Expected: exit 1 after the clock passes 60 s; stderr contains `timed out` and `not stopped`; fake log has 0 stop calls.
- AC5. Start failure. (a) start → 503 `gpu_unavailable` `reason` `budget_exhausted`: exit 1, stderr contains `budget_exhausted`, 1 request. (b) start → 202, status → `ALLOCATING`, then `STOPPED` with `unavailable` `no_offer`: exit 1, stderr contains `no_offer`. (c) a reason not in CORE-055's list (`balance_unknown`) is printed verbatim, exit 1.
- AC6. Stop walk (FR-7.5). Fake: stop → 202 `DRAINING`; status → `DRAINING`, `DELETING`, `STOPPED`. Expected: exit 0, 3 state lines. Stop on STOPPED → 200: stdout `already stopped`, exit 0, 1 request. Status stuck at `DELETING` with default timeout: exit 1 once the clock passes 5 m.
- AC7. Status (FR-7.4, FR-6.8). Five scripted documents (STOPPED with null `instance`, ALLOCATING, READY, DRAINING, STOPPED with `unavailable` `no_offer`). Expected: output equals the golden files byte for byte; the READY table has all 10 rows of the Status table and the word `estimate`. `status --json` stdout equals the fake body byte for byte.
- AC8. Exit codes. 401 → 3, 403 → 3 (on start, status and stop, and on the 3rd poll of a start), 500 → 1, 302 → 1 with the fake's redirect target receiving 0 requests, unknown subcommand → 2, `--timeout abc` → 2, missing token variable → 2.
- AC9. Config precedence (FR-7.6). Table test over the five Configuration rows: every combination of the sources each row has (flag, environment, file, default); expected value is from the highest source present in every case. A file with `api.token`, `token` at top level, or `Token` → exit 2 and 0 requests to the fake. `--endpoint http://llm.example.com` → exit 2; `http://127.0.0.1:<port>` accepted.
- AC10. Headers and redaction (C-16). Every request the fake records has `Authorization: Bearer sk-vkllm-REPLACE_ME`, `User-Agent: vk-llm/dev`; no request carries a body except a forced `stop`'s `{"force":true}`. Across all tests' captured stdout and stderr, the token string occurs 0 times, including the 401 error path.
- AC11. 429 during polling. Status returns 429 with `Retry-After: 7` once, then `READY`. Expected: the next poll is registered for 7 s (fake `BlockUntil`), exit 0.
- AC12. Tier 2 (docs/testing-strategy.md). The CORE-080 kind job runs `vk-llm status` built from the PR against the installed chart via `kubectl port-forward` to `127.0.0.1`. Expected: exit 0, table shows `State` `STOPPED`.
- AC13. Distribution (FR-7.7). A CORE-120 release dry run lists 4 `vk-llm` archives and `checksums.txt`; `vk-llm version` from one archive prints the release `X.Y.Z`.

## Definition of done

- [x] AC1–AC11 pass (tier 1, `go test ./cmd/vk-llm/...`, run locally this
      session; see Evidence for the AC→test mapping). Also passed in the
      real PR #29 CI run (`go` job, run 43734025623).
- [x] AC12 passes in the kind job. Wired this round (`.github/workflows/ci.yml`
      lines 311-320) and confirmed by a real `chart-kind` run: PR #29, run
      [43734025623](https://github.com/savak1990/vk-llm-on-demand/actions/runs/43734025623/job/132872594086),
      conclusion success.
- [ ] AC13 checked in the first release dry run (CORE-120). `.goreleaser.yaml`
      confirmed correct via a local `goreleaser --snapshot` build during
      Task 5's review, but the user declined triggering a real `release.yml`
      dispatch (same decision as CORE-120/CORE-110). Stays open as an
      accepted gap — see Evidence and "Status flipped to DONE" below.
- [x] `go.mod` lists `gopkg.in/yaml.v3` and CORE-010's allowed-module list
      names it (`go.mod:9`; `specs/core/010-D-repo-skeleton-and-ci/spec.md`
      lines 79 and 277).
- [x] README install section present with checksum verification and
      placeholders only (`README.md` lines 99-124).
- [x] `make specs-check` passes, confirmed in real CI (PR #29, `repo` job,
      [43734025623](https://github.com/savak1990/vk-llm-on-demand/actions/runs/43734025623/job/132872594160),
      conclusion success) and locally after `scripts/specs-check.sh` was
      fixed to exclude `docs/superpowers/plans/`.
- [x] Status flipped to DONE in the same PR; folder renamed
      `150-D-vk-llm-cli`. User decision (asked directly, not decided
      silently): flip to DONE now, matching CORE-120's precedent, since
      AC1-AC12 and `specs-check` are all confirmed by real CI and only
      AC13 (a real release dispatch the user has repeatedly declined to
      trigger, even though `.goreleaser.yaml` is already confirmed
      correct) remains open as an accepted gap.
- [x] Evidence section filled.

## Open questions

| Question | Why it matters | Settled by | Default |
|---|---|---|---|
| Does ADR 0012 §1's top-level `result` field ship, or CORE-055's "no field"? | ADR and CORE-055 disagree | ADR 0012 / CORE-055 edit | the CLI ignores `result` either way; HTTP status + `state` decide |
| Full `reason` set: ADR 0012 §1 lists `balance_unknown`, CORE-055 does not | printed text only | CORE-055 | print any reason verbatim |
| Homebrew tap | easier install on macOS | later ADR | not in MVP; archives + checksums only |
| `sha256sum -c --ignore-missing` and `shasum -a 256 -c` behaviour on the target macOS/Linux versions | README command must work as written | first release dry run (AC13) | the two commands above |
| Should `start` exit non-zero on `FAILED` instead of polling on? | a FAILED GPU may be retried by the controller or end in STOPPED | CORE-050 | keep polling; `STOPPED` + `unavailable` or the timeout ends it |
| Does `--timeout` cover the first POST as well as polling? | a 15 s POST counts against a 1m test timeout | this spec | yes, the deadline starts before the POST |

## Evidence

### Commits

| Commit | Task | Content |
|---|---|---|
| `a0dd748` | 1 | config resolution (flag > env > file > default) |
| `f6eda5b` | 2 | management-API client, error mapping, shared test fake |
| `fa83ea6`..`15c3060` | 3 | status table and JSON rendering; `15c3060` is a same-task fix round that corrected a broken state/desired comparison, local-time rendering, and duration padding |
| `67dc541`..`0e0b12e` | 4 | start/status/stop/version subcommands and poll loop; `0e0b12e` is a same-task fix round that added AC11/AC8-mid-poll test coverage |
| `a9fa457`..`7c23bd0` | 5 | kind job wiring, README install section; `7c23bd0` is a same-task fix that corrected the GoReleaser archive naming/checksum commands |

### AC → test mapping (tier 1, `go test ./cmd/vk-llm/...`, ran locally this session: `ok`)

| AC | Tests |
|---|---|
| AC1 start walk | `TestStartWalk` |
| AC2 already running | `TestStartAlreadyRunning` |
| AC3 detach | `TestDetach` |
| AC4 start timeout | `TestStartTimeout` |
| AC5 start failure / no offer | `TestStartNoOffer` |
| AC6 stop walk | `TestStopWalk` |
| AC7 status golden/JSON | `TestStatusGolden`, `TestJSONRender` |
| AC8 exit codes, incl. mid-poll 403 | `TestErrorMapping`, `TestMidPollForbidden`, `TestUsage` |
| AC9 config precedence | `TestConfigPrecedence`, `TestTokenInFileRejected`, `TestUnknownKeyRejected`, `TestPlainHTTPRejected` |
| AC10 headers/redaction | `TestHeaders`, `TestNoTokenInOutput` |
| AC11 429 retry-after | `TestRetryAfter429` |
| AC12 tier 2, kind job | wired this round (`.github/workflows/ci.yml` "Build the vk-llm CLI" / "vk-llm status shows STOPPED" steps, lines 311-320); confirmed by a real `chart-kind` CI run, PR #29, run 43734025623, conclusion success |
| AC13 distribution / release dry run | stays unchecked; needs a real `release.yml` dispatch. `.goreleaser.yaml` is already correct (confirmed via a real local `goreleaser --snapshot` build during Task 5's review), but the user previously declined triggering a real release dispatch (same decision as CORE-120/CORE-110) |

Other tests not tied to a single AC but exercised by the suite:
`TestNoRedirect`, `TestVersion`, `TestFakeServerScriptsPerRoute`,
`TestStartAndStop`.

### `make specs-check` fixed to pass locally too

Earlier in this round, `scripts/specs-check.sh`'s missing-folder check
flagged the folder path this spec's DONE rename would use (`specs/core/`
combined with `150-D-vk-llm-cli`) — a path named only in a local,
gitignored plan document at `docs/superpowers/plans/2026-11-11-core-150-vk-llm-cli.md`,
not in any tracked file. The script's `--exclude-dir=.superpowers` flag
worked correctly; the actual gap was that `docs/superpowers/plans/` is a
*different* gitignored directory the exclude list never covered. Fixed in
commit `152a042` by pruning that exact path via `find ... -prune` instead
of relying on `grep --exclude-dir`. `make specs-check` now passes both
locally (plan doc still on disk, verified) and in real CI — PR #29's
`repo` job (run 43734025623) passed, and a post-fix local run confirms the
same.

### Folder / status

AC12 and `make specs-check` are confirmed by real CI runs (PR #29, runs
43734025623 and 43736491698). AC13 is still open by user decision — the
user was asked directly, not decided for silently, and chose to flip
this spec to DONE now (as CORE-120's did), accepting AC13 as a known
gap rather than waiting on it (as CORE-110 chose to for its own open
item). Folder renamed `150-D-vk-llm-cli`, `status: DONE`.
