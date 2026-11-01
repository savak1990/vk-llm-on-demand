# ADR 0011: A 30-minute rolling inactivity timeout and immediate manual stop govern every GPU

## Status

Accepted (2026-11-01). Amends [ADR 0003](0003-gpu-lifecycle-management.md)
§5 (idle rule, lifetime, drain) and §3 of its state names; removes
workflow leases from ADR 0003 and ADR 0007.

## Context

The brief makes this a P0 property: `vk-llm start` must never start a GPU
indefinitely; every instance is destroyed after a configurable period
without inference, 30 minutes by default; the period is rolling, not a hard
maximum; only genuine inference counts; manual stop begins shutdown at
once. ADR 0003 had a 20-minute rule measured from the start of the last
request, workflow leases that could hold the GPU, and a 6-hour lifetime.
Vast bills per second while the instance is active, and destroying is the
only teardown that stops billing (ADR 0003 §6, research §1.5).

## Decision

### 1. The deadline

```
idleDeadline = max(readyAt, lastInferenceFinishedAt) + lifecycle.idleShutdown.timeout
```

- `lifecycle.idleShutdown.timeout` defaults to **30m**.
- `readyAt` is set once when the state becomes `Ready` (health 200 and
  the model alias listed). The first deadline is `readyAt + 30m`.
- `lastInferenceFinishedAt` is set by `RequestEnd` for every admitted
  inference request, whether it finished, failed, was cancelled, or its
  stream disconnected. With overlapping requests the last `RequestEnd`
  wins by construction.
- While `inFlight > 0` the idle rule never fires. A request admitted
  before the deadline keeps the GPU; when it ends, the deadline moves to
  `now + 30m`.
- The controller evaluates the rule on every reconcile tick and on a timer
  armed at the deadline; the tick makes the rule restart-safe because the
  three timestamps are persisted (ADR 0004).

### 2. What never counts as activity

`vk-llm start` on a running instance, `vk-llm status`, `vk-llm stop`,
`GET /api/v1/gpu/status`, `GET /api/v1/gpu/cost`, `GET /v1/models`,
`/healthz`, `/readyz`, `/metrics`, reconcile runs, GitHub Actions polling,
repeated lifecycle calls. None of these write `lastInferenceFinishedAt` or
`readyAt`. Only `Admit` + `RequestEnd` on `/v1/chat/completions` do.

### 3. Manual stop

`POST /api/v1/gpu/stop` sets `desired = STOPPED` and transitions **at
once** to `Draining`: no new admissions; in-flight requests get
`lifecycle.shutdown.drainTimeout` (**60s**) to finish; then `Deleting`
(destroy, retried to confirmation) and `Stopped`. Stop never waits for the
idle deadline. `force=true` (future, documented): skip the drain and close
in-flight streams with an in-stream error; it requires the management
token and `lifecycle.shutdown.allowForce: true`. Stop during
`Allocating`/`Starting`/`LoadingModel` cancels: the instance is destroyed
as soon as a provider id exists. Stop is idempotent.

### 4. Atomic admission

Admission (`Admit`) and the transitions into `Draining` share one
in-process mutex over `(state, inFlight)`. A request admitted before the
transition is drained; a request arriving after it receives
`backend_unavailable`. This single rule covers a request racing automatic
shutdown, a request racing manual stop, and a request at the exact
deadline. Concurrent requests only ever increase `inFlight`, so they cannot
cause premature termination.

### 5. Removed and changed safeguards

- **Workflow leases are removed.** No lease endpoints, no `leases` field,
  no lease demand. The rolling timeout is the only activity mechanism.
- **`lifecycle.safety.maxLifetime`** defaults to **12h** (a deliberate
  change from 6h) and is a safeguard against a runaway session, never
  the activity rule. It drains like a manual stop.
- **`lifecycle.safety.maxInstanceSpendUSD`** (default 0 = off) bounds one
  instance's estimated spend; with it off the bound is
  `maxLifetime × maxHourlyPriceUSD`.
- **`lifecycle.startupTimeout`** (15m, was `gpu.startupDeadline`) and
  `maxProvisionAttemptsPerHour` (3) are unchanged in meaning.
- **`lifecycle.idleShutdown.enabled`** is true by default; the chart
  refuses `false` unless `maxLifetime` is set, so no configuration can
  rent a GPU without a stop condition.
- The worker's dead-man timer (ADR 0003 §7) stays at
  `idleShutdown.timeout + 10m`, counted from the worker's own last request.

### 6. States

Names align with the brief; the controller keeps the finer stages as
sub-states for metrics:

| Public state | Controller stages |
|---|---|
| `STOPPED` | Idle |
| `ALLOCATING` | Searching, Creating |
| `STARTING` | Booting (link up) |
| `LOADING_MODEL` | Loading (health 503) |
| `READY` | Ready, Serving (inFlight > 0) |
| `DRAINING` | Draining |
| `DELETING` | Destroying |
| `FAILED` | a terminal provider status or startup timeout, until the destroy confirms, then `STOPPED` |

## Alternatives considered

- **Count the start of a request** (previous rule). A long generation
  could end after the deadline computed from its start; counting the end
  is what the brief specifies and what keeps a working session alive.
- **Leases for agents.** Explicitly rejected by the brief; they let a
  client hold the GPU without inference.
- **A hard 30-minute runtime.** Contradicts the brief's example of a
  three-hour session.
- **Wait for idle on manual stop.** Contradicts the brief; the user asked
  for the compute to be released.

## Rationale

Measuring from readiness and from the end of the last inference matches
how a person uses a coding assistant: bursts of requests, then silence.
Persisting the three timestamps makes the rule survive restarts; one mutex
makes it race-free; immediate manual stop makes cost an explicit choice.

## Consequences

- ADR 0003 §5's idle rule, lease clamp and 6h lifetime are replaced by
  this ADR; §2's state names map per §6 above.
- ADR 0004's document gains `readyAt`, `lastInferenceFinishedAt`,
  `idleDeadline` and `desired: RUNNING|STOPPED`; `leases` is removed.
- Spec 050 implements the rule with a fake clock; spec 140 proves it on a
  real instance with a shortened timeout.
- Status output exposes `idleDeadline` and `secondsUntilIdleShutdown`.

## Risks

- A client that streams forever holds the GPU. Mitigation:
  `model.generationTimeout` (10m) ends any single request; `maxLifetime`
  ends the instance.
- Clock skew between restarts. Mitigation: deadlines are recomputed from
  persisted timestamps, never from stored timers.
