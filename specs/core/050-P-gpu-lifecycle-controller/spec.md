---
id: "CORE-050"
status: "DRAFT"
updated: "2026-11-01"
depends_on: ["CORE-020", "CORE-040"]
adrs: ["0003", "0001", "0002", "0004", "0005", "0006", "0007"]
---

# 050 — GPU lifecycle controller

**Status note:** DRAFT, written 2026-11-01 before any code exists.

**Complexity:** L
**Risk:** high — this package decides when money is spent and when a live instance is destroyed
**Estimated effort:** three to four sessions (14–18 h), half of it the simulation harness and the tunnel
**Recommended model:** strong — fencing, the two-observation rule and the saga are cost-safety critical
**Depends on:** [CORE-020](../020-P-vastai-provider-client/spec.md), [CORE-040](../040-P-state-store/spec.md)

## Outcome

`internal/controller` runs the single-instance state machine of ADR 0003 §2. A demand from the gateway starts exactly one instance; the controller drives it through boot, link and model load, holds the SSH tunnel and hands the gateway its `RoundTripper`, and destroys the instance on idle, max lifetime, budget, kill switch, startup deadline, worker failure or a terminal provider status. Every 60 s it reconciles the provider's label list against the state document, destroys orphans, and after a restart re-adopts a Ready instance or converges from the provider. A deterministic simulation (fake provider, store, clock, dialer) proves it with exact call counts; `vkllm_gpu_state`, `vkllm_coldstart_stage_seconds` and the counters below make it visible.

## Scope

- `internal/controller`: `State`, events, transition table, provisioning saga, loop, reconcile, restart
  recovery, the gateway API (demand, requests, leases, desired, status), metrics, logs.
- `internal/tunnel`: `Dialer`, `Conn`, implementations `ssh` and `direct` (ADR 0006).
- `internal/controller/sim_test.go`: the simulation harness and AC1–AC22.

Excludes: provider HTTP calls and the offer filter (CORE-020); `Load`/`Save`/`Transition` (CORE-040);
`Probe`, `WorkerKey`, `BuildCreate` (CORE-070); ledger arithmetic, rollover and `budget.Checker`
(CORE-100); `internal/clock` and its fake (CORE-010); request queueing, heartbeats and HTTP status mapping (CORE-060); alert rules (CORE-090);
`maxInstances > 1` (ADR 0003 §2).

## Design and contracts

### States and persistence

`type State int`: `Idle, Searching, Creating, Booting, Loading, Ready, Serving, Draining, Destroying`;
`String()` gives the names stored in `store.Instance.State` and used as the `state` metric label.

| Item | Where | Why |
|---|---|---|
| `Idle`, `Searching` | `doc.Instance == nil`; `Searching` is memory only | no provider mutation yet; a restart resumes as Idle |
| `Creating`…`Destroying` | `doc.Instance.State` | a provider instance may exist |
| `Serving` | memory only; stored as `Ready` | a CAS per request breaks NFR-10.3 (ADR 0003 §2) |
| in-flight count, waiters, the tunnel `Conn`, suspect flag | memory (C-12) | lost on restart by design |
| two-observation counters, drain start, stage timestamps, search backoff | memory | a restart re-observes; drain restarts with 0 in flight |
| create backoff | derived from trailing `create-*` ops in `doc.Ops` | survives restart without a new field |
| `desired` (admin switch, sticky), `generation`, denylist, attempts, leases, `lastRequestAt` | document (CORE-040) | `lastRequestAt` flushed at most once per 10 s, piggybacked on any CAS |

### Events and transition table

