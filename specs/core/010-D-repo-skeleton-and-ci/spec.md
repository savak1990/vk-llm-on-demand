---
id: "CORE-010"
status: "DONE"
updated: "2026-11-01"
depends_on: ["CORE-000"]
adrs: ["0003", "0004", "0008", "0012", "0013"]
---

# 010 — Repo skeleton, Go module and CI gate

**Status note:** DONE. Implemented and merged; `make specs-check` and the Definition of done below are satisfied, with Evidence filled in from the real repository.

**Complexity:** M — many small files, no algorithmic logic.
**Risk:** low — copies a proven template (vk-ahorro); no GPU, no money, no secrets touched.
**Estimated effort:** one session (3–5 h)
**Recommended model:** standard — mechanical pattern-copy and wiring, no novel design decision.
**Depends on:** CORE-000 (constitution)

## Outcome

A fresh checkout builds (`make go-build`), lints clean (`make go-lint`), and runs one binary that serves `:8080` and `:9090`, answers `/healthz` and `/readyz` on `:9090`, and shuts down within 30 s of `SIGTERM`. A pull request against `main` shows at least the jobs `changes`, `go`, `image`, `helm`, `workflows`, `repo`, `security`, `cli`, `ci-ok`, and only `ci-ok` is required. `deploy/docker/gpu-on-demand.Dockerfile` builds a non-root, distroless, multi-arch image. Tag `v0.1.0` exists so `scripts/version.sh` has a base to bump from. `internal/clock` gives every later timer a fake; `cmd/fake-worker` gives spec 080's kind job a GPU-less double of llama-server. `cmd/vk-llm` also builds as a second, independently published binary — a usage-only skeleton here; CORE-150 fills in its subcommands.

## Scope

- Go module `github.com/savak1990/vk-llm-on-demand`, directive `go 1.26.0` (copied from `vk-ahorro/go.mod` at implementation time; the `golang:1.27` builder it compiles with is research §6).
- Layout: `cmd/gpu-on-demand/main.go`; `cmd/vk-llm/main.go` (second production binary, a usage-only CLI skeleton — CORE-150 adds the subcommands); `internal/{gateway,controller,provider,store,tunnel,worker,config,obs,mgmt,auth,budget}` as `doc.go`-only packages (one sentence, no exported symbol); `internal/clock` and `internal/clock/fake` with real code (the only interface this spec defines); `cmd/fake-worker` with real code (test-only, see Design and contracts).
- `main.go`: two `net/http` servers, signal handling, graceful shutdown, `/healthz` and `/readyz`, and the extension point later specs hang readiness checks on, including the `/api/v1/gpu` mount point CORE-055 fills.
- `.golangci.yml` v2 (copied from `vk-ahorro/.golangci.yml`).
- `deploy/docker/gpu-on-demand.Dockerfile` and `deploy/docker/fake-worker.Dockerfile`, both per ADR 0008 §3's shape.
- `.github/workflows/ci.yml` (jobs `changes, go, image, helm, workflows, repo, security, cli, ci-ok`), `.github/dependabot.yml`.
- `scripts/version.sh` and `scripts/repo-settings.sh`, both copied unchanged in behaviour from vk-ahorro (`REPO=savak1990/vk-llm-on-demand` for the latter); `scripts/specs-check.sh` already exists, unchanged.
- Makefile targets: `help go-build go-test go-lint specs-check helm-lint helm-template helm-package helm-push image-build image-push version release buildx-init keygen repo-settings require-svc`, each with this repo's existing inline `## ` doc comment. This spec is the one place these targets are defined; spec 120 uses them, it does not redefine them.
- Seed tag `v0.1.0`.
- The `VKLLM_INSTALLATION_ID` and `VKLLM_LIFECYCLE_*` config contract (names, types, rules — see Design and contracts; no code reads any of them yet).
- `.gitignore` additions for `vk-llm`'s local config and secrets (`*.local.yaml`, `.env`, `vk-llm.yaml`); if this repository grows an `examples/` directory, it holds placeholders only (ADR 0013 §1).

Excludes: provider, store, tunnel, controller, gateway or worker logic (specs 020–070); the management API, auth classifier and budget ledger implementations behind `internal/mgmt`, `internal/auth`, `internal/budget` (specs 055, 100); `vk-llm`'s subcommand logic and management-API client (spec 150); Helm chart content (spec 080 — the `helm` CI job and `make helm-*` render nothing yet); the `fake-worker` image *build step in CI*, which lives in spec 080's kind-cluster job, not in this spec's `ci.yml`; the `gitleaks`/`govulncheck`/`trivy` scanning steps and `SECURITY.md` (spec 170 — this spec only reserves the `security` job name in `ci-ok`'s `needs`); the real GoReleaser release build and CLI binary matrix published on tag (spec 120); `publish.yml`, `deploy.yml`, `release.yml`, `gitops/` (spec 120); secrets chart values and `ExternalSecret` rendering (ADR 0009, spec 130); `domain-check` / AWS SSM — this gateway templates no domain (ADR 0008 §2).

