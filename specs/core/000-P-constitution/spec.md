---
id: "CORE-000"
status: "DRAFT"
updated: "2026-11-01"
depends_on: []
adrs: ["0001", "0003", "0004", "0008", "0009", "0010", "0011", "0012", "0013"]
---

# 000 — Constitution

**Status note:** DRAFT, written 2026-11-01 before any code exists. Becomes
READY when the user approves the Phase 1–2 documents.

**Complexity:** S
**Risk:** low — it is rules, not code
**Estimated effort:** review only
**Recommended model:** standard
**Depends on:** nothing

## Outcome

Every later spec, pull request and review in this repository can be checked
against one short list of invariants. A change that violates one of them is
wrong even when its tests pass.

## Scope

The engineering constraints that hold across all specs: cost safety,
state and identity, security, delivery, code, documentation. Each rule has
an id (`C-n`) that specs cite.

Excludes: anything specific to one subsystem; those rules live in the
subsystem's spec and cite the constitution.

## Design and contracts

### Cost safety

| Id | Rule |
|---|---|
| C-1 | The GPU is the only expensive resource. Every design choice is judged first by whether it can leave a GPU running unobserved. |
| C-2 | Every provider mutation (create, destroy) is idempotent under an operation key and bounded by a timeout. A retry can never create a second instance. |
| C-3 | Every create has a deadline (`lifecycle.startupTimeout`) and an attempt budget (`lifecycle.safety.maxProvisionAttemptsPerHour`). Exhaustion means stop and alert, never try harder. |
| C-4 | Destroy is retried until the provider confirms absence. A destroy that keeps failing is an alert, not a log line. |
| C-5 | Instances are destroyed, never stopped (ADR 0003 §6). |
| C-6 | The provider's instance list is reconciled against the state store at least every 60 s; an instance carrying our label prefix and unknown to the store is destroyed (ADR 0003 §4). |
| C-7 | The defaults $30 per month, $0.80 per hour, one instance, 30 minutes rolling inactivity, 60 seconds drain, 12 hours lifetime are chart defaults and never hard-coded elsewhere. |
| C-7a | Every GPU is subject to the rolling inactivity timeout (ADR 0011); nothing in the inference or management path can disable or extend it except a finished inference request. Provisioning happens only through the management API in the initial release (ADR 0010). |
| C-8 | The worker exits on its own after `worker.deadManTimeout` without traffic (ADR 0003 §7). |
| C-9 | A failed read of provider state never triggers a destroy; only a successful observation does, twice in a row. |

### State and identity

| Id | Rule |
|---|---|
| C-10 | Durable state is the single ConfigMap document of ADR 0004, written only through `Store.Save` with the version read, never blind. |
| C-11 | Identity across the provider boundary is the label `vkllm:<installation-id>:<generation>`; the generation is the fencing token. A process acts only on generations it has read. |
| C-12 | In-flight requests and the pending queue are process memory and are allowed to be lost on restart. |
| C-13 | The store can be empty at any start. Startup converges from the provider, not the other way round. |

### Security

| Id | Rule |
|---|---|
| C-14 | The repository is public. No secret, no root domain, no `fqdn` value, no token in Git, chart values, specs, examples, fixtures, commit messages or CI logs. Placeholders are `<fqdn>`, `llm.example.com` and `sk-vkllm-REPLACE_ME`. gitleaks runs on every pull request (ADR 0013). |
| C-15 | Secrets reach the pod by reference to existing Kubernetes Secrets, mounted as files (ADR 0009 §1). |
| C-16 | Every API request is authenticated before any other work; comparison is constant-time; keys, prompts and completions are never logged or persisted. Inference keys and management tokens are separate lists; each is rejected on the other surface (ADR 0012). |
| C-17 | The inference port on the worker is never bound to a public interface (ADR 0006 §1). |
| C-18 | The controller dials only addresses the provider API returned for an instance this installation created; the gateway never dials. |
| C-19 | Request limits are on by default: body 1 MiB, `max_tokens` cap, per-key rate limit. |
| C-20 | The pod runs as non-root, read-only root filesystem, no capabilities, with the least RBAC that the state store needs. |

### Delivery

| Id | Rule |
|---|---|
| C-21 | One version per repository, from a git tag; image tag equals chart version; every image also carries the full commit SHA; `latest` is never built (ADR 0008). |
| C-22 | GitHub Actions are pinned to full commit SHAs; workflows start with `permissions: {}`. |
| C-23 | The chart installs on any Kubernetes cluster with three Secrets and no ingress controller; platform-specific objects are optional and off by default. |
| C-24 | Platform changes go to `vk-lab-platform` as pull requests listed in spec 130; this repository holds no platform code. |

### Code

| Id | Rule |
|---|---|
| C-25 | Standard library first. Third-party modules are those listed in spec 010; adding one needs an ADR. |
| C-26 | An interface exists only where a second implementation or a test fake exists: `Provider`, `Store`, `Dialer`, `Clock`. |
| C-27 | Every goroutine has an owner and a context; every external call has a timeout; bounded concurrency everywhere. |
| C-28 | Errors are wrapped with `%w` and classified at the boundary into retryable, terminal and fatal. |
| C-29 | Tests are deterministic: fake provider, fake store, fake clock; no sleeps; no network in unit tests. One acceptance spec (140) touches the real provider under a budget cap. |
| C-30 | Comments are rare, at most three lines, and never reference specs or ADRs by number. |

### Documentation

| Id | Rule |
|---|---|
| C-31 | A material decision is an ADR with Status, Context, Decision, Alternatives, Rationale, Consequences, Risks. An Accepted ADR is changed only by a superseding ADR. |
| C-32 | External facts cited in specs come from `docs/research.md` with a confidence tag; unverified facts are open questions, not assumptions in code. |
| C-33 | A spec's folder letter matches its status; the pull request that finishes a spec carries it to DONE with evidence. |
| C-34 | Metrics labels are bounded enumerations; no ids in labels. |

## Requirements

- R1. Every spec MUST cite the constitution rules it implements or is bound by. (source: C-31)
- R2. A pull request MUST NOT weaken C-1 to C-9 to make a test or a feature easier. (source: C-1)
- R3. `make specs-check` MUST pass on every pull request. (source: C-33)

## Implementation steps

1. Review this document with the user; set status READY.
2. Each later spec cites the rules it is bound by in its Requirements.

## Tests / acceptance criteria

- AC1. `make specs-check` reports `SPECS-CHECK: specs/ layout is valid.`
- AC2. Every spec `010`–`180` cites at least one `C-n` rule.

## Definition of done

- [ ] User approved.
- [ ] Status READY, folder letter unchanged (`P`).

## Open questions

None.

## Evidence

None yet.
