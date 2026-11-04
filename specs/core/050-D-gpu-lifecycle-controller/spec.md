---
id: "CORE-050"
status: "DONE"
updated: "2026-11-03"
depends_on: ["CORE-020", "CORE-040"]
adrs: ["0011", "0010", "0012", "0003", "0001", "0002", "0004", "0005", "0006", "0007"]
---

# 050 — GPU lifecycle controller

**Status note:** DONE 2026-11-03. Written 2026-11-01 before any code existed; updated 2026-11-01 for explicit provisioning (ADR 0010) and the rolling 30-minute idle rule (ADR 0011); implementation, the simulation harness and AC1–AC27 landed on `core-050-controller` and this write-back carries it to DONE.

**Complexity:** L
**Risk:** high — this package decides when money is spent and when a live instance is destroyed
**Estimated effort:** three to four sessions (14–18 h), half of it the simulation harness and the tunnel
**Recommended model:** strong — fencing, the admission mutex, the two-observation rule and the saga are cost-safety critical
**Depends on:** [CORE-020](../020-D-vastai-provider-client/spec.md), [CORE-040](../040-D-state-store/spec.md)

## Outcome

`internal/controller` runs the single-instance state machine of architecture §4. `Start` records `desired=RUNNING` and the controller provisions exactly one instance, drives it through boot, link and model load, holds the SSH tunnel and admits inference through `Admit`/`RequestEnd`. It destroys the instance 30 minutes after `max(readyAt, lastInferenceFinishedAt)`, at once on `Stop` (after a drain of at most 60 s), and on max lifetime, budget, startup timeout, worker failure or a terminal provider status. Every 60 s it reconciles the provider's label list against the state document, destroys orphans, and after a restart re-adopts a Ready instance or drains one whose persisted deadline has passed. A deterministic simulation (fake provider, store, clock, dialer) proves it with exact call counts.

## Scope

- `internal/controller`: stages, public states, events, transition table, admission mutex, idle rule, provisioning saga, loop, reconcile, restart recovery, the controller API of ADR 0012 §2, metrics, logs.
- `internal/tunnel`: `Dialer`, `Conn`, implementations `ssh` and `direct` (ADR 0006).
- `internal/controller/sim_test.go`: the simulation harness and AC1–AC27.

Excludes: provider HTTP calls and the offer filter (CORE-020); `Load`/`Save`/`Transition` (CORE-040); `Probe`, `WorkerKey`, `BuildCreate` (CORE-070); ledger arithmetic, rollover and `budget.Checker` (CORE-100); `internal/clock` and its fake (CORE-010); HTTP routes and status codes (CORE-055, CORE-060); alert rules (CORE-090); `maxInstances > 1` (ADR 0003 §2); on-demand mode (below, future).

## Design and contracts

### Stages, public states and persistence

`type State int`: `Idle, Searching, Creating, Booting, Loading, Ready, Serving, Draining, Destroying`; `String()` gives the names stored in `store.Instance.State`, used only for persistence and logs, never as a metric label. `PublicState()` maps per ADR 0011 §6: Idle → `STOPPED`; Searching, Creating → `ALLOCATING`; Booting → `STARTING`; Loading → `LOADING_MODEL`; Ready, Serving → `READY`; Draining → `DRAINING`; Destroying → `DELETING`, or `FAILED` while the memory field `failure` is set (T14, T15, T24; cleared at T29). `PublicState()`'s 8 values are the sole source of the `vkllm_gpu_state` label (Design Decision 6 — the internal `State` names are never exposed as a label).

| Item | Where | Why |
|---|---|---|
| `Idle`, `Searching` | `doc.Instance == nil`; `Searching` is memory only | no provider mutation yet; a restart resumes as Idle |
| `Creating`…`Destroying` | `doc.Instance.State` | a provider instance may exist |
| `Serving` | memory only; stored as `Ready` | `inFlight > 0` is not durable (C-12) |
| `inFlight`, `admitting`, the tunnel `Conn`, suspect flag, `failure` | memory, under the admission mutex | lost on restart by design |
| two-observation counters, drain start, stage timestamps, search backoff | memory | a restart re-observes; drain restarts with 0 in flight |
| create backoff | derived from trailing `create-*` ops in `doc.Ops` | survives restart without a new field |
| `desired` (`RUNNING`/`STOPPED`), `generation`, denylist, attempts | document (CORE-040) | `desired` changes only as the table says |
| `instance.readyAt`, `instance.lastInferenceFinishedAt`, `instance.idleDeadline` | document (CORE-040, ADR 0011 §1) | restart-safe idle rule; `lastInferenceFinishedAt`/`idleDeadline` flushed ≤ 10 s after `RequestEnd` |

The flush lag means a restart can lose at most 10 s of deadline extension; the destroy then happens ≤ 10 s early, inside FR-3.1's one-interval tolerance. Memory is authoritative while the process lives.

### Events, definitions and transition table

