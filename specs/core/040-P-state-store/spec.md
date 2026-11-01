---
id: "CORE-040"
status: "DRAFT"
updated: "2026-11-01"
depends_on: ["CORE-010"]
adrs: ["0004", "0003"]
---

# 040 — State store

**Status note:** DRAFT, written 2026-11-01 before any code exists.

**Complexity:** M
**Risk:** medium — every other component trusts this package for its only durable state and for write serialisation
**Estimated effort:** two sessions (6–8 h), including the kind integration job
**Recommended model:** strong — optimistic concurrency, RBAC and schema rules must be exact
**Depends on:** [CORE-010](../010-P-repo-skeleton-and-ci/spec.md)

## Outcome

`internal/store` loads and saves the single state document of ADR 0004 in
the ConfigMap `<release>-state` with compare-and-swap on
`metadata.resourceVersion`. Callers mutate state only through
`store.Transition`, which retries conflicts with jitter, so two writers
racing 1000 transitions on a kind cluster lose no update. The first `Load`
of the chart's empty ConfigMap shell returns a fresh document with the
configured `installationId`; the first `Save` writes it.
Outcomes show as `vkllm_store_conflicts_total` and `vkllm_store_ops_total`.

## Scope

- `internal/store`: the `Document` type, `Version`, the `Store` interface,
  sentinel errors, `Encode`/`Decode` (caps, size guard, schema upgrades),
  `NewDocument`, `Document.IsFresh`, `Transition`, two metrics.
- `internal/store/configmap`: stdlib `net/http` client for one ConfigMap.
- `internal/store/fake`: in-memory `Store` with injectable conflicts and
  failures.
- The RBAC Role contract the chart (CORE-080) renders.
- `internal/config.Load()` with `VKLLM_INSTALLATION_ID` and
  `VKLLM_STATE_CONFIGMAP`; 050/060 add their fields.
- Unit tests, an httptest suite for the API client, and a kind integration
  test in CI.

Excludes: the state machine and the meaning of `instance.state` (CORE-050);
the first-boot sweep of the own prefix `vkllm:<installationId>:`, which
the controller performs (CORE-050); computing the `installationId` default
(chart, CORE-080); ledger month rollover, folding into `totalUsd` and cost
estimation (CORE-100); the worker key (derived, CORE-070); lease and activity
semantics (CORE-060); the chart templates themselves (CORE-080); any
leader election or `replicas > 1` support (ADR 0004, Consequences).

## Design and contracts

### Document

The JSON shape is ADR 0004 §1, plus `hostKeyFingerprint` (ADR 0006 §3),
`ops[].finishedAt`, `ledger.entries[].estUsd` and `machineDenylist`
(FR-5.4, ADR 0003 §5). The worker API key is derived and never stored
(ADR 0006 §4, CORE-070 `WorkerKey`), so the document has no field for it.
Optional timestamps are pointers and encode as `null`. All times are UTC.

```go
package store

const SchemaVersion = 1

type Document struct {
	SchemaVersion    int           `json:"schemaVersion"`
	InstallationID   string        `json:"installationId"`
	Generation       int64         `json:"generation"`
	Desired          int           `json:"desired"`
	Instance         *Instance     `json:"instance"` // nil: no instance record
	Ops              []Op          `json:"ops"`      // newest last, at most 20
	AttemptsThisHour Attempts      `json:"attemptsThisHour"`
	Activity         Activity      `json:"activity"`
	Leases           []Lease       `json:"leases"`
	Ledger           Ledger        `json:"ledger"`
	MachineDenylist  []DenyEntry   `json:"machineDenylist"`
}

type Instance struct {
	ProviderID         string     `json:"providerId"`
	Label              string     `json:"label"` // vkllm:<installation-id>:<generation>
	State              string     `json:"state"` // values owned by CORE-050
	Offer              Offer      `json:"offer"`
	SSHHost            string     `json:"sshHost"`
	SSHPort            int        `json:"sshPort"`
	HostKeyFingerprint string     `json:"hostKeyFingerprint"` // opaque, from internal/tunnel
	CreatedAt          time.Time  `json:"createdAt"`
	ReadyAt            *time.Time `json:"readyAt"`
	LastError          string     `json:"lastError"`
}

type Offer struct {
	MachineID int64   `json:"machineId"`
	GPU       string  `json:"gpu"`
	DPH       float64 `json:"dph"`      // USD per hour
	InetDown  float64 `json:"inetDown"` // unit per research §1.2 (open question Q4)
}

type Op struct {
	Key        string     `json:"key"` // e.g. "create-7"
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt"`
	Outcome    string     `json:"outcome"` // "" while running
}