Events: `Demand`, `OfferChosen`, `NoOffer`, `Created`, `CreateFailed`, `LinkUp`, `HealthOK`, `RequestStart`,
`RequestEnd`, `Tick`, `ProviderObserved(status)`, `Vanished`, `WorkerFailed`, `DeadlineExpired`,
`DesiredZero`, `DesiredOne`, `BudgetExhausted`, `Gone`. Definitions:
- **Wanted** = `desired=1` AND (waiters > 0 OR an unexpired lease OR `now − lastRequestAt < gpu.idleTimeout`).
- **Gate** = budget ok (the last tick's `Budget.Allow(doc, balance)`) AND (`attemptsThisHour.hour` ≠ current UTC hour OR `count < gpu.maxProvisionAttemptsPerHour`)
  AND now ≥ next create time (saga step 7) AND now ≥ next search time.
- **Idle rule** = `now − max(lastRequestAt, readyAt) ≥ gpu.idleTimeout` AND in-flight 0 AND no unexpired lease.
- **Twice** = two consecutive successful observations (C-9); failed reads neither count nor reset.

| # | From | Event | Guard | To | Side effect (CAS first, then call) |
|---|---|---|---|---|---|
| T1 | Idle | Demand / Tick | wanted, gate ok | Searching | `SearchOffers(filter, denylist)` |
| T2 | any | Demand | `desired=0`, budget denied, or (Idle and attempts exhausted) | same | `Demand` returns `ErrUnavailable` from the caller's goroutine; outcome `budget_exhausted`/`attempts_exhausted` (edge-triggered); a backoff wait is not a denial |
| T3 | any | DesiredOne | — | same | CAS `desired=1`; nothing else (warm-up is `Demand`) |
| T4 | Searching | OfferChosen | gate ok | Creating | CAS `generation++`, instance `{Creating, label, offer, createdAt}`, op `create-<gen>` outcome `""`, `attemptsThisHour` = `{current hour, count+1}` (restarts at 1 in a new hour); then `CreateInstance` |
| T5 | Searching | NoOffer / search error / gate denied | — | Idle | close waiters, failure `gpu_unavailable`; next search ≥ now+60 s; outcome `no_offer`, `provider_error`, `budget_exhausted`, `attempts_exhausted` |
| T6 | Creating | Created(id) | op `create-<gen>` still `""` | Booting | CAS `providerId`, op `created`, `budget.OpenEntry(doc, now, gen, dph)`; post-commit metrics |
| T7 | Creating | CreateFailed(Terminal/Fatal, not duplicate) or `AssertPrice` error | — | Idle | CAS instance nil, op `create_error`; close waiters, `gpu_cold_start`; outcome `provider_error`; Fatal logs at `error` |
| T8 | Creating | CreateFailed(Retryable) | — | Creating | op stays `""`; saga step 4 resolves it |
| T9 | Creating | CreateFailed(`ErrDuplicateLabel`) | — | Destroying | destroy every instance with the exact label |
| T10 | Booting | ProviderObserved(running, ssh set) | — | Booting | CAS `sshHost`, `sshPort`; `Dial` with that same `GetInstance` result |
| T11 | Booting | LinkUp | — | Loading | CAS state, `hostKeyFingerprint`; probe at once, then every 5 s |
| T12 | Loading | HealthOK | `Probe` = `Ready` | Ready | CAS `readyAt`, op `ready` + `finishedAt`; send `Ready` to waiters; outcome `ok` |
| T13 | Creating…Draining | ProviderObserved(`exited`/`offline`/`unknown`) twice | — | Destroying | close `Conn`; op `failed_status` if not final; outcome `failed_status` if before Ready |
| T14 | Creating/Booting/Loading | DeadlineExpired | `now − createdAt ≥ gpu.startupDeadline` | Destroying | op `deadline`; denylist `{machineId, now+1h}`; close waiters, `gpu_cold_start`; outcome `deadline_exceeded` |
| T15 | Creating/Booting/Loading | DesiredZero / BudgetExhausted | — | Destroying | op `cancelled`; close waiters, `gpu_unavailable`; outcome `cancelled` |
| T16 | Ready | RequestStart(gen) | gen current | Serving | in-flight 0→1 (memory) |
| T17 | Serving | RequestEnd(gen) | in-flight 1→0 | Ready | `lastRequestAt=now` (memory) |
| T18 | Ready | Tick | idle rule | Draining | CAS state |
| T19 | Ready/Serving | Tick | `now − createdAt ≥ gpu.maxLifetime` AND (no unexpired lease OR `now − createdAt ≥ maxLifetime + 30m`) | Draining | CAS state |
| T20 | Ready/Serving | BudgetExhausted / DesiredZero | — | Draining | CAS state (and `desired=0` for DesiredZero) |
| T21 | Ready/Serving | Vanished (absent twice) | — | Idle | close `Conn` (in-flight → 502); CAS instance nil, `budget.CloseEntry(doc, now, gen)`; post-commit metrics; log `instance_lost` |
| T22 | Ready/Serving | WorkerFailed: two non-`Ready` probes, or `ErrHostKeyMismatch` | — | Destroying | close `Conn`; `lastError`; log `instance_lost` |
| T23 | Draining | RequestEnd / entry | in-flight 0 | Destroying | |
| T24 | Draining | DeadlineExpired | `gpu.drainTimeout` since drain start | Destroying | close `Conn` |
| T25 | Creating…Loading, Draining | Vanished (absent twice) | — | Idle | CAS instance nil, `budget.CloseEntry(doc, now, gen)`, post-commit metrics; op `lost` if not final; outcome `lost` if before Ready |
| T26 | Destroying | entry / retry timer | fencing holds | Destroying | CAS op `destroy-<gen>`; `DestroyInstance`; on nil `GetInstance` |
| T27 | Destroying | Gone (`ErrNotFound`) | — | Idle | CAS instance nil, op `destroy-<gen>` `ok`, `budget.CloseEntry(doc, now, gen)`; post-commit metrics; price gauge 0; if wanted, evaluate T1 at once |
| T28 | Destroying | destroy or get error, or still present | — | Destroying | retry after `min(5m, 10s·2^n)`; `vkllm_destroy_attempts_total{outcome}` = the provider error class (`retryable`, `terminal`, `fatal`); still present after a nil destroy counts `retryable` |
| T29 | any | Demand | not T2 | same, or T1 | `lastRequestAt=now`; register waiter (also while Idle waits for backoff); Ready/Serving send at once |
| T30 | Idle/Searching | DesiredZero | — | Idle | CAS `desired=0`; close waiters, `gpu_unavailable` |

`ProviderObserved(StatusOther(raw))` is never a transition: only the startup deadline (T14) or vanish
(T21/T25) ends it (wave-1 ruling, CORE-020). `desired=0` is sticky until `SetDesired(1)`. A guard that no
longer holds inside the CAS `fn` returns `errStale`; the event is dropped and the loop re-reads. Any
(state, event) pair not in the table leaves the state unchanged.

### Fencing (C-11)

Every CAS `fn` checks that `doc.Generation` and `doc.Instance.Label` equal the values the event was raised
for, else `errStale`. `DestroyInstance(id)` runs only when the instance's label parses as
`vkllm:<installationId>:<g>` with the configured `installationId` (ADR 0004 §6 as amended) and
`g ≤ doc.Generation` from the `Load` of the same loop iteration. Unparsable labels are logged, never destroyed.

### Provisioning saga

1. **Search.** `SearchOffers` with the config filter, `MaxHourly` = `gpu.maxHourlyPriceUSD`,
   `ExcludeMachineIDs` = unexpired `machineDenylist` (expired entries pruned in the next CAS). First offer wins.
2. **Record before acting (C-2).** T4 commits op `create-<gen>` and label `vkllm:<installationId>:<gen>` before
   any provider call. The request is `worker.BuildCreate` with `WorkerKey(adminKey, installationId, gen)`.
   Before the call, `budget.AssertPrice(offer.Hourly.USD(), cfg.MaxHourly)`; an error takes T7 (outcome
   `provider_error`, counted as an attempt) and `CreateInstance` is not called.
3. **Create.** `CreateInstance` (CORE-020 lists and adopts inside one call). Result → T6, T7, T8 or T9.
4. **Unknown outcome** (op `""` and no `providerId`, seen at a poll, a tick or a restart): `ListInstances`
   with the exact label as prefix. One match → T6 (adopt). Several → adopt the lowest id; the rest are
   orphans. Zero on two consecutive successful lists → CAS op `create_lost`, instance nil, Idle, outcome `lost`; a late
   instance with that label is then an orphan. List error → next poll. The startup deadline bounds this step.
5. **Boot and link.** Every 5 s `GetInstance(providerId)` until `running` with `sshHost`/`sshPort`; then
   `Dialer.Dial` with the address from that result, never from the document (NFR-7.4). Failure → next poll.
6. **Readiness.** Every 5 s `worker.Client{HTTP: conn.RoundTripper(), Key, Alias}.Probe`: `Unreachable`
   and `Loading` stay (FR-3.1); `Ready` → T12; probe errors stay.
7. **Backoff.** n = trailing `create-*` ops whose final outcome is not `ready`; next create not before the
   last one's `finishedAt + min(30m, 1m·2^(n−1))` (ADR 0003 §5). `ready` resets n to 0.

### Reconcile, every `controller.reconcileInterval`

One `Load`, one `ListInstances("vkllm:<installationId>:")`, one `Provider.Balance` (on error reuse the last
good value; with none since start, deny with reason `balance_unknown`), then:

| Provider says | Store says | Action |
|---|---|---|
| list error | any | no destroy decision; `reconcile_runs_total{outcome="error"}`; time rules (T14, T18, T19, T20, T24) still run |
| label gen > `doc.generation` | — | CAS `generation = g` (fence advanced); counts as the first orphan observation |
| our prefix, id ≠ `providerId`, no pending op for its gen | — | orphan: twice → `DestroyInstance` + `GetInstance`, `orphans_destroyed_total++`; repeated each tick until absent |
| pending op gen matches | Creating, no id | saga step 4 |
| `exited`/`offline`/`unknown` | Creating…Draining | `ProviderObserved` → T13 (ADR 0003 §4 "mark Failed") |
| absent | known with id | `Vanished` after twice → T21/T25 |
| running | known | Ready/Serving: one `Probe`; address changed → redial via step 5 |

The tick's CAS prunes expired leases and denylist entries, then runs `budget.Rollover`,
`budget.RefreshOpenEntry` and `Budget.Allow(doc, balance)` (false → `BudgetExhausted`), then the idle rule
and max lifetime. These budget calls are pure document mutations. **Post-commit metrics:** after
`Transition` commits, and only then, the loop sets from the committed document: `obs.SetBudgetUsedRatio(Budget.UsedRatio(doc))`,
`obs.SetCostInstanceUSD(<open entry EstUSD or 0>)`, `obs.AddCostEstimatedUSD(<sum of each entry's EstUSD
increase versus the previous committed document, ignoring rollover drops>)`, `obs.SetProviderBalanceUSD(balance)`;
no CAS function calls an `obs` setter, so conflict retries never double-count. It never issues a destroy for a state already
`Destroying`; T26 owns that timer.

### Timing model (the simulation relies on it)

| Timer | Cadence | Active in |
|---|---|---|
| reconcile | t=0 (recovery pass), then every `controller.reconcileInterval` | always |
| poll | every 5 s from entry | Creating with op `""`, Booting, Loading |
| suspect probe | at `ReportUpstreamFailure` and 5 s later | Ready/Serving |
| startup deadline | `createdAt + gpu.startupDeadline` | Creating…Loading |
| drain deadline | drain start + `gpu.drainTimeout` | Draining |
| destroy retry | `min(5m, 10s·2^n)` after a failed round | Destroying |
| metrics pull | every 15 s; `worker.Client.Metrics` → `Exporter`; on error `obs.IncInferenceScrapeErrors()` | Ready/Serving |
| activity flush | 10 s after the first unflushed `lastRequestAt`, unless a CAS carried it | any |

Same-instant order: deadlines, destroy retry, reconcile, poll, external events. One loop goroutine; a CAS
ending in `ErrRetriesExhausted` re-queues the same event 5 s later (the search is not repeated).

### Restart recovery (ADR 0003 §8)

1. `Load` (a keyless shell yields a fresh document; `ErrStoreMissing` is retried every 5 s and fails
   `/readyz`). `store.ErrInstallation`: `Run` returns it and nothing is destroyed. `ErrStoreReset` from
   `Transition` mid-run: drop the event and rerun step 3 (reconcile pass).
2. Map the stored state: no instance and op `""` → saga step 4; `Creating` with id, `Booting`, `Loading`
   → resume at Booting (poll, dial, probe; deadline still from `createdAt`); `Ready` → `GetInstance`,
   `Dial`, `Probe`: `Ready` re-adopts without a write, otherwise two non-`Ready` probes 5 s apart → T22;
   `Draining` → Destroying (in-flight is 0); `Destroying` → T26.
3. Run the reconcile pass at t=0. An empty store (generation 0) plus an instance under our prefix is the
   "gen above" row, so it is destroyed at the second tick (FR-4.5).
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
func (c *Conn) Close() error                    // in-flight requests on it fail; the gateway answers 502
func (c *Conn) HostKey() string                 // fingerprint seen at dial; the controller persists it (T11)
type Dialer interface{ Dial(ctx context.Context, in Instance) (*Conn, error) }
var ErrHostKeyMismatch = errors.New("tunnel: host key changed")
```

| Item | `ssh` | `direct` |
|---|---|---|
| Used when | `tunnel.dialer=ssh` (default) | `tunnel.dialer=direct` (kind job only) |
| Transport | `golang.org/x/crypto/ssh` client to `Host:Port`, user `root`, key `id_ed25519` from `existingSecrets.ssh` read per dial; one `direct-tcpip` channel to `127.0.0.1:8080` per HTTP connection | plain TCP to `tunnel.directAddr`; `Instance` ignored |
| Host key | `HostKey` empty → accept, report fingerprint; mismatch → `ErrHostKeyMismatch`, no channel opened (ADR 0006 §3) | none |
| Timeouts | dial and handshake 15 s; `keepalive@openssh.com` every 15 s, 3 misses close the `Conn` | dial 15 s |

The controller opens one `Conn` per generation in Booting, holds it through Loading, Ready and Serving,
and closes it on every exit from those states and on shutdown.

### Gateway API

```go
type Ready struct {
    Generation int64             // label generation
    Transport  http.RoundTripper // Conn.RoundTripper(); fails after the controller closes the Conn
}
var (
    ErrUnavailable   = errors.New("controller: gpu unavailable") // desired=0, budget, attempts
    ErrDraining      = errors.New("controller: not admitting")   // stale gen, or not Ready/Serving
    ErrLeaseNotFound = errors.New("controller: lease not found")
)
func New(d Deps) (*Controller, error)
func (c *Controller) Run(ctx context.Context) error
func (c *Controller) Demand(ctx context.Context) (<-chan Ready, error) // buffered 1: one send then close, or close without a value on failure; ctx end drops the waiter only
func (c *Controller) RequestStart(gen int64) error
func (c *Controller) RequestEnd(gen int64)
func (c *Controller) ReportUpstreamFailure(gen int64)              // marks suspect: probe now and 5 s later
func (c *Controller) ObserveFirstToken(gen int64, d time.Duration) // first_token stage, once per generation
func (c *Controller) CreateLease(ctx context.Context, owner string, ttl time.Duration) (store.Lease, error)
func (c *Controller) RenewLease(ctx context.Context, id string, ttl time.Duration) (store.Lease, error) // same TTL and clamp; ErrLeaseNotFound
func (c *Controller) ReleaseLease(ctx context.Context, id string) error
func (c *Controller) SetDesired(ctx context.Context, n int) error   // 0 or 1, persisted; 0 holds until 1
func (c *Controller) Status() Snapshot

type Deps struct {
    Provider       provider.Provider
    Store          store.Store
    Dialer         tunnel.Dialer
    Clock          clock.Clock
    Budget         *budget.Checker                 // CORE-100; Allow(doc, balanceUSD)
    AdminKey       func() ([]byte, error)          // for worker.WorkerKey, read per use
    InstallationID string                          // configured (ADR 0004 §6 as amended)
    Config         config.Controller
    Reg            prometheus.Registerer
    Log            *slog.Logger
}
type Snapshot struct {
    State State; Generation int64; Since time.Time; InFlight, Waiters, Leases int
    InstallationID, LabelPrefix string; Desired int
    Unavailable string     // "", no_offer, budget, attempts, desired_zero
    LeaseList []store.Lease; SpendMTDUSD float64 // budget.EstimatedMTD
    ETA time.Duration       // 0 = unknown: last successful cold start's stage sum minus time elapsed in the current one
    Stages map[string]time.Duration; ProviderID provider.InstanceID; Hourly provider.Money
    CreatedAt time.Time; ReadyAt *time.Time; LastError string; AttemptsThisHour int; NextCreateAt time.Time
    FailureCode string // "gpu_unavailable" | "gpu_cold_start" | "": why the last waiters were closed without a value
}
```

Lease TTL is 1 s–2 h (ADR 0007 §3), else an error; `expiresAt` is clamped at creation and renewal to
`createdAt + gpu.maxLifetime + 30m` when an instance exists (ADR 0003 §5). A lease counts as demand. The
worker dead-man (CORE-070) still ends an instance after `worker.deadManTimeout` with no inference, lease
or not; leases protect against the controller's idle rule only.
`Status` copies memory under a mutex and never calls the provider.

`clock.Clock` and the fake are CORE-010's (`internal/clock`).

### Concurrency and limits (C-27)

One loop goroutine owned by `Run(ctx)` is the only caller of `Provider` and `Dialer`. Gateway methods take
a mutex for memory and send a non-blocking wake on a channel of size 1, so 50 demands coalesce into one
wake. CAS functions are pure (CORE-040). Provider calls use CORE-020's per-op timeouts; `Probe` 5 s
(CORE-070); `Dial` 15 s. Shutdown cancels `ctx`, closes the `Conn`, and never destroys (FR-4.1).

### Stage timing (FR-2.4, architecture §4)

`search` T1→T4; `create` T4→T6; `boot` T6→first `running`; `link` `running`→T11; `download` T11→first
`Probe` = `Loading`; `load` first `Loading`→first `/health` 200 (`Ready`, or `Loading` with `alias
missing`); `ready` `/health` 200→T12; `first_token` from `ObserveFirstToken`. An unobserved stage records 0.

### Observability (C-34)

| Metric | Type | Labels |
|---|---|---|
| `vkllm_gpu_state` | gauge, one-hot | `state` ∈ the 9 `State` names |
| `vkllm_provision_attempts_total` | counter | `outcome` ∈ `ok, no_offer, provider_error, deadline_exceeded, attempts_exhausted, budget_exhausted, failed_status, lost, cancelled` (CORE-090) |
| `vkllm_coldstart_stage_seconds` | histogram, buckets per CORE-090 | `stage` ∈ the 8 stages |
| `vkllm_gpu_uptime_seconds_total` | counter | none; seconds with a `providerId` set, added at each wake |
| `vkllm_gpu_idle_seconds` | gauge | none; idle-rule age in Ready with 0 in flight, else 0 |
| `vkllm_destroy_attempts_total` | counter | `outcome` ∈ `ok, retryable, terminal, fatal` |
| `vkllm_orphans_destroyed_total` | counter | none |
| `vkllm_reconcile_runs_total` | counter | `outcome` ∈ `ok, error` |
| `vkllm_instance_hourly_price_usd` | gauge | none; offer `dph` while an instance exists, else 0 |

`budget_exhausted` and `attempts_exhausted` count once per flip from allowed to denied. Logs (`slog` JSON): `event`, `from`,
`to`, `state`, `instance_gen`, `instance_id`, `op_key`, `outcome`, `reason`, `stage`, `duration_ms`,
`attempt`. Never the admin key, worker key, SSH key, `env` or request content (C-16). Transitions log at
`info`; destroy errors, orphans and `instance_lost` at `warn`; Fatal provider errors at `error`.

### Failure handling (architecture §8)

| Failure | Transition |
|---|---|
| Vast API down or 5xx | T5, T8, T28 retry; reconcile `provider_error`, never a destroy |
| No qualifying offer | T5, `no_offer`, `gpu_unavailable` |
| Slow pull or download | T14, denylist 1 h, backoff |
| Checksum mismatch (`exited`) | T13 |
| Worker crash mid-request | `ReportUpstreamFailure` → T22, or T21 if the instance vanished |
| Control plane restart | restart recovery steps 1–4 |
| Control plane gone long | worker dead-man → `exited` → T13 on return |
| Budget reached | T2, T5, T15, T20 |
| Store conflict | `Transition` retries; exhausted → re-queue 5 s |
| Orphan instance | reconcile orphan rows |

## Requirements

- R1. The controller MUST implement exactly T1–T30; any other (state, event) pair MUST leave the state unchanged. (source: ADR 0003 §2)
- R2. Op `create-<gen>` and the label MUST be committed before `CreateInstance` is called. (source: C-2, ADR 0003 §3)
- R3. At most one `CreateInstance` per generation; no new generation while `doc.instance` is non-nil. (source: C-2, FR-2.1, FR-2.5)
- R4. A failed provider read MUST NOT trigger a destroy; orphan, terminal-status and vanish decisions MUST need two consecutive successful observations. (source: C-9, FR-5.2)
- R5. `DestroyInstance` MUST only target a label with the configured installation id and generation ≤ the generation read in the same iteration. (source: C-11, ADR 0003 §3)
- R6. Only `exited`, `offline`, `unknown` MUST be terminal; `StatusOther` MUST NOT trigger a destroy. (source: wave-1 ruling, research §1.4, FR-5.3)
- R7. Reconcile MUST run at start and every `controller.reconcileInterval`. (source: C-6, ADR 0003 §4)
- R8. An orphan MUST be destroyed within two reconcile intervals of its first successful observation. (source: C-6, FR-6.6)
- R9. Ready MUST require `Probe` = `Ready`; `Loading` and `Unreachable` MUST keep the state. (source: ADR 0002 §4, FR-3.1)
- R10. Startup deadline expiry MUST destroy, denylist the machine for 1 h and back off 1, 2, 4 … 30 min. (source: ADR 0003 §5, C-3, FR-5.4)
- R11. With `attemptsThisHour` at `gpu.maxProvisionAttemptsPerHour` no create MUST happen until the next UTC hour. (source: C-3, FR-6.5)
- R12. Idle, max lifetime, budget and `desired=0` MUST drain, and a drain MUST end within `gpu.drainTimeout`. (source: ADR 0003 §5, FR-4.2, FR-4.3, FR-6.7)
- R13. Destroy MUST be retried until `GetInstance` returns `ErrNotFound`. (source: C-4, ADR 0003 §6, FR-4.6)
- R14. The controller MUST NOT call a stop endpoint and MUST NOT destroy on shutdown. (source: C-5, FR-4.1)
- R15. A restart MUST re-adopt a running instance whose probe is `Ready` without a create or destroy. (source: ADR 0003 §8, FR-4.4)
- R16. An empty store MUST lead to destroying every instance under our label prefix. (source: C-13, ADR 0004 §6, FR-4.5)
- R17. Durable state MUST change only through `store.Transition` with pure functions. (source: C-10, CORE-040 R5)
- R18. `Budget.Allow(doc, balance)` false MUST deny provisioning and drain or destroy a live instance. (source: ADR 0003 §4, FR-6.3)
- R19. All eight stages MUST be observed per cold start. (source: ADR 0005 §4, FR-2.4)
- R20. `Dial` MUST use only the `sshHost`/`sshPort` of the latest `GetInstance` for the stored `providerId`. (source: C-18, ADR 0006 §5, NFR-7.4)
- R21. A changed host key MUST fail the dial and destroy the instance. (source: ADR 0006 §3)
- R22. `desired=0` MUST persist and block provisioning until `SetDesired(1)`. (source: ADR 0007 §3)
- R23. Metric labels MUST be the enumerations above; goroutines MUST be owned by `Run`'s context; tests MUST use fakes, no sleeps, no network. (source: C-27, C-29, C-34)

## Implementation steps

1. `internal/tunnel` `direct` and `ssh` over `net.Pipe` with an in-process `x/crypto/ssh` server. Tests: `TestSSHRoundTrip`, `TestHostKeyMismatch`, `TestKeepaliveClose`.
2. `State`, events, pure `decide(state, event, facts)`. Test: `TestTransitionTable` (one case per row; unlisted pairs are no-ops).
3. Fencing and label parsing. Test: `TestMayDestroy` (foreign id, gen above, gen equal, unparsable).
4. Saga with CAS ordering. Tests: `TestOpBeforeCreate` (store fails after the op write → 0 creates), `TestBackoffFromOps`.
5. Loop, timers, wake coalescing, activity flush. Test: `TestSameInstantOrder`.
6. Reconcile and two-observation counters. Test: `TestReconcileTable` (one case per row).
7. Restart recovery. Test: `TestRecoveryMapping` (one case per stored state).
8. Gateway API, leases, `Status`, metrics, logs. Tests: `TestLeaseClamp`, `TestDemandErrors`, `TestMetricsOneHot`, `TestLogsNoKey`, `TestMetricsPull` (15 s pull; error increments scrape errors).
9. Simulation harness and AC1–AC22 in `sim_test.go`.

## Tests / acceptance criteria

Harness: fake clock starts at `2026-11-01T00:00:00Z` (t=0); defaults from architecture §9; installation id
`iid`; `Run`, then `Settle()` processes every due event. The CORE-020 fake keeps the last value of a status
sequence; `Calls(op)` counts harness calls too. The fake dialer counts `Dial` and `Close`; the fake worker
returns a scripted `Probe` sequence and keeps its last value. "Adopted Ready gen 3" = store gen 3, instance
id 9 `Ready`, provider id 9 `running`, probe `Ready`. Offer A: id 11, machine 42, $0.60/h. Default `SetBalance(100)`.

- AC1 happy cold start (FR-2.4, FR-3.1). Setup: fresh store, offer A, status `StatusOther("loading")`×2 then `running`; probes `Unreachable`×2, `Loading`×3, `Ready`. Action: `Demand` at t=1, `ObserveFirstToken(1, 800ms)`. Expected: `Ready{Generation: 1}` at t=41; calls search 1, create 1, get 3, list 1, destroy 0; dial 1, probe 6; `vkllm_gpu_state{state="Ready"}` 1, the other 8 states 0; stage count 1 for all 8 stages, `boot` 15 s, `download` 10 s, `load` 15 s; `provision_attempts_total{outcome="ok"}` 1; price gauge 0.60; store op `create-1` `ready`.
- AC2 50 concurrent demands (FR-2.1, FR-2.5). Setup as AC1. Action: 50 goroutines call `Demand` at t=1. Expected: search 1, create 1; all 50 channels deliver `Generation` 1 at t=41.
- AC3 idle destroy (FR-4.2). Continue AC1, no requests. Expected: destroy 1 at t=1260 (idle reference 41 + 1200, next tick); Idle at t=1260; `destroy_attempts_total{outcome="ok"}` 1; uptime 1259 s; search still 1 at t=3600.
- AC4 lease (FR-4.3). Continue AC1; `CreateLease("ci", 30m)` at t=41. Expected: destroy 0 by t=1260; destroy 1 at t=1860.
- AC5 startup deadline (FR-5.4). Setup: offer A only, status `running`, probes `Unreachable` forever, `Demand` every 60 s from t=0. Expected: destroy 1 at t=900; denylist `{42, until t=4500}`; op `create-1` `deadline`; waiters closed without a value at t=900 with `FailureCode` `gpu_cold_start`; at t=960 search 2 with `ExcludeMachineIDs` [42] and outcome `no_offer`; create 1.
- AC6 provider list error (FR-5.2). Setup: adopted Ready gen 3; orphan `vkllm:iid:2` added at t=30; `FailNext("list", 10, Retryable)` from t=60. Expected: destroy 0 for t ≤ 600; `reconcile_runs_total{outcome="error"}` 10; state Ready; then destroy 1 at t=720, `orphans_destroyed_total` 1.
- AC7 instance vanishes. Setup: adopted Ready gen 3, `RequestStart(3)`; at t=30 the harness destroys id 9. Expected: t=60 state Ready, `Close` 0; t=120 state Idle, `Close` 1, store `instance` null; `Calls("destroy")` 1 (the harness's own).
- AC8 orphan injection (FR-6.6, FR-6.2). Setup: Idle, gen 3. Inject `vkllm:iid:2` at t=30. Expected: destroy 1 at t=120, `orphans_destroyed_total` 1. Variant `vkllm:iid:5`: store gen 5 at t=60, destroy 1 at t=120. Variant `vkllm:other:1`: destroy 0.
- AC9 restart with Ready instance (FR-4.4). Setup: adopted Ready gen 4 (id 9). Action: `Run`, `Demand` at t=0. Expected: `Ready{Generation: 4}` at t=0; create 0, destroy 0, search 0; get 1, dial 1; store gen 4, id 9, state `Ready`.
- AC10 restart with empty store (FR-4.5). Setup: empty store; provider id 5 label `vkllm:iid:7`. Expected: store gen 7 at t=0; destroy 1 at t=60; `orphans_destroyed_total` 1; create 0.
- AC11 destroy retries, kill switch (FR-4.6). Setup: adopted Ready gen 3, `FailDestroy(3)`. Action: `SetDesired(0)` at t=10; `Demand` at t=100. Expected: destroys at t=10, 20, 40, 80 (4); `destroy_attempts_total` `retryable` 3, `ok` 1; Idle at t=80; the t=60 tick adds none; `Demand` returns `ErrUnavailable`; search 0 by t=600.
- AC12 max lifetime with lease (FR-6.7). Setup: adopted Ready, `createdAt` = t−5h; `RequestStart`/`RequestEnd` every 30 s; `CreateLease("ci", 2h)` at t=0. Expected: stored `expiresAt` t+5400; destroy 0 at t=3600; destroy 1 at t=5400.
- AC13 budget mid-Serving (FR-6.3). Setup: adopted Ready, `RequestStart(3)` at t=0, `SetBalance(0)` at t=30 (below the CORE-100 floor). Expected: Draining at t=60; `RequestStart(3)` at t=61 → `ErrDraining`; `Demand` at t=61 → `ErrUnavailable`; `RequestEnd(3)` at t=90 → destroy 1 at t=90. Variant without `RequestEnd`: `Close` 1 and destroy 1 at t=360.
- AC14 CAS conflict storm (ADR 0004 Risks). Setup as AC1, `ConflictNext(12)` before `Demand`. Expected: `Ready` at t=46; search 1, create 1; `vkllm_store_conflicts_total` 12.
- AC15 terminal status (FR-5.3). Setup as AC1 with status `exited`. Expected: gets at t=6 and t=11, destroy 1 at t=11; `provision_attempts_total{outcome="failed_status"}` 1; search 2 at t=120.
- AC16 non-terminal string (wave-1 ruling). Setup: status `StatusOther("scheduling")` forever, `Demand` at t=1. Expected: destroy 0 before t=901, destroy 1 at t=901.
- AC17 attempts budget (FR-6.5). Setup: offers on machines 42–45; probes `Unreachable` forever; `Demand` every 60 s from t=0. Expected: creates at t=0, 960, 1980 (machines 42, 43, 44); every `Demand` from t=2880 to t=3540 → `ErrUnavailable`; `provision_attempts_total{outcome="attempts_exhausted"}` 1; create 4 at t=3600 on machine 45.
- AC18 ready gate (FR-3.1). Setup as AC1, probes `Loading` forever. Expected: state Loading at t=900, destroy 1 at t=901.
- AC19 SSRF boundary (NFR-7.4). Setup: store gen 4, id 9 `Ready`, `sshHost` `198.51.100.7`, `sshPort` 2222; provider id 9 at `192.0.2.10:41022`. Expected: dial 1 with `192.0.2.10:41022`, 0 dials to `198.51.100.7`; store address rewritten. Variant with `FailNext("get", 6, Retryable)`: dial 0 at t=0.
- AC20 worker crash (FR-5.6). Setup: adopted Ready gen 3, `RequestStart(3)`; probes `Unreachable` from t=10. Action: `ReportUpstreamFailure(3)` at t=10. Expected: probes at t=10 and t=15; `Close` 1 and destroy 1 at t=15. Variant with probe `Ready` at t=15: destroy 0.
- AC21 host key change (ADR 0006 §3). Setup: adopted Ready gen 3, dialer returns `ErrHostKeyMismatch`. Action: `Run`. Expected: destroy 1 at t=0; create 0.
- AC22 balance floor (FR-6.3, research §1.5). Setup: fresh store, offer A, `SetBalance(1.59)`. Action: `Demand` at t=1. Expected: `ErrUnavailable`; search 0, create 0; `Status().Unavailable` `budget`.

## Definition of done

- [ ] AC1–AC22 and the step tests pass in CI; `go test -race ./internal/controller/... ./internal/tunnel/...` finishes in < 15 s.
- [ ] `make specs-check` passes.
- [ ] Status flipped to DONE and folder renamed in the same PR.
- [ ] Evidence section filled.

## Open questions

| Question | Why it matters | Settled by | Default |
|---|---|---|---|
| CORE-040 `NewDocument` must write `desired: 1` and the configured `installationId` | a fresh install would otherwise never provision | CORE-040 amendment | 050 does not merge before it |
| Does `ListInstances` show an instance right after create? | lag inflates create-lost and vanish risk | CORE-030 logs | two observations cover lag ≤ one interval |
| 5 s polls vs Vast per-endpoint limits (research §1.1, thresholds U) | 429s slow boot | CORE-030 | 5 s; CORE-020 backoff absorbs 429 |
| SSH user, keepalive interval and miss count are not in research.md | tunnel stability | CORE-030 | `root`, 15 s, 3 |

## Evidence

None yet.