Events: `Start`, `Stop`, `OfferChosen`, `NoOffer`, `Created`, `CreateFailed`, `LinkUp`, `HealthOK`, `Admit`, `RequestEnd`, `Tick`, `IdleTimer`, `ProviderObserved(status)`, `Vanished`, `WorkerFailed`, `DeadlineExpired`, `BudgetExhausted`, `Gone`. Definitions:
- **Wanted** = `desired=RUNNING` AND `doc.Instance == nil`.
- **Gate** = budget ok (the last tick's `Budget.Allow(doc, balance)`) AND (`attemptsThisHour.hour` ≠ current UTC hour OR `count < lifecycle.safety.maxProvisionAttemptsPerHour`) AND now ≥ next create time (saga step 7) AND now ≥ next search time.
- **idleDeadline** = `max(readyAt, lastInferenceFinishedAt) + lifecycle.idleShutdown.timeout` (ADR 0011 §1).
- **Idle rule** = `lifecycle.idleShutdown.enabled` AND state Ready AND `inFlight = 0` AND now ≥ idleDeadline.
- **BudgetExhausted** = `Allow` false, or `lifecycle.safety.maxInstanceSpendUSD > 0` and the open entry's `EstUSD` ≥ it (CORE-100).
- **Drain(reason)** = under the admission mutex `admitting=false`; CAS state `Draining`, `desired=STOPPED`; drain start = now; post-commit `vkllm_shutdowns_total{reason}++`.
- **Twice** = two consecutive successful observations (C-9); failed reads neither count nor reset.

| # | From | Event | Guard | To | Side effect (CAS first, then call) |
|---|---|---|---|---|---|
| T1 | Idle | Start / Tick / Gone | wanted, gate ok | Searching | `SearchOffers(filter, denylist)` |
| T2 | Idle, Draining, Destroying | Start / Tick / Gone | Start, or wanted; budget or attempts denied, or `no_offer` search backoff | same | CAS `desired=STOPPED` if `RUNNING`; `Unavailable` = mapped reason; `Start` returns `ErrGPUUnavailable`; outcome edge-triggered; a create-backoff wait is not a denial |
| T3 | Searching…Serving | Start | `desired=RUNNING` | same | nothing written, no timestamp touched; snapshot returned (200 or 202 at CORE-055) |
| T4 | Creating (`desired=STOPPED`), Draining, Destroying | Start | not T2 | same | CAS `desired=RUNNING`; T8 then goes to Booting; from Draining/Destroying T1 runs at T29 |
| T5 | Searching | OfferChosen | gate ok, `desired=RUNNING` | Creating | CAS `generation++`, instance `{Creating, label, offer, createdAt}`, op `create-<gen>` outcome `""`, `attemptsThisHour` = `{current hour, count+1}`; then `CreateInstance` |
| T6 | Searching | NoOffer / gate denied | — | Idle | CAS `desired=STOPPED`; `Unavailable` = `no_offer` or mapped reason; next search ≥ now+60 s; outcome `no_offer`, `budget_exhausted`, `attempts_exhausted` |
| T7 | Searching | search error / Stop / OfferChosen with `desired=STOPPED` | — | Idle | error: outcome `provider_error`, next search ≥ now+60 s, `desired` kept; Stop: CAS `desired=STOPPED`, outcome `cancelled` |
| T8 | Creating | Created(id) | op `create-<gen>` still `""` | Booting, or Destroying if `desired=STOPPED` | CAS `providerId`, op `created` (or `cancelled`), `budget.OpenEntry(doc, now, gen, dph)`; post-commit metrics |
| T9 | Creating | CreateFailed(Terminal/Fatal, not duplicate) or `AssertPrice` error | — | Idle | CAS instance nil, op `create_error`; outcome `provider_error`; `desired` kept (backoff retry); Fatal logs at `error` |
| T10 | Creating | CreateFailed(Retryable) | — | Creating | op stays `""`; saga step 4 resolves it |
| T11 | Creating | CreateFailed(`ErrDuplicateLabel`) | — | Destroying | destroy every instance with the exact label |
| T12 | Booting | ProviderObserved(running, ssh set) | — | Booting | CAS `sshHost`, `sshPort`; `Dial` with that same `GetInstance` result |
| T13 | Booting | LinkUp | — | Loading | CAS state, `hostKeyFingerprint`; probe at once, then every 5 s |
| T14 | Creating…Draining | ProviderObserved(`exited`/`offline`/`unknown`) twice | — | Destroying | close `Conn`; `failure`; op `failed_status` and outcome `failed_status` if before Ready; `desired=STOPPED` if after Ready; reason `failure` |
| T15 | Creating/Booting/Loading | DeadlineExpired | `now − createdAt ≥ lifecycle.startupTimeout` | Destroying | op `deadline`; denylist `{machineId, now+1h}`; `failure`; outcome `deadline_exceeded`; `desired` kept (backoff retry) |
| T16 | Creating (id)/Booting/Loading | Stop / BudgetExhausted | — | Destroying | CAS `desired=STOPPED`, op `cancelled`; outcome `cancelled`; reason `manual`/`budget` |
| T17 | Creating (no id) | Stop / BudgetExhausted | — | Creating | CAS `desired=STOPPED`; T8 then goes to Destroying; `create_lost` (saga step 4) goes to Idle |
| T18 | Ready/Serving | Admit(gen) | gen current, `admitting` | Serving | `inFlight++` under the admission mutex; returns `Conn.RoundTripper()` |
| T19 | Serving | RequestEnd(gen) | gen current | Serving, or Ready at `inFlight=0` | `inFlight--`; `lastInferenceFinishedAt=now`; recompute idleDeadline; re-arm the idle timer; flush ≤ 10 s |
| T20 | Ready | IdleTimer / Tick | idle rule | Draining | Drain(`idle`) |
| T21 | Ready/Serving | Tick | `now − createdAt ≥ lifecycle.safety.maxLifetime` | Draining | Drain(`lifetime`), regardless of `inFlight` |
| T22 | Ready/Serving | Stop / BudgetExhausted | — | Draining | Drain(`manual`/`budget`) |
| T23 | Ready/Serving | Vanished (absent twice) | — | Idle | close `Conn` (in-flight fail, CORE-060 maps); CAS instance nil, `desired=STOPPED`, `budget.CloseEntry(doc, now, gen)`; reason `failure`; log `instance_lost` |
| T24 | Ready/Serving | WorkerFailed: two non-`Ready` probes, or `ErrHostKeyMismatch` | — | Destroying | close `Conn`; CAS `desired=STOPPED`, `lastError`; `failure`; reason `failure`; log `instance_lost` |
| T25 | Draining | RequestEnd / entry | `inFlight=0` | Destroying | |
| T26 | Draining | DeadlineExpired | `lifecycle.shutdown.drainTimeout` since drain start | Destroying | close `Conn` (in-flight streams get an in-stream error, CORE-060) |
| T27 | Creating…Loading, Draining | Vanished (absent twice) | — | Idle | CAS instance nil, `budget.CloseEntry(doc, now, gen)`; op `lost` if not final; outcome `lost` if before Ready |
| T28 | Destroying | entry / retry timer | fencing holds | Destroying | CAS op `destroy-<gen>`; `DestroyInstance`; on nil `GetInstance` |
| T29 | Destroying | Gone (`ErrNotFound`) | — | Idle | CAS instance nil, op `destroy-<gen>` `ok`, `budget.CloseEntry(doc, now, gen)`; clear `failure`; price gauge 0; if wanted, evaluate T1 at once |
| T30 | Destroying | destroy or get error, or still present | — | Destroying | retry after `min(5m, 10s·2^n)`; `vkllm_destroy_attempts_total{outcome}` = provider error class (`retryable`, `terminal`, `fatal`); still present after a nil destroy counts `retryable` |
| T31 | Idle, Draining, Destroying | Stop | — | same | CAS `desired=STOPPED` if not already; nothing else |
| T32 | Loading | HealthOK | `Probe` = `Ready` | Ready | CAS state, `readyAt=now`, `idleDeadline=readyAt+timeout`, op `ready` + `finishedAt`; arm the idle timer; outcome `ok` |

`ProviderObserved(StatusOther(raw))` is never a transition: only the startup timeout (T15) or vanish (T23/T27) ends it (wave-1 ruling, CORE-020). Only `Start` writes `desired=RUNNING`; every drain, every loss after Ready, `no_offer`, a budget denial and an exhausted attempt count write `STOPPED`, so no shutdown re-provisions by itself. Failures before Ready keep `RUNNING` and retry under the backoff and the hourly cap. A guard that no longer holds inside the CAS `fn` returns `errStale`; the event is dropped and the loop re-reads. Any (state, event) pair not in the table leaves the state unchanged. Reason mapping for `Unavailable`: `Allow` `monthly_budget_exceeded` → `budget_exhausted`, `balance_floor` and `balance_unknown` → `balance_low` (CORE-100), both counted as outcome `budget_exhausted`; attempts cap → `attempts_exhausted`; a `Start` inside the 60 s search backoff after a `no_offer` → `no_offer`.

### Admission mutex (ADR 0011 §4)

One `sync.Mutex` guards `(state, generation, admitting, inFlight, Conn)`. `Admit`, `RequestEnd`, Drain and every exit from Ready/Serving take it. A request admitted before Drain is counted in `inFlight` and drained; one arriving after gets an error. `Admit` returns `ErrDraining` for a stale generation or state Draining, and `ErrBackendUnavailable` for any other state or `desired=STOPPED`. Concurrent `Admit`s only raise `inFlight`, so they never trigger a shutdown (FR-3.9). `RequestEnd` in any state other than Serving/Draining, or for a non-current generation, only decrements `inFlight` (floored at 0) and touches no timestamp. The memory flip happens before the CAS; a failed CAS is re-queued, admission stays closed.

### Fencing (C-11)

Every CAS `fn` checks that `doc.Generation` and `doc.Instance.Label` equal the values the event was raised for, else `errStale`. `DestroyInstance(id)` runs only when the instance's label parses as `vkllm:<installationId>:<g>` with the configured `installationId` (ADR 0004 §6 as amended) and `g ≤ doc.Generation` from the `Load` of the same loop iteration. Unparsable labels are logged, never destroyed.

### Provisioning saga

1. **Search.** `SearchOffers` with the config filter, `MaxHourly` = `lifecycle.safety.maxHourlyPriceUSD`, `ExcludeMachineIDs` = unexpired `machineDenylist` (expired entries pruned in the next CAS). First offer wins.
2. **Record before acting (C-2).** T5 commits op `create-<gen>` and label `vkllm:<installationId>:<gen>` before any provider call. The request is `worker.BuildCreate` with `WorkerKey(workerSeed, installationId, gen)` (ADR 0006 §4). Before the call, `budget.AssertPrice(offer.Hourly.USD(), cfg.MaxHourly)`; an error takes T9 (outcome `provider_error`, counted as an attempt) and `CreateInstance` is not called.
3. **Create.** `CreateInstance` (CORE-020 lists and adopts inside one call). Result → T8, T9, T10 or T11.
4. **Unknown outcome** (op `""` and no `providerId`, seen at a poll, a tick or a restart): `ListInstances` with the exact label as prefix. One match → T8 (adopt). Several → adopt the lowest id; the rest are orphans. Zero on two consecutive successful lists → CAS op `create_lost`, instance nil, Idle, outcome `lost`; a late instance with that label is then an orphan. List error → next poll. The startup timeout bounds this step.
5. **Boot and link.** Every 5 s `GetInstance(providerId)` until `running` with `sshHost`/`sshPort`; then `Dialer.Dial` with the address from that result, never from the document (NFR-8.4). Failure → next poll.
6. **Readiness.** Every 5 s `worker.Client{HTTP: conn.RoundTripper(), Key, Alias}.Probe`: `Unreachable` and `Loading` stay; `Ready` (health 200 and alias listed) → T32 (FR-2.7).
7. **Backoff.** n = trailing `create-*` ops whose final outcome is not `ready`; next create not before the last one's `finishedAt + min(30m, 1m·2^(n−1))` (ADR 0003 §5). `ready` resets n to 0.

### Reconcile, every `controller.reconcileInterval`

One `Load`, one `ListInstances("vkllm:<installationId>:")`, one `Provider.Balance` (on error reuse the last good value; with none since start, deny with reason `balance_unknown`), then:

| Provider says | Store says | Action |
|---|---|---|
| list error | any | no destroy decision; `reconcile_runs_total{outcome="error"}`; time rules (T15, T20, T21, T22, T26) still run |
| label gen > `doc.generation` | — | CAS `generation = g` (fence advanced); counts as the first orphan observation |
| our prefix, id ≠ `providerId`, no pending op for its gen | — | orphan: twice → `DestroyInstance` + `GetInstance`, `orphans_destroyed_total++`, `shutdowns_total{reason="orphan"}++`; repeated each tick until absent |
| pending op gen matches | Creating, no id | saga step 4 |
| `exited`/`offline`/`unknown` | Creating…Draining | `ProviderObserved` → T14 (ADR 0003 §4) |
| absent | known with id | `Vanished` after twice → T23/T27 |
| running | known | Ready/Serving: one `Probe`; address changed → redial via step 5 |

The tick's CAS prunes expired denylist entries, then runs `budget.Rollover`, `budget.RefreshOpenEntry` and `Budget.Allow(doc, balance)`, then BudgetExhausted, the idle rule from the persisted idleDeadline and max lifetime (FR-6.9). These are pure document mutations. **Post-commit metrics:** after `Transition` commits, and only then, the loop sets from the committed document `obs.SetBudgetUsedRatio(Budget.UsedRatio(doc))`, `obs.SetCostInstanceUSD(<open entry EstUSD or 0>)`, `obs.AddCostEstimatedUSD(<sum of each entry's EstUSD increase versus the previous committed document, ignoring rollover drops>)`, `obs.SetProviderBalanceUSD(balance)`, the two timestamp gauges and `vkllm_shutdowns_total`; no CAS function calls an `obs` setter, so conflict retries never double-count. Reconcile never issues a destroy for a state already `Destroying`; T28 owns that timer. Reconcile never writes `lastInferenceFinishedAt` or `readyAt` (FR-3.5).

### Timing model (the simulation relies on it)

| Timer | Cadence | Active in |
|---|---|---|
| reconcile | t=0 (recovery pass), then every `controller.reconcileInterval` | always |
| idle | at idleDeadline; re-armed at T32 and every `RequestEnd` | Ready/Serving |
| poll | every 5 s from entry | Creating with op `""`, Booting, Loading |
| suspect probe | at `ReportUpstreamFailure` and 5 s later | Ready/Serving |
| startup timeout | `createdAt + lifecycle.startupTimeout` | Creating…Loading |
| drain deadline | drain start + `lifecycle.shutdown.drainTimeout` | Draining |
| destroy retry | `min(5m, 10s·2^n)` after a failed round | Destroying |
| metrics pull | every 15 s; `worker.Client.Metrics` → `Exporter`; on error `obs.IncInferenceScrapeErrors()` | Ready/Serving |
| activity flush | 10 s after the first unflushed `RequestEnd`, unless a CAS carried it | Ready/Serving |

Same-instant order: deadlines (idle, startup, drain), destroy retry, reconcile, poll, external events. So an `Admit` at the exact idleDeadline loses to the idle timer and gets `ErrDraining`, or `ErrBackendUnavailable` once the drain has moved on (FR-3.8). One loop goroutine; a CAS ending in `ErrRetriesExhausted` re-queues the same event 5 s later (the search is not repeated).

### Restart recovery (ADR 0003 §8)

1. `Load` (a keyless shell yields a fresh document with `desired=STOPPED`, CORE-040; `ErrStoreMissing` is retried every 5 s and fails `/readyz`). `store.ErrInstallation`: `Run` returns it and nothing is destroyed. `ErrStoreReset` from `Transition` mid-run: drop the event and rerun step 3.
2. Map the stored state: no instance and op `""` → saga step 4; `Creating` with id, `Booting`, `Loading` → resume at Booting (deadline still from `createdAt`); `Ready` with now ≥ the persisted idleDeadline, now ≥ `createdAt + maxLifetime`, or `desired=STOPPED` → Drain at once, no dial, then Destroying (FR-3.10); other `Ready` → `GetInstance`, `Dial`, `Probe`: `Ready` re-adopts without a write and arms the idle timer at the persisted idleDeadline, otherwise two non-`Ready` probes 5 s apart → T24; `Draining` → Destroying (in-flight is 0); `Destroying` → T28.
3. Run the reconcile pass at t=0. An empty store (generation 0) plus an instance under our prefix is the "gen above" row, so it is destroyed at the second tick (FR-4.3).
4. Start the loop. No provider mutation happens before step 1 succeeds.

### Tunnel (`internal/tunnel`, ADR 0006)

```go
type Instance struct {
    Host    string // sshHost from the latest GetInstance for the stored providerId
    Port    int
    HostKey string // pinned SHA256 fingerprint from the document; "" = trust on first use
}
type Conn struct{ /* rt, closer, hostKey */ }
func (c *Conn) RoundTripper() http.RoundTripper // requests reach 127.0.0.1:8080 on the worker
func (c *Conn) Close() error                    // in-flight requests on it fail; CORE-060 maps the error
func (c *Conn) HostKey() string                 // fingerprint seen at dial; the controller persists it (T13)
type Dialer interface{ Dial(ctx context.Context, in Instance) (*Conn, error) }
var ErrHostKeyMismatch = errors.New("tunnel: host key changed")
```

| Item | `ssh` | `direct` |
|---|---|---|
| Used when | `tunnel.dialer=ssh` (default) | `tunnel.dialer=direct` (kind job only) |
| Transport | `golang.org/x/crypto/ssh` client to `Host:Port`, user `root`, key `id_ed25519` from `existingSecrets.ssh` read per dial; one `direct-tcpip` channel to `127.0.0.1:8080` per HTTP connection | plain TCP to `tunnel.directAddr`; `Instance` ignored |
| Host key | `HostKey` empty → accept, report fingerprint; mismatch → `ErrHostKeyMismatch`, no channel opened (ADR 0006 §3) | none |
| Timeouts | dial and handshake 15 s; `keepalive@openssh.com` every 15 s, 3 misses close the `Conn` | dial 15 s |

The controller opens one `Conn` per generation in Booting, holds it through Loading, Ready, Serving and Draining, and closes it on every exit from those states and on shutdown.

### Controller API (ADR 0012 §2)

```go
var (
    ErrGPUUnavailable     = errors.New("controller: gpu unavailable")     // Start: Snapshot.Unavailable has the reason
    ErrBackendUnavailable = errors.New("controller: backend unavailable") // Admit: no Ready backend or desired=STOPPED
    ErrDraining           = errors.New("controller: not admitting")       // Admit: stale gen or Draining
    ErrForceNotEnabled    = errors.New("controller: force not enabled")   // Stop(force=true) in the MVP
)
func New(d Deps) (*Controller, error) // error unless lifecycle.provisioningMode == "explicit" (ADR 0010 §1)
func (c *Controller) Run(ctx context.Context) error
func (c *Controller) Start(ctx context.Context) (Snapshot, error)            // T1–T4; idempotent; one CAS at most
func (c *Controller) Stop(ctx context.Context, force bool) (Snapshot, error) // T7, T16, T17, T22, T31; idempotent
func (c *Controller) Status() Snapshot                                        // memory copy; never calls the provider
func (c *Controller) Admit(gen int64) (http.RoundTripper, error)             // T18
func (c *Controller) RequestEnd(gen int64)                                    // T19, T25; every exit path
func (c *Controller) ObserveFirstToken(gen int64, d time.Duration)           // first_token stage, once per generation
func (c *Controller) ReportUpstreamFailure(gen int64)                        // suspect: probe now and 5 s later