type Attempts struct {
	Hour  string `json:"hour"` // UTC, layout "2006-01-02T15"
	Count int    `json:"count"`
}

type Activity struct {
	LastRequestAt *time.Time `json:"lastRequestAt"`
}

type Lease struct {
	ID        string    `json:"id"`
	Owner     string    `json:"owner"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type Ledger struct {
	Month    string        `json:"month"`    // UTC, layout "2006-01"
	TotalUSD float64       `json:"totalUsd"` // month to date, folded entries included
	Entries  []LedgerEntry `json:"entries"`  // at most 100, newest last
}

type LedgerEntry struct {
	Gen    int64      `json:"gen"`
	Start  time.Time  `json:"start"`
	End    *time.Time `json:"end"` // null while the instance lives
	DPH    float64    `json:"dph"`
	EstUSD float64    `json:"estUsd"`
}

type DenyEntry struct {
	MachineID int64     `json:"machineId"`
	Until     time.Time `json:"until"`
}

// IsFresh reports a document no instance was ever created from.
func (d Document) IsFresh() bool { return d.Generation == 0 && d.Instance == nil }

// NewDocument returns an empty schema-1 document with `desired` 1 for the configured id.
func NewDocument(installationID string) (Document, error)
```

`installationId` is configured, never generated (ADR 0004 §6): chart value
`installationId`, default `sha256("<namespace>/<release>")[:12]`, passed to
the store constructors. It survives a store loss. `NewDocument` rejects a
value not matching `^[a-z0-9][a-z0-9-]{0,31}$` (no `:`, so the label still
parses on its last `:`); CORE-080's `values.schema.json` uses the same pattern.

Type mapping to CORE-020: `providerId` = `strconv.FormatInt(int64(id), 10)`;
`dph`, `estUsd`, `totalUsd` are USD floats = `float64(provider.Money) / 1e6`.

`IsFresh` is the store's whole contribution to first boot. It is derived,
not a flag, so it stays true across restarts until the controller first
increments `generation`; a crash before the controller's own-prefix sweep
repeats the sweep on the next start instead of skipping it.

### Interface and errors

```go
type Version string // opaque; the ConfigMap's metadata.resourceVersion

type Store interface {
	Load(ctx context.Context) (Document, Version, error)
	Save(ctx context.Context, d Document, v Version) (Version, error)
}

var (
	ErrConflict          = errors.New("store: version conflict")
	ErrRetriesExhausted  = errors.New("store: conflict retries exhausted")
	ErrTooLarge          = errors.New("store: document above 256 KiB")
	ErrCorrupt           = errors.New("store: document unreadable")
	ErrSchemaTooNew      = errors.New("store: schemaVersion newer than binary")
	ErrInstallation      = errors.New("store: stored installationId differs from configured")
	ErrStoreReset        = errors.New("store: document reset during transition")
	ErrStoreMissing      = errors.New("store: state ConfigMap not found")
)

func Transition(ctx context.Context, s Store, fn func(*Document) error) (Document, error)

func Encode(d Document) ([]byte, error) // trim ops to 20, marshal, size guard
func Decode(b []byte) (Document, error) // schema check, upgrades, unmarshal
```

`Load` returns a fresh document on first use and never creates the
ConfigMap (ADR 0004 §3–4). A stored
`installationId` that differs from the configured one returns
`ErrInstallation` and nothing is written: changing the chart value would
strand the old prefix's instances, so an operator decides. `Save` with an
empty `Version` is a programming error and returns an error without I/O.
Both implementations call `Encode` and `Decode`, so caps, size guard and
upgrades behave the same in tests and in the cluster.

### Transition

| Rule | Value |
|---|---|
| Loop | `Load`, `fn(&doc)`, `Save(doc, v)`; return on success |
| Attempts | at most 8 `Load`+`Save` pairs, then `ErrRetriesExhausted` |
| Wait after a conflict | uniform random 10–100 ms, aborted by `ctx`; `vkllm_store_conflicts_total` +1 |
| Retried errors | `ErrConflict` only; every other error returns at once, wrapped with `%w` |
| `fn` | runs once per attempt on a freshly loaded copy; must be pure: no provider call, no network, no channel send |
| No-op | `fn` returning nil without changing the encoded bytes skips `Save` |
| Reset | a reload whose `generation` is below the first attempt's returns `ErrStoreReset` without calling `fn`; the caller's next loop sees `IsFresh` and takes the first-boot path (ADR 0004 §6) |
| Wait in tests | an unexported package variable `wait func(ctx, time.Duration) error` replaced by an instant fake |

### `configmap` implementation

```go
type Config struct {
	APIServer string // https://$KUBERNETES_SERVICE_HOST:$KUBERNETES_SERVICE_PORT
	Namespace string
	Name      string // <release>-state
	TokenPath string
	CAPath    string
	InstallationID string // configured; passed to NewDocument
}
func InCluster(name, installationID string) (Config, error) // env + service-account mount
func New(cfg Config) (*Client, error)
```

`InCluster` reads `KUBERNETES_SERVICE_HOST`, `KUBERNETES_SERVICE_PORT` and
`/var/run/secrets/kubernetes.io/serviceaccount/{token,ca.crt,namespace}`.
The integration test builds `Config` by hand against kind. No `client-go`
(ADR 0004 §3).

| Call | Request | Response handling |
|---|---|---|
| Load | `GET /api/v1/namespaces/{ns}/configmaps/{name}` | 200 → `Decode(data["state.json"])`, version = `metadata.resourceVersion`; no `state.json` key (the chart's empty shell) → `NewDocument(cfg.InstallationID)` with the shell's version; empty value → `ErrCorrupt`; other `installationId` → `ErrInstallation` |
| Load, absent | GET 404 | `ErrStoreMissing`, message `ConfigMap <release>-state not found: install via the chart or create an empty ConfigMap`; no POST, ever |
| Save | `PUT /api/v1/namespaces/{ns}/configmaps/{name}` with `metadata.resourceVersion` = `v` | 200 → new version; 409 → `ErrConflict`; 404 → `ErrStoreMissing` |
| any | 401 | re-read the token file, repeat the request once; second 401 is terminal |
| any | 403 | terminal error naming the verb (RBAC is wrong) |
| any | 5xx, timeout, transport error | wrapped retryable error; the caller's loop retries next tick |

A 404 means the ConfigMap is gone. The client cannot recreate it (no
`create` verb); `ErrStoreMissing` fails `/readyz` until the empty shell
is restored, then the fresh-document path (C-13) runs. If the shell reappears between two
attempts of one `Transition`, the reset rule stops `fn` from running on
it. Two writers initialising one shell race on its `resourceVersion` and
one gets `ErrConflict`, as for any write. The token is read on every
401 because the mounted token rotates. Each request carries a 10 s timeout
(C-27). TLS trusts only the mounted CA. The PUT body is the ConfigMap as
read, with `data` set to the one key `state.json`; metadata the chart set
(labels, annotations) is sent back unchanged.

### `fake` implementation

```go
type Fake struct{ /* mutex, encoded bytes, int version */ }
func New(installationID string) *Fake
func (f *Fake) FailNext(n int, err error) // next n Load/Save calls return err
func (f *Fake) ConflictNext(n int)         // next n Saves return ErrConflict
func (f *Fake) Raw() []byte                // stored bytes, for assertions
func (f *Fake) Put(d Document)             // seed state, bumps version
```

The fake stores encoded bytes, not a struct, so tests exercise `Encode` and
`Decode`. Its version is a decimal counter; a `Save` with a stale version
returns `ErrConflict` exactly like the API server.

### Schema evolution

`Decode` reads `schemaVersion` first. Above `SchemaVersion` →
`ErrSchemaTooNew`; 0 or missing → `ErrCorrupt`. Below → apply
`upgrades[v]` for each `v` up to the current version, then unmarshal. The
table is `map[int]func(map[string]any) error`, keyed by the version it
upgrades from, and is empty at version 1. An upgraded document is persisted
by the next successful `Save` (ADR 0004 §5). The store never writes over a
document it could not decode: `ErrCorrupt` and `ErrSchemaTooNew` stop every
`Transition` until an operator deletes the ConfigMap, which takes the
fresh-document path.

### Size and caps

| Item | Bound | Enforced by |
|---|---|---|
| Encoded document | 256 KiB; above → `ErrTooLarge`, no request sent | `Encode` |
| `ops` | last 20 kept, oldest dropped | `Encode` |
| `ledger` | current month; at most 100 entries, older closed entries folded into `totalUsd` | CORE-100 (ADR 0004 Consequences) |
| `leases`, `machineDenylist` | expired entries removed by their owner's `fn` | CORE-060, CORE-050 |

The store never rejects a long ledger (that would also block destroy
records); the 256 KiB guard is the backstop. At the caps it is < 32 KiB (AC10).

### RBAC

The chart renders this Role when `rbac.create` is true (ADR 0008 §4), and
the ConfigMap itself as an empty shell (CORE-080). It is ADR 0004 §3:
exactly `get` and `update` on one name. No `create`, `patch`, `delete`,
`list` or `watch`.

```yaml
kind: Role
rules:
  - apiGroups: [""]
    resources: ["configmaps"]
    resourceNames: ["<release>-state"]
    verbs: ["get", "update"]
```

The kind test uses a token for exactly this Role: a missing verb fails CI,
and `create`, `patch`, `delete` and `list` must answer 403.

### Concurrency

- The controller and the gateway share one `Store` value built in `main`.
- Every mutation goes through `Transition`. A raw `Save` outside this
  package is a review failure.
- In-flight counts and the pending queue never enter the document (C-12).

### Observability

| Metric | Type | Labels |
|---|---|---|
| `vkllm_store_conflicts_total` | counter | none |
| `vkllm_store_ops_total` | counter | `op` = `load`, `save`, `transition`; `outcome` = `ok`, `conflict`, `exhausted`, `reset`, `too_large`, `corrupt`, `installation`, `missing`, `error` |

`main` registers the package's collectors with the `obs` registry and
appends `store.Ready(s)` to the `/readyz` checks of CORE-010: a `Load`
with a 2 s timeout whose error fails readiness, so `ErrCorrupt`,
`ErrSchemaTooNew`, `ErrInstallation`, `ErrStoreMissing` and a 403 are visible as 503 on
`/readyz`. Log fields on failure: `op`, `outcome`, `attempt`,
`schema_version`, `bytes`. Document contents are never logged.

## Requirements

- R1. All writes MUST send the version from the preceding `Load`; the store MUST NOT write blind. (source: ADR 0004 §2, C-10)
- R2. `Save` with a stale version MUST return `ErrConflict` in both implementations. (source: ADR 0004 §4)
- R3. `Transition` MUST retry only `ErrConflict`, at most 8 attempts, waiting 10–100 ms with jitter between attempts. (source: ADR 0004 Risks)
- R4. `Transition` MUST NOT call `Save` when `fn` returns an error or leaves the encoded document unchanged. (source: ADR 0004 §2)
- R5. Every mutation of durable state MUST go through `Transition`. (source: ADR 0003 §2, architecture §7)
- R6. `Load` on a ConfigMap without `state.json` MUST return a document with `generation` 0, `desired` 1, no instance and the configured `installationId`; it MUST NOT generate one. (source: ADR 0004 §3, §6, C-13, FR-4.5)
- R6a. The client MUST NOT create the ConfigMap; GET or PUT 404 MUST return `ErrStoreMissing` with the operator message above. (source: ADR 0004 §3)
- R7. `Document.IsFresh` MUST return true exactly when `generation` is 0 and `instance` is nil. (source: ADR 0004 §6, FR-4.5)
- R8. The store MUST NOT perform the provider sweep; it only reports freshness. (source: ADR 0004 §6, CORE-050)
- R9. A reload inside `Transition` whose `generation` is below the first attempt's MUST return `ErrStoreReset` without calling `fn`. (source: ADR 0004 §6, ADR 0003 §3)
- R9b. `Load` MUST return `ErrInstallation` and write nothing when the stored `installationId` differs from the configured one. (source: ADR 0004 §6, C-11)
- R10. On 401 the client MUST re-read the token file and repeat the request once. (source: ADR 0004 §3)
- R11. The client MUST use only `net/http`, the mounted CA and the mounted token; it MUST NOT import `client-go`. (source: ADR 0004 §3, C-25)
- R12. `Encode` MUST reject documents above 256 KiB with `ErrTooLarge` before any request. (source: ADR 0004 Risks)
- R13. `Encode` MUST keep at most the 20 newest `ops`. (source: ADR 0004 Consequences)
- R14. `Decode` MUST refuse `schemaVersion` above the binary's with `ErrSchemaTooNew` and MUST NOT overwrite that document. (source: ADR 0004 §5)
- R15. Lower schema versions MUST be upgraded in code on read and persisted on the next write. (source: ADR 0004 §5)
- R16. The document MUST NOT contain any secret or derived key, including the worker API key. (source: ADR 0006 §4, C-14, C-16)
- R17. The document MUST NOT contain in-flight counts or the pending queue. (source: ADR 0004 §1, C-12)
- R18. The Role MUST grant exactly `get` and `update` on ConfigMap `<release>-state` by `resourceNames`, and nothing else. (source: ADR 0004 §3, C-20)
- R19. `Store` MUST be the only interface in the package, with exactly the `configmap` and `fake` implementations. (source: C-26)
- R20. Unit tests MUST use the fake store and the injectable wait; no sleeps, no network. (source: C-29)
- R21. Metric labels MUST be the bounded enumerations listed above. (source: C-34)
- R22. Every API request MUST carry a timeout of 10 s or less. (source: C-27)

## Implementation steps

1. Types, `NewDocument`, `IsFresh`, `Encode`, `Decode` with the empty upgrade table. Tests: `TestRoundTrip`, `TestIsFresh`, `TestOpsCapped`, `TestTooLarge`, `TestCappedDocumentSize`, `TestNewDocumentRejectsBadID`, `TestSchemaTooNew`, `TestUpgradeChain` (injects a synthetic 1→2 table via an unexported decode).
2. `fake` package. Tests: `TestFakeStaleVersion`, `TestFakeInjectedFailure`.
3. `Transition` and metrics. Tests: `TestTransitionRetriesConflict`, `TestTransitionExhausted`, `TestTransitionFnErrorNoSave`, `TestTransitionNoopNoSave`, `TestTransitionCtxCancel`, `TestTransitionStoreReset`, `TestReadyCheck`.
4. `configmap` client against `httptest.NewTLSServer`. Tests: `TestLoadKeylessShellIsFresh`, `TestLoadMissing404`, `TestSaveSendsResourceVersion`, `TestSave409Conflict`, `TestSave404Missing`, `TestTokenRefreshOn401`, `TestForbiddenTerminal`, `TestCorruptData`, `TestInstallationMismatch`.
5. Role manifest under `test/kind/` for the integration job, mirrored by CORE-080. Test: `TestKindRBAC` (integration tag) gets and updates the shell with the Role's token and gets 403 on `create`, `patch`, `delete` and `list`.
6. Kind integration job in CI (`go test -tags integration ./internal/store/...`). Test: `TestKindRace`.

## Tests / acceptance criteria

- AC1. Setup: fake store, `ConflictNext(3)`. Action: one `Transition` incrementing `generation`. Expected: success, `generation` 1, `vkllm_store_conflicts_total` 3, 4 Saves attempted. (ADR 0004 Risks)
- AC2. Setup: `ConflictNext(8)`. Action: one `Transition`. Expected: `ErrRetriesExhausted`, 8 Saves, `vkllm_store_ops_total{op="transition",outcome="exhausted"}` 1, stored bytes unchanged.
- AC3. Setup: fake store. Action: `fn` returns an error; then `fn` changes nothing. Expected: 0 Saves in both cases; stored version unchanged.
- AC4. Setup: httptest server whose GET returns a ConfigMap with no `data` and `resourceVersion` `"7"`. Action: `Load`, then `Save`. Expected: `Load` returns `generation` 0, `desired` 1, `instance` null, `installationId` the configured `4f2c1a9b0e77`, `IsFresh()` true, version `"7"`; the PUT carries `resourceVersion` `"7"` and `data["state.json"]`; 0 POSTs. GET 404 instead: `ErrStoreMissing` with the operator message, 0 POSTs. (FR-4.5)
- AC5. Setup: fake seeded with `generation` 3 and no instance. Action: `IsFresh`. Expected: false. Seeded with `generation` 0: true.
- AC6. Setup: httptest server. Action: `Save` with version `"41"`. Expected: PUT body `metadata.resourceVersion` equals `"41"`; server answer 409 yields `errors.Is(err, ErrConflict)`; answer 404 yields `ErrStoreMissing`.
- AC7. Setup: httptest server that answers 401 until the token file content changes; test rewrites the file. Action: `Load`. Expected: 2 GETs, the second with the new bearer token, result 200. A server answering 401 twice with an unchanged file yields an error after exactly 2 requests.
- AC8. Setup: document with 25 ops. Action: `Encode`, `Decode`. Expected: 20 ops, the newest 20.
- AC9. Setup: document whose encoding exceeds 262,144 bytes. Action: `Save` on the httptest client. Expected: `ErrTooLarge`, 0 requests received.
- AC10. Setup: a full `instance`, 100 closed ledger entries, 20 ops, 10 leases, 10 denylist entries. Action: `Encode`. Expected: size below 32,768 bytes; the size is logged and recorded in Evidence. (ADR 0004 Risks)
- AC11. Setup: bytes with `schemaVersion` 2. Action: `Load` then `Transition`. Expected: `ErrSchemaTooNew`, 0 PUTs. Bytes with no `schemaVersion`: `ErrCorrupt`.
- AC12. Setup: kind cluster, `kubectl create ns` for the test, an empty ConfigMap `<release>-state`, the Role and a ServiceAccount token bound to it. Action: two writers, each with its own client, run 1000 `Transition` calls that increment `generation` and append to a writer-local list of observed values; exhausted calls are retried by the test. Expected: final `generation` 2000; each writer's observed values strictly increasing; `vkllm_store_conflicts_total` > 0; zero 403 responses. (C-10, ADR 0004 §2)
- AC13. Setup: kind, Role token, store at `generation` 3. Action: delete the ConfigMap; `Transition`; recreate the empty shell with `kubectl create configmap`; `Transition`. Expected: first call `ErrStoreMissing` and `store.Ready` fails; second call succeeds on a document with the same `installationId`, `generation` 0 before `fn`, `IsFresh()` true. (C-13, FR-4.5)
- AC14. Setup: fake at `generation` 5. Action: one `Transition` whose `fn` calls `Put` with a fresh document (the reset) on its first run. Expected: `ErrStoreReset`, `fn` called exactly once, `vkllm_store_ops_total{op="transition",outcome="reset"}` 1.
- AC15. Setup: fake holding `installationId` `aaaa`, store configured with `bbbb`. Action: `Load`. Expected: `ErrInstallation`, stored bytes unchanged; `store.Ready` returns that error.
- AC16. `go list -deps ./internal/store/...` contains no `k8s.io/` module. (ADR 0004 §3)

## Definition of done

- [ ] AC1–AC16 pass in CI; AC12–AC13 run in the kind job.
- [ ] `make specs-check` passes; measured AC10 size recorded below.
- [ ] Status flipped to DONE in the same PR (folder `040-D-state-store`); Evidence filled.

## Open questions

| Question | Why it matters | Settled by | Default |
|---|---|---|---|
| `research.md` has no Kubernetes API section: request paths, `resourceVersion` semantics, 409 on stale PUT, keyless ConfigMap shape, the service-account mount paths, token rotation, the 1 MiB ConfigMap limit | rule: no external fact without a research citation | kind integration job (AC12, AC13, `TestKindRBAC`); then add a research section | values from ADR 0004 §2–3 and this spec |

## Evidence

None yet.