## Design and contracts

### Package layout

| Path | Contents in this spec | First spec to add code |
|---|---|---|
| `cmd/gpu-on-demand/main.go` | real: servers, signal handling, shutdown, health | — |
| `cmd/fake-worker/main.go` | real: SSE stub server (test-only) | — (consumer: spec 080) |
| `cmd/vk-llm/main.go` | real: prints usage and exits 0 (CLI skeleton only) | 150 |
| `internal/clock/clock.go` | real: wraps `time.Now/After/NewTicker` | — |
| `internal/clock/fake/fake.go` | real: manually advanced clock | — |
| `internal/gateway/doc.go` | package comment only | 060 |
| `internal/controller/doc.go` | package comment only | 050 |
| `internal/provider/doc.go` | package comment only | 020 |
| `internal/store/doc.go` | package comment only | 040 |
| `internal/tunnel/doc.go` | package comment only | 050 |
| `internal/worker/doc.go` | package comment only | 070 |
| `internal/config/doc.go` | package comment only | 040 |
| `internal/obs/doc.go` | package comment only | 090 |
| `internal/mgmt/doc.go` | package comment only | 055 |
| `internal/auth/doc.go` | package comment only | 055 |
| `internal/budget/doc.go` | package comment only | 100 |

One Deployment-image `cmd/` entry point, matching ADR 0003 §1 (one binary, one Deployment). `fake-worker` and `vk-llm` are additional `cmd/` entry points: `fake-worker` never ships as a release artifact; `vk-llm` ships as a standalone GitHub Release binary (ADR 0012 §4), never as a container image or a chart Deployment. Neither violates ADR 0003 §1.

### go.mod

```
module github.com/savak1990/vk-llm-on-demand

go 1.26.0
```

No `require` lines yet: nothing imports beyond the standard library and `internal/clock`. `go.sum` is generated by `go mod tidy` and stays empty until the first real dependency lands.

| Module | Status | First user |
|---|---|---|
| `golang.org/x/crypto` (package `ssh`) | pre-approved, not yet imported | `internal/tunnel` (ADR 0006, spec 050) |
| `github.com/prometheus/client_golang` | pre-approved, not yet imported | `internal/obs` (spec 090) |
| `github.com/prometheus/common` (package `expfmt`) | pre-approved, not yet imported | `internal/worker` (spec 070) |
| `gopkg.in/yaml.v3` | pre-approved, not yet imported; reason: CLI config file | `cmd/vk-llm` (ADR 0012 §4, spec 150) |
| `k8s.io/client-go` | forbidden | never (ADR 0004 §3) |
| any Postgres driver (e.g. `pgx`) | forbidden | never (ADR 0004 superseded Postgres) |
| any migration tool (e.g. `goose`) | forbidden | never (ADR 0004 §5) |
| any HTTP router/framework (`chi`, `gin`, `echo`, …) | forbidden | never — `net/http` + `ServeMux` only |
| anything else | forbidden without an ADR | C-25 |

Indirect requirements of the four allowed modules (e.g. `golang.org/x/sys` under `x/crypto`) are allowed as `// indirect` lines; they are not a second module to approve.

### `main.go`: what is real now vs. wired later

Only two servers and the shutdown path are real in this spec. The `config → obs → store → budget → auth → provider → tunnel → controller → gateway → mgmt` order is the eventual call sequence; today's packages are empty, so `main.go` carries a short marker comment and each later spec inserts its constructor call at that line instead of restructuring `main()`. The package table above, not the in-code comment, is the record of which spec fills which line.

```go
func main() {
    ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
    defer stop()

    // Construction order; later packages slot in here.
    // cfg := config.Load(); o := obs.New(cfg); st := store.New(cfg); bg := budget.New(cfg, st)
    // au := auth.New(cfg); pv := provider.New(cfg); tn := tunnel.New(cfg); ctl := controller.New(pv, st, tn, bg)
    // gw := gateway.New(ctl, st, au); mg := mgmt.New(ctl, bg, au)

    apiMux := http.NewServeMux()
    // apiMux.Handle("/v1/", gw); apiMux.Handle("/api/v1/gpu/", mg)
    api := &http.Server{Addr: ":8080", Handler: apiMux}
    internalMux := http.NewServeMux()
    internalMux.HandleFunc("/healthz", healthz)
    internalMux.HandleFunc("/readyz", readyz)
    health := &http.Server{Addr: ":9090", Handler: internalMux}

    run(ctx, api, health) // starts both, blocks, shuts both down on ctx.Done, 30s cap
}
```

