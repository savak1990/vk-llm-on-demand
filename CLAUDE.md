# CLAUDE.md

## Code comments

Comments are allowed only in very complex parts of code, and must be at most 3 lines. Prefer a single line where a comment is necessary at all. Do not comment straightforward code.

Never reference specific documents (specs, ADRs, tickets) in code comments. Explain the WHY directly in the comment instead; document links belong in commit messages or PR descriptions, not in code.

---

## Project purpose

`vk-llm-on-demand` is a Go control plane that exposes an OpenAI-compatible API and rents a GPU instance (Vast.ai first) only while requests arrive. It is deployed onto the separate platform repository `vk-lab-platform`, but its Helm chart must install into any Kubernetes cluster.

This repository holds the control plane, the worker launch scripts, the Helm chart and the release pipeline. It MUST NOT hold platform code. Platform changes (Argo pointer Application, External Secrets policy, SSM parameters) go to `vk-lab-platform` as a pull request; spec `core/130` lists them.

The authority is `specs/core/000-P-constitution/spec.md`. Read it before non-trivial work; this file does not restate it.

---

## Decisions already made

`docs/adr/` is the source of truth. Do not reopen an Accepted ADR inside a spec or a pull request; write a new ADR that supersedes it. ADRs marked Proposed wait on the spike in `specs/core/030`.

Short version, for orientation only:

- Vast.ai Docker instances are remote workers, never cluster nodes (ADR 0001).
- `llama-server` from the official `server-cuda` image serves `Qwen3.6-35B-A3B` Q5_K_M (ADR 0002).
- One binary, one Deployment, `replicas: 1`, `strategy: Recreate`; destroy on idle, never stop (ADR 0003).
- State is one ConfigMap JSON document written with `resourceVersion` optimistic concurrency; the provider instance list is reconciled against it every minute (ADR 0004).
- Model weights download from Hugging Face on every provision, checksum verified (ADR 0005).
- The controller opens an SSH tunnel to the worker and hands the gateway a transport; llama-server never listens on a public interface (ADR 0006, Proposed).
- Cold starts are covered by holding the request up to `gateway.maxWait` with SSE heartbeats, then `503` + `Retry-After` (ADR 0007).
- The pipeline and chart conventions copy `vk-ahorro` (ADR 0008).
- Runtime secrets reach the cluster through the platform's KMS ciphertext → SSM → External Secrets path (ADR 0009, Proposed).

---

## Cost safety

The GPU is the only expensive thing. Every provider mutation is idempotent, every create is bounded by a deadline and an attempt budget, every destroy is retried until the provider confirms absence, and anything carrying our label that the state store does not know is destroyed. Never weaken one of these to make a test or a feature easier.

Defaults the user set: monthly budget $30, maximum hourly price $0.80, one instance, idle timeout 20 minutes.

---

## Nothing sensitive in Git

No secret, no root domain, and no `fqdn` value is ever committed — not in code, not in a chart value, not in a spec, a commit message, a pull request body, or a CI log. Use `<root-domain>` or `lab.example.com` where a shape must be shown. API keys, prompts and completions are never logged.

---

## Images and charts

Every image carries the full commit SHA as a tag. `latest` is never built or pushed. One version number names the whole repository and comes from a git tag; the image tag and the chart version are the same string. `Chart.yaml`'s `version:` is a placeholder overridden at package time. Images and charts live on GHCR under `ghcr.io/savak1990/vk-llm-on-demand/`.

---

## Spec-driven development workflow

For non-trivial work:

1. Read `docs/architecture.md`.
2. Read `specs/core/000-P-constitution/spec.md`.
3. Read the relevant numbered spec and the ADRs it cites.
4. Inspect the existing implementation.
5. Produce an implementation plan.
6. Implement incrementally, test first.
7. Run validation.
8. Fix failures before declaring completion.
9. Record material decisions as ADRs in `docs/adr/`.
10. Carry the spec to `DONE` in the same pull request.

A spec's folder letter must match its front-matter `status:`; changing the status renames the folder in the same commit. `make specs-check` enforces it.

Do not silently change a requirement to make implementation easier. If requirements are ambiguous, prefer the interpretation most consistent with:

1. cost safety,
2. architecture invariants,
3. security,
4. simplicity.

---

## Go rules

Standard library first: `net/http` with method patterns, `log/slog`, `context` everywhere, `errors.Is/As`. Allowed third-party modules are listed in `specs/core/010`; adding one needs an ADR. Interfaces exist where a second implementation exists or a test needs a fake (provider, store, tunnel dialer, clock). No framework.

---

## Exploration and planning tools

When exploring the codebase before planning or implementing, prefer dispatching read-only subagents in parallel over ad hoc reading. When designing an implementation approach for non-trivial work, consult the advisor tool before committing to a plan, and again before declaring the work complete.

---

## Validation

`make` targets are the only supported entry points, and every target carries a `##` doc comment that `make help` prints.

Before opening a pull request: `make specs-check`, and once code exists `make go-build go-test go-lint helm-lint helm-template`. Never claim a check passes without running it in the same session.

---

## Cost rules

Educational project on a personal account. Resource requests set cpu and memory; limits set memory only, never cpu. One replica. No HA. Nothing stays running when storage would do.
