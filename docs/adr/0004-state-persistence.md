# ADR 0004: State is one ConfigMap document written with optimistic concurrency

## Status

Accepted (2026-11-01). Supersedes the brief's working assumption of
PostgreSQL after the discussion recorded below. Re-evaluated against the
2026-11-01 update, which again prefers PostgreSQL; the decision stands
(ADR 0010 Context). Document fields amended by [ADR 0011](0011-rolling-inactivity-timeout.md):
`desired` is `RUNNING|STOPPED`; `instance` gains `readyAt`,
`lastInferenceFinishedAt`, `idleDeadline`; `leases` is removed; a `cache`
field is reserved for [ADR 0014](0014-persistent-volume-cache-and-preferred-host.md).

## Context

The brief asks to evaluate PostgreSQL for instance metadata, operations,
pending requests, activity, leases, errors and cost accounting, and to
consider whether all of it is needed in the MVP. With `maxInstances = 1`
the whole state is one instance record, a short list of recent operations,
one timestamp, a handful of leases and a month of cost entries: a few
kilobytes written a few times a minute. The platform's shared PostgreSQL
exists but has no per-application database mechanism, and depending on it
would make the Helm chart unusable without a database.

The user first chose PostgreSQL, then asked what would actually be stored
and whether Kubernetes Leases would do. A Lease holds one holder and one
timestamp, so it cannot carry the record. The Kubernetes API can.

## Decision

### 1. The store

One ConfigMap, `<release>-state`, in the release namespace, with a single
key `state.json` holding a versioned JSON document:

```json
{
  "schemaVersion": 2,
  "installationId": "4f2c1a9b0e77",
  "generation": 7,
  "desired": "RUNNING",
  "instance": {
    "providerId": "12345678", "label": "vkllm:4f2c1a9b0e77:7", "state": "Ready",
    "offer": {"machineId": 1, "gpu": "L40S", "dph": 0.71, "inetDown": 850},
    "sshHost": "ssh5.vast.ai", "sshPort": 12345,
    "createdAt": "…", "readyAt": "…", "lastInferenceFinishedAt": "…", "idleDeadline": "…",
    "lastError": ""
  },
  "ops": [{"key": "create-7", "startedAt": "…", "outcome": "ok"}],
  "attemptsThisHour": {"hour": "2026-11-01T14", "count": 1},
  "ledger": {"month": "2026-11", "totalUsd": 3.42, "entries": [{"gen": 7, "start": "…", "end": null, "dph": 0.71}]},
  "cache": null
}
```

(Schema 2 as amended by ADR 0011; schema 1 had `desired` 0/1, `activity`
and `leases`.)

In-flight request counts stay in process memory; they are meaningless
across a restart.

### 2. Concurrency

Every write is a read-modify-write that sends back the ConfigMap's
`metadata.resourceVersion`. The API server rejects a stale write with 409
Conflict; the writer re-reads and re-applies the transition. This is the
only locking primitive. It replaces `SELECT … FOR UPDATE`. The fencing token
against the provider is the `generation` carried in the instance label
(ADR 0003).

### 3. Access

A stdlib `net/http` client against the in-cluster API server
(`KUBERNETES_SERVICE_HOST`, the mounted service-account token and CA), two
verbs on one resource. No `client-go`. The chart renders the ConfigMap
`<release>-state` as an empty shell (no `data`), so Helm owns its lifetime
(`helm uninstall` removes it) and the controller never needs `create`,
which Kubernetes RBAC cannot scope by name. The store treats a present but
keyless ConfigMap as a fresh document. A namespaced Role grants exactly
`get` and `update` on that one name. No `create`, `patch`, `delete` or
`list`. Helm's three-way merge leaves `data` written by the controller
untouched on upgrade because the template never sets it.

### 4. Interface

```go
type Store interface {
    Load(ctx context.Context) (Document, Version, error)   // a keyless ConfigMap yields a fresh document
    Save(ctx context.Context, d Document, v Version) (Version, error) // ErrConflict on stale version
}
```

Two implementations: `configmap` and an in-memory `fake` with injectable
conflicts for tests. A `Transition(ctx, func(*Document) error)` helper
performs the retry loop so callers never handle `ErrConflict` themselves.

### 5. Schema evolution

`schemaVersion` is checked on load; upgrades are in-code functions applied
on read and persisted on the next write. No migration tool.

### 6. Loss and rebuild

The ConfigMap dies with its namespace. The `installationId` is **not**
generated: it is the chart value `installationId`, default
`sha256("<namespace>/<release>")[:12]`, so it survives a store loss and is
distinct per installation. On an empty store the controller creates a fresh
document with that id, lists provider instances by its own prefix
`vkllm:<installationId>:` and destroys them all (it cannot know which
generation was live, and a running instance it cannot re-adopt is an
orphan). It never touches another installation's prefix, so an acceptance
install on a laptop against the same provider account cannot destroy the
platform's instance. The ledger restarts, so month-to-date spend after a
rebuild is undercounted; the provider's prepaid balance remains the hard
floor (spec 100). Operators who want history keep it in Prometheus.

## Alternatives considered

- **PostgreSQL** (shared `vkdb` schema, goose migrations, `FOR UPDATE`).
  Correct and familiar, but oversized for the data, adds a client library,
  a migration tool, a platform dependency and a failure mode (DB down → no
  GPU control) to protect a few kilobytes. Recorded as the upgrade path for
  `replicas > 1` or for long-term reporting.
- **Kubernetes Lease alone.** Cannot hold the record.
- **ConfigMap + Lease.** The Lease adds nothing at `replicas: 1` with
  `Recreate`; CAS already serialises writers.
- **SQLite on a PersistentVolume.** Pins the pod to a node and a volume for
  no benefit.
- **No durable state, provider as the only truth.** Loses the ledger, leases
  and the idle clock on every restart. Cheap, but the ConfigMap costs almost
  nothing more and keeps them.

## Rationale

The state is tiny and the writer is single. The Kubernetes API already
provides durability, optimistic concurrency and RBAC; using it keeps the
chart dependency-free and the code small.

## Consequences

- No SQL, no migrations, no connection pool. The `Store` package is a few
  hundred lines including the API client.
- Document size must stay small: `ops` keeps the last 20 entries; the ledger
  keeps a running `totalUsd` for the month plus the 100 most recent entries,
  older entries are folded into the total (spec 100).
- Horizontal scaling later needs either leader election or a store with
  row-level semantics; the `Store` interface is the seam.

## Risks

- A write storm (many CAS retries) under a bug. Mitigation: a bounded retry
  count with jitter and a metric `vkllm_store_conflicts_total`.
- ConfigMap size limit (1 MiB) if a future field grows unbounded.
  Mitigation: the caps above, a 256 KiB guard in `Save`, and a test that 100
  ledger entries plus 20 ops stay under 32 KiB.