Each server runs in its own goroutine with its own `context.WithTimeout` (C-27). `run` calls `Shutdown(ctx)` on both servers concurrently when `ctx` is cancelled, waits up to 30 s total; `main` exits 0 if both drained in time. No instance is destroyed on this path (ADR 0003 §8).

### Health semantics

| Endpoint | Port | 200 when | Never depends on |
|---|---|---|---|
| `/healthz` | 9090 | the process accepted the connection (liveness only) | config, store, provider, GPU |
| `/readyz` | 9090 | every registered readiness check passes | a GPU existing or being Ready |

`readyz` holds a package-level `[]func() error` of checks, empty in this spec, so it returns 200 unconditionally today. CORE-040 gives both `internal/store` and `internal/config` their first real code; a readiness check for either goes into this slice when that happens, without restructuring `readyz` — no interface is defined for a single caller (C-26).

### Clock

The only interface this spec defines, because it already has two implementations (C-26):

```go
type Ticker struct {
    C    <-chan time.Time
    stop func()
}
func (t Ticker) Stop() { t.stop() }

type Clock interface {
    Now() time.Time
    After(d time.Duration) <-chan time.Time
    NewTicker(d time.Duration) Ticker
}
```

`internal/clock` wraps the three `time` functions directly. `internal/clock/fake` holds a mutex-guarded time value and channel-backed tickers that fire only when a test calls `Advance(d time.Duration)`; it never sleeps. `BlockUntil(n int)` blocks until `n` timers/tickers are pending, so a test can wait for a goroutine to register its wait before calling `Advance` without a race. Every later spec's reconcile loop, idle timer and backoff timer takes a `Clock` instead of calling `time` directly (C-29).

### `cmd/fake-worker` (test-only)

A second `cmd/` entry point exists for one reason: spec 080's kind-cluster CI job needs something that answers the worker contract without a GPU.

| Endpoint | Behaviour |
|---|---|
| `GET /health` | `200` once `READY_AFTER` (env, default `0s`) has elapsed since start, else `503` |
| `GET /v1/models` | lists the alias from env `MODEL_ALIAS` (default `qwen3.6-35b-a3b`) |
| `POST /v1/chat/completions` | SSE stream of a few fixed chunks; honours `stream: true/false` |
| `GET /metrics` | a fixed Prometheus-text snippet using llama-server's metric names |

Built from `deploy/docker/fake-worker.Dockerfile` (same shape as `gpu-on-demand.Dockerfile`). It is published to no registry: spec 080's kind job builds it with `--load` only, and this spec's own `ci.yml` never builds it.

### Config contract

`internal/config` stays `doc.go`-only (Package layout), but this spec fixes the first environment variable name and rule every later field follows:

| Env var | Type | Rule | Default |
|---|---|---|---|
| `VKLLM_INSTALLATION_ID` | string | required, MUST be non-empty | chart value `installationId`, default `sha256("<namespace>/<release>")[:12]` (ADR 0004 §6) |
| `VKLLM_LIFECYCLE_PROVISIONING_MODE` | string | required; MUST equal `explicit`; any other value is a startup error (ADR 0010 §1 — `on-demand` is future, not compiled into MVP paths) | `explicit` |
| `VKLLM_LIFECYCLE_IDLE_SHUTDOWN_ENABLED` | bool | if `false`, `VKLLM_LIFECYCLE_SAFETY_MAX_LIFETIME` MUST be set to a value > 0, else startup error (ADR 0011 §5; FR-3.12) | `true` |
| `VKLLM_LIFECYCLE_IDLE_SHUTDOWN_TIMEOUT` | duration | rolling, from readiness and last inference end (ADR 0011 §2) | `30m` |
| `VKLLM_LIFECYCLE_SAFETY_MAX_LIFETIME` | duration | MUST be > 0 whenever idle shutdown is disabled | `12h` |

No code reads any of these yet; CORE-040 writes `internal/config`'s first real `Load()`, validating `VKLLM_INSTALLATION_ID` alongside `VKLLM_STATE_CONFIGMAP`; CORE-050 adds the `lifecycle.*` struct and its two startup checks (provisioning mode, idle/lifetime pairing); 060 adds its own fields. The id is chart-set, not controller-generated, so it survives a store loss (ADR 0004 §6; ADR 0003 §3). The four `VKLLM_LIFECYCLE_*` rows fix the naming pattern every other `lifecycle.*` key follows — `lifecycle.<a>.<b>` → `VKLLM_LIFECYCLE_<A>_<B>` (architecture §9) — the same dotted-path-to-SCREAMING_SNAKE_CASE rule `VKLLM_INSTALLATION_ID` set. CORE-080's `values.schema.json` enforces the same two validation rules at the chart level (its R27), so an install that bypasses the CLI and sets Helm values directly is still refused.