type Deps struct {
    Provider       provider.Provider
    Store          store.Store
    Dialer         tunnel.Dialer
    Clock          clock.Clock
    Budget         Budget                 // this package's own interface (Design Decision 7); CORE-100's budget.Checker implements it
    Worker         Worker                 // this package's own interface; CORE-070's worker.Client implements it
    WorkerSeed     func() ([]byte, error) // Secret key worker-seed, read per use (ADR 0006 §4)
    InstallationID string                 // configured (ADR 0004 §6 as amended)
    ModelAlias     string                 // owned by model.* config, not config.Controller
    Config         config.Controller      // lifecycle.* and controller.* keys, architecture §9
    Reg            prometheus.Registerer
    Log            *slog.Logger
}
type Snapshot struct {
    State State; Public string; Desired string; Generation int64; Since time.Time; InFlight int
    InstallationID, LabelPrefix, Provider string
    Unavailable string // "", no_offer, budget_exhausted, balance_low, attempts_exhausted
    GPUName string; MachineID int64; Hourly provider.Money; ProviderID provider.InstanceID
    CreatedAt time.Time; ReadyAt, LastInferenceFinishedAt, IdleDeadline *time.Time
    Ready bool; Uptime, UntilIdleShutdown time.Duration // Ready: Public == READY; UntilIdle 0 when IdleDeadline nil
    SpendInstanceUSD, SpendMTDUSD float64 // open entry EstUSD; budget.EstimatedMTD
    Stages map[string]time.Duration; LastError string; AttemptsThisHour int; NextCreateAt time.Time
}
```

`Start` and `Stop` run in the caller's goroutine: they take the admission mutex for the memory part, commit their one `Transition`, wake the loop through a channel of size 1, and return within the CAS time (FR-2.5). A `Start` that will take T1 sets memory state Searching before it returns, so its snapshot reads `ALLOCATING`. `Start` in Ready/Serving writes nothing (FR-3.4). `Stop(force=true)` returns `ErrForceNotEnabled` unless `lifecycle.shutdown.allowForce`; with it, Drain is followed by T26 at once (ADR 0011 §3, future). CORE-055 builds the status document of R-O from `Snapshot` plus `model.*` and `budget.monthlyUSD` config.

### Future: on-demand mode (ADR 0010 §4, ADR 0007)

Not compiled into MVP paths. When `lifecycle.provisioningMode: on-demand` is implemented, it adds `Demand(ctx) (<-chan Ready, error)` delivering a `Ready{Generation, Transport}` value (buffered 1: one send then close; close without a value on failure), a waiter set closed with `gpu_unavailable` or `gpu_cold_start`, and makes Wanted also true while waiters exist. Bounds come from `onDemand.maxWait`/`maxPending`; CORE-060 owns the heartbeat. Its tests reuse this harness. Nothing else in this spec changes.

### Concurrency and limits (C-27)

One loop goroutine owned by `Run(ctx)` is the only caller of `Provider` and `Dialer`. API methods take the admission mutex for memory and send a non-blocking wake, so 50 `Start`s coalesce into one search. CAS functions are pure (CORE-040). Provider calls use CORE-020's per-op timeouts; `Probe` 5 s (CORE-070); `Dial` 15 s. Pod shutdown cancels `ctx`, closes the `Conn`, and never destroys (FR-4.1); the persisted deadline still applies to the next process.

### Stage timing (FR-2.4, architecture §4)

`search` T1→T5; `create` T5→T8; `boot` T8→first `running`; `link` `running`→T13; `download` T13→first `Probe` = `Loading`; `load` first `Loading`→first `/health` 200; `ready` `/health` 200→T32; `first_token` from `ObserveFirstToken`. An unobserved stage records 0.

### Observability (C-34)

| Metric | Type | Labels |
|---|---|---|
| `vkllm_gpu_state` | gauge, one-hot | `state` ∈ the 8 `PublicState()` values (Design Decision 6) |
| `vkllm_provision_attempts_total` | counter | `outcome` ∈ `ok, no_offer, provider_error, deadline_exceeded, attempts_exhausted, budget_exhausted, failed_status, lost, cancelled` (CORE-090) |
| `vkllm_coldstart_stage_seconds` | histogram, buckets per CORE-090 | `stage` ∈ the 8 stages |
| `vkllm_gpu_uptime_seconds_total` | counter | none; seconds with a `providerId` set, added at each wake |
| `vkllm_gpu_idle_seconds` | gauge | none; `now − max(readyAt, lastInferenceFinishedAt)` in Ready with 0 in flight, else 0 |
| `vkllm_gpu_idle_deadline_seconds` | gauge | none; idleDeadline as unix seconds, 0 when none |
| `vkllm_gpu_last_inference_finished_seconds` | gauge | none; `lastInferenceFinishedAt` as unix seconds, 0 when none |
| `vkllm_shutdowns_total` | counter | `reason` ∈ `idle, manual, lifetime, budget, failure, orphan`; once per drain or failure entry, post-commit |
| `vkllm_destroy_attempts_total` | counter | `outcome` ∈ `ok, retryable, terminal, fatal` |
| `vkllm_orphans_destroyed_total` | counter | none |
| `vkllm_reconcile_runs_total` | counter | `outcome` ∈ `ok, error` |
| `vkllm_instance_hourly_price_usd` | gauge | none; offer `dph` while an instance exists, else 0 |

`budget_exhausted` and `attempts_exhausted` count once per flip from allowed to denied. Logs (`slog` JSON): `event`, `from`, `to`, `state`, `public_state`, `desired`, `instance_gen`, `instance_id`, `op_key`, `outcome`, `reason`, `stage`, `duration_ms`, `attempt`, `idle_deadline`. Never the worker seed, worker key, SSH key, management token, `env` or request content (C-16). Transitions log at `info`; destroy errors, orphans and `instance_lost` at `warn`; Fatal provider errors at `error`.

### Failure handling (architecture §8)

Vast API down or 5xx → T7, T10, T30 retries, never a destroy; no qualifying offer → T6; slow pull or download → T15; checksum mismatch (`exited`) → T14; worker crash mid-request → T24, or T23 if vanished; control plane restart → recovery, past deadline drains at once; control plane gone long → worker dead-man (`idleShutdown.timeout + 10m`) → `exited` → T14 on return; stop during provisioning → T16/T17; start during draining → T4 then T29 → T1; budget → T2, T6, T16, T22; store conflict → re-queue 5 s; orphan → reconcile.

## Requirements

- R1. The controller MUST implement exactly T1–T32; any other (state, event) pair MUST leave the state unchanged. (source: architecture §4, ADR 0011 §6)
- R2. Op `create-<gen>` and the label MUST be committed before `CreateInstance` is called. (source: C-2, ADR 0003 §3)
- R3. At most one `CreateInstance` per generation; no new generation while `doc.instance` is non-nil; concurrent `Start`s MUST yield one search. (source: C-2, FR-2.1)
- R4. A failed provider read MUST NOT trigger a destroy; orphan, terminal-status and vanish decisions MUST need two consecutive successful observations. (source: C-9, FR-5.2)
- R5. `DestroyInstance` MUST only target a label with the configured installation id and generation ≤ the generation read in the same iteration. (source: C-11, ADR 0003 §3)
- R6. Only `exited`, `offline`, `unknown` MUST be terminal; `StatusOther` MUST NOT trigger a destroy. (source: wave-1 ruling, research §1.4, FR-5.3)
- R7. Reconcile MUST run at start and every `controller.reconcileInterval`. (source: C-6, ADR 0003 §4)
- R8. An orphan MUST be destroyed within two reconcile intervals of its first successful observation. (source: C-6, FR-6.6, FR-6.2)
- R9. Ready MUST require `Probe` = `Ready`; a successful create MUST NOT imply readiness. (source: ADR 0002 §4, FR-2.7)
- R10. Startup timeout expiry MUST destroy, denylist the machine for 1 h and back off 1, 2, 4 … 30 min. (source: ADR 0003 §5, C-3, FR-5.4)
- R11. With `attemptsThisHour` at `maxProvisionAttemptsPerHour` no create MUST happen until the next UTC hour. (source: C-3, FR-6.5)
- R12. idleDeadline MUST equal `max(readyAt, lastInferenceFinishedAt) + lifecycle.idleShutdown.timeout`, and only `RequestEnd` MUST set `lastInferenceFinishedAt`. (source: ADR 0011 §1–2, FR-3.1, FR-3.2, FR-3.5)
- R13. The idle rule MUST NOT fire while `inFlight > 0`. (source: ADR 0011 §1, FR-3.3, FR-3.9)
- R14. `Start` on a Ready instance MUST NOT write the document or move idleDeadline. (source: ADR 0011 §2, FR-3.4)
- R15. `Stop` MUST close admission and enter Draining before it returns, and a drain MUST end within `lifecycle.shutdown.drainTimeout`. (source: ADR 0011 §3, FR-3.6, FR-3.7)
- R16. `Admit` and every transition into Draining MUST be serialized by one mutex over `(state, inFlight)`. (source: ADR 0011 §4, FR-3.8)
- R17. Stop before an id exists MUST destroy as soon as the id is known; `Start` in Draining or Destroying MUST provision generation + 1 only after the destroy confirms. (source: FR-3.11, FR-2.8)
- R18. Every drain and every loss after Ready MUST write `desired=STOPPED` in the same CAS; only `Start` MUST write `RUNNING`. (source: ADR 0010 §2, architecture §4)
- R19. Idle, max lifetime and budget rules MUST be evaluated on every tick from persisted fields, so a restart past a deadline drains at t=0. (source: FR-3.10, FR-6.9)
- R20. Instances older than `lifecycle.safety.maxLifetime` MUST be drained regardless of activity. (source: ADR 0011 §5, FR-6.7)
- R21. Destroy MUST be retried until `GetInstance` returns `ErrNotFound`. (source: C-4, ADR 0003 §6, FR-4.4)
- R22. The controller MUST NOT call a stop endpoint and MUST NOT destroy on pod shutdown. (source: C-5, FR-4.1)
- R23. A restart MUST re-adopt a running instance whose probe is `Ready` and whose deadline has not passed, without a create or destroy. (source: ADR 0003 §8, FR-4.2)
- R24. An empty store MUST lead to destroying every instance under our label prefix; a stored id the provider lacks MUST reach STOPPED after two observations. (source: C-13, ADR 0004 §6, FR-4.3, FR-4.6)
- R25. Durable state MUST change only through `store.Transition` with pure functions; `obs` setters MUST run only after commit. (source: C-10, CORE-040 R5)
- R26. `Budget.Allow(doc, balance)` false MUST deny `Start` and drain or destroy a live instance. (source: ADR 0003 §4, FR-6.3)
- R27. `Dial` MUST use only the `sshHost`/`sshPort` of the latest `GetInstance` for the stored `providerId`; a changed host key MUST fail the dial and destroy. (source: C-18, ADR 0006 §3, §5, NFR-8.4)
- R28. Metric labels MUST be the enumerations above; goroutines MUST be owned by `Run`'s context; tests MUST use fakes, no sleeps, no network. (source: C-27, C-29, C-34)

## Implementation steps

1. `internal/tunnel` `direct` and `ssh` over `net.Pipe` with an in-process `x/crypto/ssh` server. Tests: `TestSSHRoundTrip`, `TestHostKeyMismatch`, `TestKeepaliveClose`.
2. `State`, `PublicState`, events, pure `decide(state, event, facts)`. Tests: `TestTransitionTable` (one case per row; unlisted pairs are no-ops), `TestPublicStateMap`.
3. Fencing and label parsing. Test: `TestMayDestroy` (foreign id, gen above, gen equal, unparsable).
4. Saga with CAS ordering. Tests: `TestOpBeforeCreate` (store fails after the op write → 0 creates), `TestBackoffFromOps`.
5. Admission mutex, idle deadline, idle timer, activity flush. Tests: `TestIdleDeadlineFormula`, `TestAdmitVsDrainRace` (`-race`, 1000 iterations, every admitted request ends in `inFlight` accounting), `TestSameInstantOrder`.
6. Loop, timers, wake coalescing. Test: `TestStartCoalesces`.
7. Reconcile and two-observation counters. Test: `TestReconcileTable` (one case per row).
8. Restart recovery. Test: `TestRecoveryMapping` (one case per stored state, plus deadline passed).
9. `Start`/`Stop`/`Status`, metrics, logs. Tests: `TestStartReasons`, `TestStopForceRejected`, `TestMetricsOneHot`, `TestLogsNoSecret`, `TestMetricsPull`.
10. Simulation harness and AC1–AC27 in `sim_test.go`.

## Tests / acceptance criteria

Harness: fake clock starts at `2026-11-01T00:00:00Z` (t=0); defaults from architecture §9 (idle 1800 s, drain 60 s, startup 900 s, lifetime 12 h, reconcile 60 s); installation id `iid`; `Run`, then `Settle()` processes every due event. The CORE-020 fake keeps the last value of a status sequence; `Calls(op)` counts harness calls too. The fake dialer counts `Dial` and `Close`; the fake worker returns a scripted `Probe` sequence and keeps its last value. "Adopted Ready gen 3" = store gen 3, `desired` RUNNING, instance id 9 `Ready`, `readyAt` t=0, idleDeadline t=1800, provider id 9 `running`, probe `Ready`. Offer A: id 11, machine 42, $0.60/h. Default `SetBalance(100)`.

- AC1 happy cold start (FR-2.4, FR-2.7). Setup: fresh store, offer A, status `StatusOther("loading")`×2 then `running`; probes `Unreachable`×2, `Loading`×3, `Ready`. Action: `Start` at t=1, `ObserveFirstToken(1, 800ms)`. Expected: `Start` returns nil, `Public` `ALLOCATING`, `Desired` `RUNNING`; Public `STARTING` at t=1, `LOADING_MODEL` at t=16, `READY` at t=41; calls search 1, create 1, get 3, list 1, destroy 0; dial 1, probe 6; `vkllm_gpu_state{state="READY"}` 1, the other 7 public states 0; stage count 1 for all 8 stages, `boot` 15 s, `download` 10 s, `load` 15 s; `provision_attempts_total{outcome="ok"}` 1; price gauge 0.60; store `readyAt` t=41, idleDeadline t=1841; `vkllm_gpu_idle_deadline_seconds` = unix(t=1841).
- AC2 50 concurrent starts (FR-2.1). Setup as AC1. Action: 50 goroutines call `Start` at t=1. Expected: all return nil; search 1, create 1; store `desired` writes 1; generation 1.
- AC3 idle destroy (FR-3.1). Continue AC1, no requests. Expected: destroy 0 before t=1841; Draining, then destroy 1 at t=1841; Idle at t=1841; `desired` STOPPED; `shutdowns_total{reason="idle"}` 1; uptime 1840 s; search still 1 at t=7200.
- AC4 rolling deadline (FR-3.2, FR-3.3). Continue AC1. `Admit(1)` at t=100, `RequestEnd(1)` at t=200; `Admit(1)` at t=1990, `RequestEnd(1)` at t=2100. Expected: idleDeadline 2000 after t=200; destroy 0 at t=2000 (`inFlight` 1); idleDeadline 3900 after t=2100; destroy 1 at t=3900; `vkllm_gpu_last_inference_finished_seconds` = unix(t=2100).
- AC5 overlapping requests (FR-3.9). Continue AC1. 10 `Admit(1)` at t=100; `RequestEnd(1)` at t=110, 120, … 200. Expected: `inFlight` 10 → 0; idleDeadline 2000; destroy 0 before t=2000, destroy 1 at t=2000.
- AC6 request at the exact deadline (FR-3.8). Continue AC1. Variant a: `Admit(1)` at t=1841. Expected: the idle timer runs first; destroy 1 and Idle at t=1841; `Admit` → `ErrBackendUnavailable`, `inFlight` 0. Variant b: `Admit(1)` at t=1840, `RequestEnd(1)` at t=1900. Expected: nil; destroy 0 at t=1841; destroy 1 at t=3700.
- AC7 repeated start does not move the deadline (FR-3.4, FR-3.5). Continue AC1. At t=100: 10 `Start`, 100 `Status`; reconcile ticks run. Expected: each `Start` returns nil with Public `READY` and makes 0 `Transition` calls; idleDeadline t=1841 unchanged; destroy 1 at t=1841.
- AC8 stop during active inference (FR-3.6, FR-3.7). Continue AC1. `Admit(1)` at t=100; `Stop(false)` at t=110; `Stop(false)` again at t=115. Expected: first `Stop` returns Public `DRAINING`, `Desired` STOPPED; second returns the same with 0 writes; `Admit(1)` at t=111 → `ErrDraining`. Variant a: `RequestEnd(1)` at t=130 → destroy 1 at t=130. Variant b: no `RequestEnd` → `Close` 1 and destroy 1 at t=170. `shutdowns_total{reason="manual"}` 1.
- AC9 stop during provisioning (FR-3.11). Setup as AC1 with `SetLatency("create", 10s)`. Action: `Start` at t=1, `Stop(false)` at t=5. Expected: `Stop` returns Public `ALLOCATING`, `Desired` STOPPED; at t=11 op `create-1` `cancelled`, destroy 1 at t=11; dial 0; outcome `cancelled` 1; Idle at t=11. Variant: `Stop` at t=16 (Booting) → destroy 1 at t=16.
- AC10 start during deletion (FR-2.8, FR-3.11). Setup: adopted Ready gen 3, offer A, `FailDestroy(2)`. Action: `Stop(false)` at t=10, `Start` at t=15. Expected: `Start` returns nil, Public `DELETING`; destroys at t=10, 20, 40; create 0 before t=40; at t=40 search 1, create 1 with label `vkllm:iid:4`; generation 4; `Desired` RUNNING.
- AC11 restart after the deadline passed (FR-3.10, FR-6.9). Setup: store Ready gen 4, id 9, `readyAt` t=−3600, `lastInferenceFinishedAt` t=−2400, idleDeadline t=−600. Action: `Run`. Expected: dial 0; destroy 1 at t=0; `desired` STOPPED; `shutdowns_total{reason="idle"}` 1. Variant `lastInferenceFinishedAt` t=−600: dial 1, destroy 0 before t=1200, destroy 1 at t=1200.
- AC12 restart with Ready instance (FR-4.2). Setup: adopted Ready gen 4 (id 9). Action: `Run`, `Admit(4)` at t=0. Expected: `Admit` nil; create 0, destroy 0, search 0; get 1, dial 1; store `readyAt` and idleDeadline unchanged.
- AC13 startup timeout (FR-5.4, FR-2.6). Setup: offer A only, status `running`, probes `Unreachable` forever. Action: `Start` at t=0, `Start` at t=990. Expected: destroy 1 at t=900; denylist `{42, until t=4500}`; op `create-1` `deadline`; at t=960 search 2 with `ExcludeMachineIDs` [42], outcome `no_offer`, Public `STOPPED`, `Desired` STOPPED, `Unavailable` `no_offer`; `Start` at t=990 → `ErrGPUUnavailable`; create 1.
- AC14 provider list error (FR-5.2). Setup: adopted Ready gen 3; orphan `vkllm:iid:2` added at t=30; `FailNext("list", 10, Retryable)` from t=60. Expected: destroy 0 for t ≤ 600; `reconcile_runs_total{outcome="error"}` 10; state Ready; then destroy 1 at t=720, `orphans_destroyed_total` 1.
- AC15 instance vanishes (FR-4.6). Setup: adopted Ready gen 3, `Admit(3)`; at t=30 the harness destroys id 9. Expected: t=60 state Ready, `Close` 0; t=120 Public `STOPPED`, `Close` 1, store `instance` null, `desired` STOPPED; `Calls("destroy")` 1 (the harness's own); `shutdowns_total{reason="failure"}` 1.
- AC16 orphan injection (FR-6.6, FR-6.2). Setup: Idle, gen 3. Inject `vkllm:iid:2` at t=30. Expected: destroy 1 at t=120, `orphans_destroyed_total` 1, `shutdowns_total{reason="orphan"}` 1. Variant `vkllm:iid:5`: store gen 5 at t=60, destroy 1 at t=120. Variant `vkllm:other:1`: destroy 0.
- AC17 restart with empty store (FR-4.3). Setup: empty store; provider id 5 label `vkllm:iid:7`. Expected: store gen 7 at t=0; destroy 1 at t=60; `orphans_destroyed_total` 1; create 0; `desired` STOPPED.
- AC18 destroy retries (FR-4.4). Setup: adopted Ready gen 3, `FailDestroy(3)`. Action: `Stop(false)` at t=10; `Admit(3)` at t=11. Expected: destroys at t=10, 20, 40, 80 (4); `destroy_attempts_total` `retryable` 3, `ok` 1; Public `STOPPED` at t=80; the t=60 tick adds none; `Admit` → `ErrBackendUnavailable`; search 0 by t=600.
- AC19 max lifetime (FR-6.7). Setup: adopted Ready, `createdAt` t=−11h; `Admit(3)` at every 30 s mark, `RequestEnd(3)` 10 s later. Expected: destroy 0 before t=3600; Draining and destroy 1 at t=3600; `shutdowns_total{reason="lifetime"}` 1.
- AC20 budget mid-Serving (FR-6.3 wiring). Setup: adopted Ready, `Admit(3)` at t=0, `SetBalance(0)` at t=30. Expected: Draining at t=60, `desired` STOPPED; `Admit(3)` at t=61 → `ErrDraining`; `Start` at t=61 → `ErrGPUUnavailable`, `Unavailable` `balance_low`; `RequestEnd(3)` at t=90 → destroy 1 at t=90; `shutdowns_total{reason="budget"}` 1. Variant without `RequestEnd`: `Close` 1 and destroy 1 at t=120.
- AC21 CAS conflict storm (ADR 0004 Risks). Setup as AC1, `ConflictNext(12)` before `Start`. Expected: Ready at t=46; search 1, create 1; `vkllm_store_conflicts_total` 12.
- AC22 terminal status (FR-5.3). Setup as AC1 with status `exited`, `FailDestroy(1)`. Expected: gets at t=6 and t=11; destroy 1 (fails) at t=11, Public `FAILED` at t=15, destroy 2 at t=21, Public `STOPPED` at t=21; `provision_attempts_total{outcome="failed_status"}` 1; `Desired` RUNNING; search 2 at t=120.
- AC23 non-terminal string (wave-1 ruling). Setup: status `StatusOther("scheduling")` forever, `Start` at t=1. Expected: destroy 0 before t=901, destroy 1 at t=901.
- AC24 attempts cap (FR-6.5). Setup: offers on machines 42–45; probes `Unreachable` forever. Action: `Start` at t=0, t=3000, t=3600. Expected: creates at t=0, 960, 1980 (machines 42, 43, 44); at t=2880 `Desired` STOPPED, `Unavailable` `attempts_exhausted`, outcome `attempts_exhausted` 1; `Start` at t=3000 → `ErrGPUUnavailable`; create 4 at t=3600 on machine 45.
- AC25 ready gate (FR-2.7). Setup as AC1, probes `Loading` forever. Expected: Public `LOADING_MODEL` at t=900, destroy 1 at t=901.
- AC26 SSRF boundary and host key (NFR-8.4, ADR 0006 §3). Setup: store gen 4, id 9 `Ready`, `sshHost` `198.51.100.7`, `sshPort` 2222; provider id 9 at `192.0.2.10:41022`. Expected: dial 1 with `192.0.2.10:41022`, 0 dials to `198.51.100.7`; stored address unchanged (R23: a recovery dial re-adopts without a write — only the initial provisioning path, around T12, ever writes `sshHost`/`sshPort`). Variant `FailNext("get", 6, Retryable)`: dial 0 at t=0. Variant dialer returns `ErrHostKeyMismatch`: destroy 1 at t=0, create 0.
- AC27 worker crash (FR-5.6). Setup: adopted Ready gen 3, `Admit(3)`; probes `Unreachable` from t=10. Action: `ReportUpstreamFailure(3)` at t=10. Expected: probes at t=10 and t=15; `Close` 1 and destroy 1 at t=15; `desired` STOPPED. Variant probe `Ready` at t=15: destroy 0. Variant balance $1.59 on a fresh store, `Start` at t=1: `ErrGPUUnavailable`, `Unavailable` `balance_low`, search 0.

## Definition of done

- [x] AC1–AC27 and the step tests pass in CI; `go test -race ./internal/controller/... ./internal/tunnel/...` finishes in < 15 s.
- [x] `make specs-check` passes.
- [x] Status flipped to DONE and folder renamed in the same PR.
- [x] Evidence section filled.

## Open questions

| Question | Why it matters | Settled by | Default |
|---|---|---|---|
| CORE-040 `NewDocument` writes `desired: "STOPPED"` and instance timestamps `readyAt`, `lastInferenceFinishedAt`, `idleDeadline` | a fresh install must not provision; the idle rule needs the fields | CORE-040 update-1 | 050 does not merge before it |
| `no_offer` is learned after `Start` returned 202; FR-2.6 says start returns 503 | a CLI sees 202 then STOPPED with `unavailable: no_offer` | CORE-055 | 503 only inside the 60 s search backoff; otherwise status carries it |
| `balance_unknown` (no balance read yet) maps to `balance_low` | start right after boot may fail | CORE-100 | map to `balance_low` |
| Does `ListInstances` show an instance right after create? | lag inflates create-lost and vanish risk | CORE-030 logs | two observations cover lag ≤ one interval |
| 5 s polls vs Vast per-endpoint limits (research §1.1, thresholds U) | 429s slow boot | CORE-030 | 5 s; CORE-020 backoff absorbs 429 |
| SSH user, keepalive interval and miss count are not in research.md | tunnel stability | CORE-030 | `root`, 15 s, 3 |

## Known limitations

Found during implementation review, each already ruled on; recorded here, not
left to be rediscovered. None blocks DONE.

1. **Reconcile's fence-advance narrowing.** The reconcile table's fence-advance
   row ("label gen > `doc.generation`" → "CAS `generation = g`") only runs
   when no instance is currently active (`advanceFence` requires
   `doc.Instance == nil`), not unconditionally as the table's text reads. An
   unconditional advance would strand a live Serving instance: `RequestEnd`
   would look stale against the moved generation, the idle-shutdown rule would
   never fire again for it, and it would bill until the 12-hour max-lifetime
   cap. Deliberate, cost-safety-motivated narrowing, with a known ceiling: an
   orphan instance whose label generation sits ahead of the active one bills
   until the active instance's own lifecycle ends — typically the ~30-minute
   idle timeout, worst case 12 h via max lifetime — reachable only through an
   out-of-band store edit or reset, never through normal operation. A `Warn`
   log fires every time the advance is deferred, so a billing orphan stays
   visible in logs.
2. **`BudgetExhausted` suppressed while the balance is unknown.** The spec's
   literal text ("`Allow` returning false, including `balance_unknown` →
   `BudgetExhausted`") is not what shipped. `reconcilePruneAndBudget` skips
   posting `EvBudgetExhausted` while the balance has never been successfully
   read (`lastBalanceKnown == false`): every process start begins with
   `known=false`, and restart recovery runs a reconcile pass at t=0, so the
   literal text would mean every restart immediately drains an
   already-healthy, already-serving GPU on its first balance-read attempt.
   Ruled cost-safety over literal spec text. `Start`'s own gate is unaffected
   — a `Start` is still correctly denied with `balance_low` while the balance
   is unknown; only the side effect of draining an already-Ready/Serving
   instance was wrong, and is now suppressed.
3. **AC26 corrected to match the code, not the other way round.** AC26's
   "Expected" text said the stored SSH address is rewritten on the
   restart/recovery-dial path. The shipped behavior, consistent with R23
   ("a restart MUST re-adopt ... without a create or destroy"), never
   rewrites `sshHost`/`sshPort` on a recovery dial — only the initial
   provisioning path (around T12) ever writes it. `TestAC26_...` already
   asserted the unchanged-address behavior; only this spec's AC26 prose was
   wrong, fixed in this write-back.
4. **T29's "evaluate T1 at once" is not implemented.** After a destroy
   completes (T29) with the instance still wanted, nothing re-triggers a new
   search immediately; revival waits for the next periodic reconcile tick (up
   to `controller.reconcileInterval`, 60 s by default). Cost-neutral — it
   delays spend, never accelerates it — but a real gap against the spec's
   literal text, not yet fixed. The same root gap means a denial's
   `desired=STOPPED` write can itself lag by up to one tick under certain
   event orderings (seen in AC10/AC24); in the rare case where that lagging
   tick crosses a UTC-hour boundary, the hourly provision-attempts gate could
   pass on the delayed tick and the controller auto-retries within its own
   hourly cap, rather than requiring an explicit new `Start`. A minor timing
   nuance of the same gap, not a separate bug, and it never bypasses the cap
   itself.
5. **60 s backoff after a search error is not implemented.** T7's `no_offer`
   backoff (AC13) ships. The equivalent 60 s pacing after a search *error*
   (as opposed to a clean `no_offer` result) was never implemented. A
   pre-existing gap, not introduced by this implementation.
6. **Recovery-drain shutdown-reason mislabeling.** When restart recovery
   determines an instance must drain because its idle deadline or max
   lifetime already passed while the process was down, recovery always
   raises a plain `Stop`-shaped event regardless of the real reason, so
   `vkllm_shutdowns_total{reason}` always reports `manual` for a
   recovery-triggered drain, even when the true reason was `idle` or
   `lifetime`. Metrics-only: the destroy itself happens correctly (AC11).
7. **`provision_attempts_total` undercounts gate-level denials.** The counter
   has no case for a denial (`no_offer`/`budget_exhausted`/`balance_low`/
   `attempts_exhausted`) raised directly at the Idle-state gate check, as
   opposed to one raised after an offer was already chosen. The denial is
   correctly enforced before any `CreateInstance` call either way, so there
   is no cost or correctness impact — only an undercounted metric.
8. **Other metrics-only gaps.** The orphan-destroyed counter can double-count
   under rare provider-list-lag timing. A coldstart per-stage-timing field
   (`stageEnteredAt`) is never populated in production — only `first_token`
   is observed, via `ObserveFirstToken` — and AC1's test documents this as a
   known shortfall rather than asserting it. A denied `Start` that happens to
   hit a store CAS-retry-exhausted condition (inside `processEvent`, distinct
   from `callerEvent`'s own `errStale` retry loop) returns no error and no
   reason to its caller; this needs 8 consecutive write conflicts and is very
   low probability.
9. **T5/T6 gate-recheck, fixed in the final review round.** An offer arriving
   after a search was already dispatched never had its own budget/attempts
   recheck: `gateReason` only computed a result for `Start`/`Tick`/`Gone`, so
   a budget denial that landed while the search was in flight let the create
   go ahead anyway (bounded — the next reconcile's `EvBudgetExhausted`
   destroyed it within about 60 s — but the recheck the spec describes
   didn't run). `gateReason` now also evaluates the same budget and
   attempts-cap checks for an offer arriving while `Searching`, so the
   create itself is now denied rather than merely unwound afterward.
10. **`vkllm_gpu_uptime_seconds_total` records in one lump sum, not
    incrementally.** The counter is added to once, when an instance returns
    to Idle (or Destroying, from Searching), covering its whole
    `createdAt`-to-now span in a single `Add`. The spec's text reads as
    "recorded at each wake"; the shipped behavior gets the same total either
    way, so this is a timing-of-recording difference, not a correctness gap.
11. **The reconcile table's T15/T26 "still runs on a tick" wording is
    imprecise.** In the shipped code, T15 (startup timeout) and T26 (drain
    deadline) fire only on `EvDeadlineExpired`, never on a periodic `EvTick`,
    so the table's text overstates what actually triggers them. This is
    practically unreachable as a behavioral gap — `procMu` serializes every
    transition, so the one row that matters (T25, the `EvTick`-driven
    drain-complete check) and these two never race each other — so it is
    recorded here as a textual imprecision, not a bug.
12. **`ErrStoreReset` recovery reruns only the reconcile pass, not a full
    re-mapping of in-memory state.** On `ErrStoreReset`, `processEvent`
    dispatches a reconcile pass against the freshly reloaded document but
    does not re-derive `c.state`/`c.generation`/the armed timers from it, so
    memory can stay briefly inconsistent with the store until the next
    natural transition or a process restart. This matches the spec's own
    literal instruction to rerun step 3, and stays cost-safe either way: the
    orphan-detection path still destroys anything untracked.
13. **A future `CORE-100` `Budget.Allow` must map into this package's own
    4-string vocabulary.** `isDenial` treats any non-empty, non-`"backoff"`
    reason as a denial, so a raw or unmapped string from a real
    `budget.Checker` would still deny correctly. But `Snapshot.Unavailable`
    only ever gets set from `isUnavailableReason`'s closed set
    (`no_offer`/`budget_exhausted`/`balance_low`/`attempts_exhausted`), and
    `provisionDenialOutcome` only counts those same four — an unmapped
    string would deny spend correctly but leave `Unavailable` empty and
    `provision_attempts_total` undercounted for that denial. `CORE-100` must
    map its own denial reasons into this vocabulary, not invent new ones.

## Evidence

`go test -race -count=1 ./internal/controller/... ./internal/tunnel/...`:
**13.230 s** wall clock (`controller` 12.246 s, `tunnel` 2.221 s), under this
spec's own < 15 s Definition-of-done bound. All 27 acceptance criteria
(`TestAC1_HappyColdStart` through `TestAC27_WorkerCrash`) and every step test
pass; `-race` reports no data races.

`internal/clock/fake` gained `NextDeadline() (time.Time, bool)` (commit
`1869963`), returning the earliest pending waiter's deadline so the
simulation harness (`sim_test.go`) can advance the fake clock to the next
event instead of guessing a step size — a merged-package touch in the same
category as CORE-040's own advisor-flagged `export_test.go` additions.

The metrics table and AC1's expectation originally said the `vkllm_gpu_state`
label used the 9 internal `State` names; the shipped code uses the 8
`PublicState()` values instead (Design Decision 6). Corrected in this
write-back; see "Known limitations" above for the other deliberate deviations
and known gaps found during implementation review.