### `.golangci.yml`

Copied from `vk-ahorro/.golangci.yml` at implementation time:

```yaml
version: "2"
run:
  timeout: 5m
linters:
  default: standard
  enable:
    - bodyclose
    - errorlint
    - gosec
    - misspell
    - revive
    - unconvert
  exclusions:
    rules:
      - path: _test\.go$
        linters:
          - gosec
formatters:
  enable:
    - gofmt
```

### Dockerfiles (ADR 0008 §3)

`deploy/docker/gpu-on-demand.Dockerfile`, shape copied from
`vk-ahorro/deploy/docker/ahorro-api.Dockerfile`:

```dockerfile
FROM --platform=$BUILDPLATFORM golang:1.27@sha256:… AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /gpu-on-demand ./cmd/gpu-on-demand

FROM gcr.io/distroless/static-debian12:nonroot@sha256:…
LABEL org.opencontainers.image.source="https://github.com/savak1990/vk-llm-on-demand"
COPY --from=build /gpu-on-demand /gpu-on-demand
USER 65532:65532
EXPOSE 8080 9090
ENTRYPOINT ["/gpu-on-demand"]
```

`deploy/docker/fake-worker.Dockerfile` is the same two-stage shape, binary `/fake-worker` from `./cmd/fake-worker`, `EXPOSE 8080` only (no `:9090`). Both digests are pinned, cross-built for `linux/amd64,linux/arm64` where published, `latest` never pushed (C-21). The digest values are not invented here (research §6): copy them from vk-ahorro's current Dockerfile at implementation time.

### CI (ADR 0008 §2)

`.github/workflows/ci.yml`, trigger `pull_request`, top-level `permissions: {}`, every `actions/*` step pinned to a full commit SHA with the version in a trailing comment (C-22). Copy the SHAs from vk-ahorro's current `ci.yml` at implementation time (research §6); the versions below are what to pin:

| Job | Gated by `changes` on | Steps |
|---|---|---|
| `changes` | — | `dorny/paths-filter` v4.0.3 → outputs `go`, `helm`, `workflows`, `cli` (`cmd/vk-llm/**`, `.goreleaser.yaml`) |
| `go` | `cmd/**`, `internal/**`, `go.mod`, `go.sum`, `.golangci.yml`, `deploy/docker/gpu-on-demand.Dockerfile`, `Makefile` | checkout (`persist-credentials: false`), `actions/setup-go` v7.0.0 (`go-version-file: go.mod`), `make go-build`, `make go-test`, `golangci-lint-action` v9.3.0 (`version: v2.13.2`) |
| `image` | same filter output as `go` (matches vk-ahorro's `image-api`) | checkout, `docker/setup-buildx-action` v4.4.1, `docker/build-push-action` v7.4.0, `push: false`, `platforms: linux/amd64,linux/arm64`, `gpu-on-demand.Dockerfile` only |
| `helm` | `deploy/helm/**` | `azure/setup-helm` v5.0.1 (helm v4.3.0), kubeconform 0.8.0 (pinned sha256), `make helm-lint`, render + `kubeconform -strict`, `make helm-package` at the PR version. No-op until spec 080. |
| `workflows` | `.github/**` | actionlint 1.7.12 (pinned sha256), `actionlint -color` |
| `repo` | always (`needs: changes` only) | checkout `fetch-depth: 0`, `make specs-check`. No AWS, no SSM, no `domain-check` (ADR 0008 §2). |
| `security` | always (`needs: changes` only) | reserved by this spec, `permissions: contents: read`; no step runs until CORE-170 adds `gitleaks`, `govulncheck` and `trivy` (ADR 0013 §1) |
| `cli` | `changes.cli` | checkout, `actions/setup-go` v7.0.0, `go build ./cmd/vk-llm`, `go test ./cmd/vk-llm/... -count=1`; a GoReleaser snapshot step guarded by `if: hashFiles('.goreleaser.yaml') != ''`, a no-op until CORE-120 adds that file |
| `ci-ok` | `if: always()`, needs every job above | fails if any needed job's result is `failure` or `cancelled`; the one required check |

`.github/dependabot.yml`, copied from vk-ahorro: `github-actions` (root, weekly), `docker` (`/deploy/docker`, weekly), `gomod` (root, weekly).

### Makefile targets

Bodies copied from `vk-ahorro/Makefile`. Two images now exist (`gpu-on-demand`, `fake-worker`), so `SVC=` and a `require-svc` guard are kept exactly as vk-ahorro has them, not trimmed away; `CHART=` stays, for spec 080:

| Target | Does |
|---|---|
| `help`, `specs-check` | already in this repo's Makefile; unchanged |
| `go-build` | `go build -o bin/ ./cmd/...` |
| `go-test` | `go test ./... -count=1` |
| `go-lint` | `golangci-lint run ./...` |
| `require-svc` | fails unless `SVC` names an existing `deploy/docker/$(SVC).Dockerfile` |
| `helm-lint` / `helm-template` | `helm lint` / `helm template deploy/helm/$(CHART)` |
| `helm-package` / `helm-push` | package at `$(VERSION)` to `dist/`; push the chart OCI artifact |
| `image-build` | `require-svc`; `docker buildx build --load -f deploy/docker/$(SVC).Dockerfile -t $(IMAGE) .` |
| `image-push` | `require-svc buildx-init`; same, multi-arch, `--push` (CI never pushes `fake-worker`) |
| `version` | `./scripts/version.sh base` |
| `release` | `gh workflow run release.yml --ref main -f bump=$(BUMP)` (spec 120's workflow; this spec reserves the target name) |
| `buildx-init` | creates the `vk` buildx builder if missing |
| `keygen` | `openssl rand -base64 32 \| tr '+/' '-_' \| tr -d '='`, prefixed `sk-vkllm-` (ADR 0009 §2) |
| `repo-settings` | runs `scripts/repo-settings.sh`: required check `ci-ok`, `enforce_admins` on, linear history, no force-push, no deletion |

ADR 0008 §6's copied-target list names neither `keygen` nor `repo-settings`; they come from ADR 0009 §2 and ADR 0008's own Consequences, not a conflict with §6.

### Seed tag and branch protection

`v0.1.0` is created once by hand (`git tag v0.1.0 && git push origin v0.1.0`), not derived from `scripts/version.sh`, because the script needs a prior tag to bump from (ADR 0008 §1). `scripts/repo-settings.sh` is copied into this repository by this spec (it does not exist yet); `make repo-settings` runs it against the real repository once.

### Repository hygiene (ADR 0013 §1)

This spec's `.gitignore` gains three entries for `vk-llm`'s local config and secrets: `*.local.yaml`, `.env`, `vk-llm.yaml`. CORE-170 adds the broader secret-scanning entries and the CI grep for `sk-vkllm-`, `vast_` and private-key headers; this spec only reserves the CLI-local patterns. If this repository grows an `examples/` directory, every value in it MUST be a placeholder (`sk-vkllm-REPLACE_ME`, `https://llm.example.com`, `<fqdn>`), never a real key, token or domain — the same rule the common authoring brief applies to specs.

## Requirements

- R1. `go.mod` MUST declare `module github.com/savak1990/vk-llm-on-demand` and `go 1.26.0`. (source: copied from vk-ahorro's `go.mod`; builder image research §6)
- R2. Direct `require` lines in `go.mod` MUST be limited to `golang.org/x/crypto`, `github.com/prometheus/client_golang`, `github.com/prometheus/common` and `gopkg.in/yaml.v3`; their indirect requirements MAY appear as `// indirect`; no other third-party module MUST appear without a new ADR. (source: C-25; ADR 0012 §4)
- R3. `go.mod` MUST NOT list `k8s.io/client-go`, any Postgres driver, any migration tool, or any HTTP router/framework. (source: ADR 0004 §3; C-25)
- R4. Every `internal/` package other than `clock` MUST compile and export nothing until the spec that owns it adds real code. (source: C-26)
- R5. `cmd/` MUST contain exactly one Deployment-image entry point, `gpu-on-demand`; `fake-worker` MUST NOT be published as a release artifact; `vk-llm` MUST build as its own binary and MUST NOT be included in `deploy/docker/gpu-on-demand.Dockerfile` or any chart Deployment. (source: ADR 0003 §1; ADR 0012 §4; ADR 0013 §3)
- R6. `/healthz` on `:9090` MUST return 200 whenever the process is accepting connections and MUST NOT depend on config, store or provider state. (source: ADR 0008 §4)
- R7. `/readyz` on `:9090` MUST return 200 only when every registered readiness check passes, and MUST NOT require a GPU instance to exist. (source: ADR 0008 §4)
- R8. On `SIGTERM` the process MUST stop accepting new connections, drain in-flight work on both servers for up to 30 s total, and exit without calling any provider destroy path. (source: FR-4.1)
- R9. Each server-owning goroutine MUST run with its own context and timeout. (source: C-27)
- R10. Every image published from this repository MUST be tagged with the full commit SHA and the release version, and `latest` MUST NOT be built or pushed. (source: ADR 0008 §3; C-21)
- R11. Every `actions/*` step in `.github/workflows/ci.yml` MUST pin a full commit SHA with the version in a trailing comment, and the workflow MUST start with `permissions: {}`. (source: ADR 0008 §2; C-22)
- R12. `ci-ok` MUST be the only required status check and MUST fail when any job it depends on reports `failure` or `cancelled`. (source: ADR 0008 §2)
- R13. Pinned action commit SHAs and base-image digests MUST be copied from vk-ahorro's current files at implementation time, never invented or copied from this spec or from research.md. (source: research §6)
- R14. Package-level unit tests added in this spec MUST use no real sleep, no real network, and no real clock. (source: C-29)
- R15. Code comments in this spec's files MUST be at most three lines and MUST NOT reference a spec or ADR by number. (source: C-30)
- R16. The repository MUST carry one seed tag, `v0.1.0`, before any CI workflow that calls `scripts/version.sh` runs. (source: ADR 0008 §1)
- R17. `Clock` MUST be the only interface this spec defines (`Ticker` is a struct, mirroring `*time.Ticker`); the fake MUST advance time only on `Advance`, never via a real sleep, and MUST expose `BlockUntil(n int)` so a caller can wait for `n` pending timers before advancing. (source: C-26; C-29)
- R18. `VKLLM_INSTALLATION_ID` MUST be a required, non-empty environment variable; its value MUST come from the chart's `installationId` value, never generated or randomized by the application. (source: ADR 0004 §6; ADR 0003 §3)
- R19. `cmd/fake-worker` MUST serve `/health`, `/v1/models`, `/v1/chat/completions` and `/metrics` per the table above, closely enough for spec 080's kind job to use it as a worker double; its `/v1/models` alias MUST come from `MODEL_ALIAS`, matching the chart's configurable `model.alias`. (source: this spec's Design and contracts — `cmd/fake-worker`)
- R20. `VKLLM_LIFECYCLE_PROVISIONING_MODE` MUST be a required environment variable restricted to `explicit`; any other value MUST be a startup error. (source: ADR 0010 §1)
- R21. `VKLLM_LIFECYCLE_IDLE_SHUTDOWN_ENABLED=false` MUST require `VKLLM_LIFECYCLE_SAFETY_MAX_LIFETIME` to be set to a value greater than 0, else startup error. (source: ADR 0011 §5; FR-3.12)
- R22. Every `VKLLM_LIFECYCLE_*` environment variable name MUST map from its `lifecycle.*` config key by the same dotted-path-to-SCREAMING_SNAKE_CASE rule as `VKLLM_INSTALLATION_ID`. (source: architecture.md §9)
- R23. `.github/workflows/ci.yml` MUST include a `security` job, content owned by CORE-170, and a `cli` job running `go build ./cmd/vk-llm` and `go test ./cmd/vk-llm/...`; both MUST be in `ci-ok`'s `needs`. (source: ADR 0013 §1, §3)
- R24. `.gitignore` MUST exclude `*.local.yaml`, `.env` and `vk-llm.yaml`; any `examples/` directory in this repository MUST hold placeholders only. (source: ADR 0013 §1; NFR-8.6)

## Implementation steps

1. `go mod init github.com/savak1990/vk-llm-on-demand`; set `go 1.26.0`.
   Test: `go build ./...` exits 0 on an empty module.
2. Create `cmd/gpu-on-demand/main.go`, `cmd/vk-llm/main.go` (prints usage and exits 0; no subcommands yet), the eleven `internal/*/doc.go` files, and `internal/clock` + `internal/clock/fake`. Test: `go vet ./...` exits 0; `go build ./cmd/vk-llm` exits 0; `go test ./internal/clock/... -race -timeout 1s` exits 0, including an `Advance` test.
3. Implement the two servers, signal handling, `/healthz`, `/readyz`, and 30 s shutdown in `main.go`. Test: `go run ./cmd/gpu-on-demand &`; curl both endpoints return `200` within 1 s; `kill -TERM $pid` exits 0 in under 30 s.
4. Create `cmd/fake-worker/main.go`. Test: `go run ./cmd/fake-worker &`; `curl localhost:8080/v1/models` lists the alias; a streamed `/v1/chat/completions` call ends with `data: [DONE]`.
5. Add `.golangci.yml`. Test: `golangci-lint run ./...` exits 0.
6. Extend this repository's `Makefile` with the targets table above. Test: `make help` lists every target; `make go-build && make go-test` exit 0.
7. Add both Dockerfiles with digests copied from vk-ahorro's current Dockerfile. Test: `make image-build SVC=gpu-on-demand` and `SVC=fake-worker` each produce a loadable image; the first runs as uid `65532`.
8. Copy `scripts/version.sh` unchanged. Test: after step 10's tag exists, `make version` prints `0.1.1`.
9. Add `.github/workflows/ci.yml` and `.github/dependabot.yml` with SHAs copied from vk-ahorro's current workflow files; add the `changes` filter output `cli`, the reserved `security` job, and the `cli` job. Test: `actionlint -color .github/workflows/ci.yml` exits 0; a pull request shows the nine jobs and only `ci-ok` is required (after step 10).
10. `git tag v0.1.0 && git push origin v0.1.0`. Test: `git describe --tags` on `main` prints `v0.1.0`.
11. Copy `scripts/repo-settings.sh` from vk-ahorro (`REPO=savak1990/vk-llm-on-demand`, Flutter Environments step dropped) and add the `repo-settings` Makefile target. Test: `bash -n scripts/repo-settings.sh` exits 0.
12. Run `make repo-settings` against the real repository. Test: `gh api repos/savak1990/vk-llm-on-demand/branches/main/protection` shows `required_status_checks.checks` containing exactly `{"context": "ci-ok"}`.
13. Add the `.gitignore` entries `*.local.yaml`, `.env`, `vk-llm.yaml`. Test: `git check-ignore vk-llm.yaml .env foo.local.yaml` exits 0 for each.

## Tests / acceptance criteria

Only AC6 traces to a functional requirement (FR-4.1); this spec builds scaffolding, so every other AC maps to a constitution rule via its R-number.

- AC1. `go build ./...` and `go test ./... -count=1` both exit 0 on a clean checkout, including `cmd/vk-llm`. (R1, R4, R5)
- AC2. `golangci-lint run ./...` exits 0. (R15)
- AC3. `grep -c 'client-go\|pgx\|goose' go.mod` returns `0`. (R3)
- AC4. Starting the binary and curling `localhost:9090/healthz` within 1 s of process start returns HTTP `200`. (R6)
- AC5. Curling `localhost:9090/readyz` before any readiness check is registered returns HTTP `200` with no store, no provider and no GPU running. (R7)
- AC6. Sending `SIGTERM` to the running process returns exit code `0` and the wall-clock time from signal to exit is `< 30` s. (R8, FR-4.1)
- AC7. `docker buildx build --load -f deploy/docker/gpu-on-demand.Dockerfile .` exits 0; `docker inspect` on the result shows `User: "65532:65532"` and no shell binary present (distroless). (R10)
- AC8. `actionlint -color .github/workflows/ci.yml` exits 0 and every `uses:` line matches `@[0-9a-f]{40}( # v[0-9].*)?`. (R11, R13)
- AC9. A test pull request against `main` produces at least the status checks `changes`, `go`, `image`, `helm`, `workflows`, `repo`, `security`, `cli`, `ci-ok` (later specs add more under `ci-ok`'s `needs`); branch protection shows only `ci-ok` as required. (R12, R23)
- AC10. `git describe --tags --abbrev=0` on `main` prints `v0.1.0`; `make version` prints `0.1.1`. (R16)
- AC11. `make specs-check` exits 0 and prints `SPECS-CHECK: specs/ layout is valid.`
- AC12. `go list -m -f '{{if not .Indirect}}{{.Path}}{{end}}' all` prints only this module plus modules from the R2 allow-list, as each lands. (R2)
- AC13. A test calling `fakeClock.Advance(20*time.Minute)` makes a `Ticker` fire with zero wall-clock delay; `BlockUntil(1)` returns once one `NewTicker`/`After` call is pending (`go test -race -timeout 1s` passes). (R17)
- AC14. `go run ./cmd/fake-worker` answers `/health` with `503` before `READY_AFTER` elapses and `200` after. (R19)
- AC15. `git check-ignore vk-llm.yaml .env foo.local.yaml` exits 0 for each path. (R24)

## Definition of done

- [x] Tests listed above pass: CI covers AC1, AC2, AC11; AC8 was run by hand this session (`actionlint`, rc 0) since the `workflows` job has not fired yet (no `.github/**` diff in either CI run to date) — the pinned-SHA format it checks is also confirmed by grep. AC3–AC7, AC10, AC12, AC14, AC15 were run by hand this session (go.mod grep, pinned-SHA grep, `git describe`/`make version`, `go list -m`, a live `go run` + curl + `SIGTERM`, a local `docker buildx build --load` + `docker inspect`/`docker run`, `git check-ignore`) and passed. AC9 is CI-run evidence above. AC13 is covered by `internal/clock`'s existing test suite.
- [x] `make specs-check` passes.
- [x] Status flipped to DONE in the same pull request, folder renamed
  `010-D-repo-skeleton-and-ci`.
- [x] Evidence section filled with the actual pinned SHAs/digests used.
- [x] `make repo-settings` has been run once against the real repository
  (Task 11); resulting protection state read back via `gh api` in Evidence,
  in place of raw command output.

## Open questions

| Question | Why it matters | Settled by | Default |
|---|---|---|---|
| Exact action commit SHAs and base-image digests to pin | Must be current at implementation time, not at spec-writing time (research §6) | implementation steps 7 and 9 | copy from vk-ahorro's files at PR time |
| Whether `internal/config` gets real code in this spec or stays `doc.go`-only | A second caller would otherwise force a mid-flight refactor of whichever spec adds the first readiness check | CORE-040 | stays `doc.go`-only here; CORE-040 writes `Load()` with `VKLLM_INSTALLATION_ID` and `VKLLM_STATE_CONFIGMAP`; 050/060 add their own fields |
| Whether the `helm` CI job should exist before spec 080 has a chart to lint | An always-skipped job is harmless but adds a row to every PR's checks list | spec 080 | keep it now; `changes` gates it to a no-op |
| Whether the `security` job and the `cli` job's GoReleaser step should exist before specs 170 and 120 fill them | Same always-skipped-job tradeoff as `helm` above, plus the `cli` job needs a real `.goreleaser.yaml` to do anything | specs 170, 120 | keep both now; `security` runs no step, `cli`'s GoReleaser step is guarded by `hashFiles('.goreleaser.yaml') != ''` |

## Evidence

### Pinned action SHAs (`.github/workflows/ci.yml`)

| Action | SHA | Version |
|---|---|---|
| `dorny/paths-filter` | `ceb8a2b8f2d89434be7ff52d3de7ec3738c5cc9d` | v4.0.3 |
| `actions/checkout` | `3d3c42e5aac5ba805825da76410c181273ba90b1` | v7.0.1 |
| `actions/setup-go` | `b7ad1dad31e06c5925ef5d2fc7ad053ef454303e` | v7.0.0 |
| `golangci/golangci-lint-action` | `ba0d7d2ec06a0ea1cb5fa41b2e4a3ab91d21278a` | v9.3.0 (lint `v2.13.2`) |
| `docker/setup-buildx-action` | `f87e5991a6d7451dcb8d9637bfbc97413f497069` | v4.4.1 |
| `docker/build-push-action` | `c3c9e263c25d99ce0380d002d59b67737d91b0dc` | v7.4.0 |
| `azure/setup-helm` | `9bc31f4ebc9c6b171d7bfbaa5d006ae7abdb4310` | v5.0.1 (helm v4.3.0) |

Pinned by sha256, not an action: kubeconform `0.8.0` (`9bc2bffbf71f261128533edaf912153948b7ff238f9a531ae6d34466ec287883`), actionlint `1.7.12` (`8aca8db96f1b94770f1b0d72b6dddcb1ebb8123cb3712530b08cc387b349a3d8`).

### Pinned base-image digests (`deploy/docker/*.Dockerfile`)

| Image | Digest |
|---|---|
| `golang:1.27` (build stage, both Dockerfiles) | `sha256:2920bb124f1a2e12a1d39f0cab756ae0068441ee1da7655dec7f5e374944095b` |
| `gcr.io/distroless/static-debian12:nonroot` (runtime stage, both Dockerfiles) | `sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab` |

Dependabot keeps these current; this table reflects the digest as of its first bump (PR #1), not a value to hand-maintain against future bumps.

### Branch protection state after `make repo-settings` (read back via `gh api repos/savak1990/vk-llm-on-demand/branches/main/protection`)

Required status check: `ci-ok` only, `strict: false`. `enforce_admins: true`. `required_linear_history: true`. `allow_force_pushes: false`. `allow_deletions: false`. Matches `scripts/repo-settings.sh`'s intent and AC9/R12. (This is the protection state as read today, not a fresh re-run of `make repo-settings` in this session — that mutates the real repository.)

### First green `ci-ok` run

Run `41344822194` (`gh run view 41344822194 --json jobs`) shows all nine job names present on the PR: `changes`, `security`, `repo`, `go`, `image` success; `cli`, `workflows`, `helm` skipped (their `changes` filters didn't match that PR's diff); `ci-ok` success, since `ci-ok` only fails on `failure`/`cancelled`, not `skipped`. Satisfies AC9 ("at least the status checks ... present", "only `ci-ok` as required").
