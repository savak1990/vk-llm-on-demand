---
id: "CORE-170"
status: "DRAFT"
updated: "2026-11-01"
depends_on: ["CORE-010", "CORE-120"]
adrs: ["0008", "0009", "0013"]
---

# 170 — Security CI gate and release checklist

**Status note:** DRAFT, written 2026-11-01 before any code exists.

**Complexity:** M — three scanners wired as pinned, sha256-verified
binaries (not actions), a grep guard, a weekly full-history schedule; no
novel design, the checksummed-binary pattern is copied from `vk-ahorro`'s
existing `kubeconform`/`actionlint` steps.
**Risk:** low-medium — a scanner false positive blocks every PR until the
allowlist is fixed; mitigated by a committed `.gitleaks.toml` reviewed in
the same pull request that adds it.
**Estimated effort:** one session (3–5 h)
**Recommended model:** standard — mechanical adaptation of an existing
pattern to three new tools, no algorithmic design.
**Depends on:** [CORE-010](../010-P-repo-skeleton-and-ci/spec.md)
(`ci.yml` skeleton, `Makefile`, the `go`/`image` jobs this spec's
`security` job sits beside), [CORE-120](../120-P-release-pipeline/spec.md)
(`release.yml`, gated by convention, not automation, on this spec's
checklist)

## Outcome

A pull request that introduces a committed secret, a reachable Go
vulnerability, or a HIGH/CRITICAL image vulnerability with a known fix
fails CI before merge, through one `security` job beside `go`, `image`,
`helm`, `workflows` and `repo` under `ci-ok`. A weekly scheduled workflow
re-scans the entire git history for secrets committed before scanning
existed. `SECURITY.md`'s release checklist (already committed) is the
manual gate a maintainer confirms before running `release.yml`; CI never
parses checkbox state out of a PR description. `make security` runs the
same three scanners locally with the same exit codes.

## Scope

- A `security` job in `.github/workflows/ci.yml`: `gitleaks` (pinned
  binary, sha256-verified) over the PR's commit range; a CI grep guard for
  leaked-secret and real-domain patterns; `govulncheck` on `./...`;
  `trivy` (pinned binary, sha256-verified) scanning the image this job
  builds locally and, once it exists, the chart via `trivy config`.
- `.github/workflows/security-schedule.yml`: weekly `gitleaks` over the
  full history, no commit-range restriction.
- `.gitleaks.toml`, a committed allowlist for this repository's documented
  placeholders (`sk-vkllm-REPLACE_ME`, `llm.example.com`, `<fqdn>`).
- `.gitignore` entries for local secret/config files.
- `examples/` holds placeholders only, enforced by the same grep guard
  (no separate script).
- `make security`: the same three scanners, same arguments as `ci.yml`.
- Review of `.github/dependabot.yml` (exists, CORE-010) and the
  action-pinning policy (C-22) against this job's one new action type
  (`docker/setup-buildx-action`, already pinned elsewhere; no new action
  is introduced).
- `SECURITY.md` maintenance (the file already exists, written against
  this spec and ADR 0013); only the ACs that verify it still matches.
- `CODEOWNERS` is not needed: one maintainer, no second reviewer for a
  required-review rule to select.

Excludes: an automated gate that parses checklist checkbox state out of a
PR/release description — rejected by the brief; the checklist stays a
documented manual step before dispatching `release.yml` (CORE-120 owns
that workflow, gains no new automation here). `golangci-lint`/`gosec`,
`actionlint`, `helm lint`/`kubeconform` (exist, CORE-010/080, unchanged).
Image/chart digest/tag discipline (ADR 0008 §3, CORE-120 — this spec scans
what they build, not how it's tagged). Cosign signing and SBOM
publication — `SECURITY.md` already lists both as planned, not MVP (ADR
0013 Alternatives). `scripts/check-rbac.sh` and CORE-110's RBAC/redaction/
rotation content — a sibling spec (CORE-110 Excludes already points
`gitleaks` ownership here).

## Design and contracts

### `security` job placement in `ci.yml`

Added beside the jobs CORE-010 already defines (`changes`, `go`, `image`,
`helm`, `workflows`, `repo`), listed in `ci-ok`'s `needs`:

| Job | Gated by `changes` on | Why |
|---|---|---|
| `security` | nothing — `needs: changes` only | a secret/real domain can land in any file, not only `cmd/**`/`internal/**`; gated like `repo` (CORE-010 §CI) so it never silently skips on a docs-only PR |

`permissions: contents: read` only; no step uploads an artifact, so no
finding is ever written where a second person could read it from the
Actions UI (ADR 0013 §1, "never upload findings as artifacts").

### Steps, in order

1. **Checkout**, `fetch-depth: 0` (`gitleaks` needs the commit range to
   diff, same reason `repo`'s `specs-check` step needs full history),
   `persist-credentials: false`.
2. **Install `gitleaks`**: the same checksummed-binary shape
   `vk-ahorro/.github/workflows/ci.yml` uses for `kubeconform` (lines
   277–287) and `actionlint` (lines 368–380) — pin `GITLEAKS_VERSION` and
   `GITLEAKS_SHA256` for the `linux_x64` asset, `curl -sSLo`, `sha256sum
   -c -`, extract, `sudo mv` to `/usr/local/bin`. Both values are filled
   in at implementation time (`docs/research.md` names no gitleaks
   release — see Open questions).
3. **Run `gitleaks`** on the PR's commits only: `gitleaks detect --redact
   --no-banner --config .gitleaks.toml --log-opts="${{
   github.event.pull_request.base.sha }}..${{
   github.event.pull_request.head.sha }}"`; non-zero exit fails the job.
4. **CI grep guard** (`git grep` calls, no script): fails if any tracked
   file matches `sk-vkllm-(?!REPLACE_ME)[A-Za-z0-9_-]+`, a PEM
   private-key header (`-----BEGIN (OPENSSH|RSA|EC) PRIVATE KEY-----`), or
   any line in `scripts/forbidden-domains.txt` (empty today — see Open
   questions).
5. **Install Go** (`actions/setup-go`, same pinned SHA and
   `go-version-file: go.mod` the `go` job already uses).
6. **Run `govulncheck`**: `go run golang.org/x/vuln/cmd/govulncheck@<pinned
   version>` on `./...`; fails on any reachable vulnerability.
7. **Install `trivy`**: same checksummed-binary shape as step 2, never
   `aquasecurity/trivy-action` (ADR 0013 §1 — the action was compromised
   in 2026, per `ci.yml`'s own header comment, copied by CORE-010).
8. **Build the image for scanning**: `docker/setup-buildx-action`
   (pinned, same version the `image` job uses), `docker buildx build
   --load --platform linux/amd64 -f deploy/docker/gpu-on-demand.Dockerfile
   -t gpu-on-demand:scan .` — a second, single-arch build independent of
   the `image` job's multi-arch `push: false` build, so no cross-job
   artifact upload/download is needed. `docker save gpu-on-demand:scan -o
   image.tar`.
9. **Scan the image**: `trivy image --input image.tar --severity
   HIGH,CRITICAL --ignore-unfixed --exit-code 1`.
10. **Scan the chart** (once one exists): `test -d deploy/helm/gpu-on-demand
    && trivy config deploy/helm/gpu-on-demand || true` — a no-op today,
    exactly like CORE-010's `helm` job ("No-op until spec 080"); CORE-080
    drops the `|| true` once the chart exists (that spec's own AC, not
    repeated here).

### `security-schedule.yml`

```yaml
on:
  schedule:
    - cron: "0 6 * * 1"   # weekly, Monday 06:00 UTC
  workflow_dispatch: {}
permissions: {}
jobs:
  gitleaks-full-history:
    runs-on: ubuntu-latest
    permissions: { contents: read }
    steps:
      # checkout fetch-depth: 0, install gitleaks (same binary/sha256
      # pins as steps 1–2 above), then, with no --log-opts so the
      # default scans every commit reachable from HEAD:
      - run: gitleaks detect --redact --no-banner --config .gitleaks.toml
```

`workflow_dispatch` lets a maintainer re-run on demand after editing
`.gitleaks.toml`. No `pull_request` trigger, so it cannot be a required
check; it exists to catch a secret committed before `gitleaks` existed in
`ci.yml`, which the PR-range scan (step 3) never sees (ADR 0013 §1).

### `.gitleaks.toml`

```toml
[extend]
useDefault = true

[allowlist]
  description = "This repository's documented placeholders"
  regexes = [
    '''sk-vkllm-REPLACE_ME''',
    '''llm\.example\.com''',
  ]
```

`useDefault = true` keeps gitleaks' built-in rule set (AWS keys, private
key headers, high-entropy strings) and adds an allowlist for the two
placeholder forms `SECURITY.md`/C-14 already name. `<fqdn>` needs no
entry: it has no high-entropy string and matches no built-in rule.
Reviewed in the same PR as any future addition (ADR 0013 Risks).

### `.gitignore` additions

```
.env
.env.*
*.pem
*.key
kubeconfig*
values.local.yaml
```

Covers local-only files a developer might create while testing against a
real cluster or Vast account; none is ever a template input or a fixture
this repository ships, so ignoring them is simpler than teaching the grep
guard about them.

### `examples/` rule, Dependabot, `CODEOWNERS` (review, no new mechanism)

`examples/` is a tracked directory like any other: the grep guard (step
4) and `gitleaks` (step 3) already cover it; a dedicated `examples`-only
check would duplicate them for no new coverage. `.github/dependabot.yml`
(CORE-010) tracks `github-actions`/`docker`/`gomod` weekly; none of
`gitleaks`/`govulncheck`/`trivy` is any of those — each is a version
string in a workflow `env:` block, so all three are bumped by hand, the
same accepted limitation `kubeconform`/`actionlint` already have in
`vk-ahorro`. This job introduces no GitHub Action beyond
`actions/setup-go` and `docker/setup-buildx-action`, both already pinned
elsewhere (C-22 needs no new SHA). `CODEOWNERS` is not added: with one
maintainer there is no second reviewer for a required-review rule to
select; a one-file addition if a second maintainer ever joins.

### Release checklist (restated placement, no new automation)

`SECURITY.md`'s "Release checklist" (already committed) is read by a
maintainer before dispatching `release.yml` (CORE-120); nothing parses
its checkbox state, per the brief's explicit instruction against that.
The automated share of the checklist is exactly the three scanners this
spec adds plus the controls CORE-010/CORE-120 already enforce (`ci-ok`
green, SHA-pinned actions, digest-pinned images, no `latest`) — every
checklist line is either an existing CI job or a once-per-release human
step, no third category.

## Requirements

- R1. `gitleaks` MUST run as a pinned-version, sha256-verified binary
  downloaded over HTTPS, never as a GitHub Action. (source: ADR 0013 §1)
- R2. On every PR, `gitleaks` MUST scan at minimum the base-to-head commit
  range; a separate scheduled workflow MUST scan the full history at
  least weekly with no commit-range restriction. (source: ADR 0013 §1;
  task-170)
- R3. A committed `.gitleaks.toml` MUST be the only mechanism that
  suppresses a finding; no inline comment or CI-only flag may do so.
  (source: ADR 0013 Risks)
- R4. `govulncheck` MUST run via `go run
  golang.org/x/vuln/cmd/govulncheck@<pinned version>` on `./...` on every
  pull request and MUST fail the job on any reachable vulnerability.
  (source: ADR 0013 §1; task-170)
- R5. `trivy` MUST run as a pinned-version, sha256-verified binary, never
  as `aquasecurity/trivy-action`. (source: ADR 0013 §1, Alternatives)
- R6. `trivy image` MUST scan the image built from
  `deploy/docker/gpu-on-demand.Dockerfile` on every PR with `--severity
  HIGH,CRITICAL --ignore-unfixed --exit-code 1`. (source: task-170)
- R7. `trivy config` MUST scan `deploy/helm/gpu-on-demand` once that
  directory exists; until then the step MUST be a documented no-op, never
  a hard failure. (source: task-170; CORE-010 §CI precedent for `helm`)
- R8. Every job this spec adds MUST declare `permissions: contents: read`
  and MUST NOT upload any scanner output as a workflow artifact. (source:
  ADR 0013 §1)
- R9. No tracked file MUST contain a string matching
  `sk-vkllm-(?!REPLACE_ME)[A-Za-z0-9_-]+` or a PEM private-key header; the
  `security` job's grep guard MUST check this on every pull request in
  addition to `gitleaks`. (source: C-14, NFR-8.6)
- R10. `.gitignore` MUST exclude `.env*`, `*.pem`, `*.key`, `kubeconfig*`
  and `values.local.yaml` from every commit. (source: C-14; SECURITY.md
  "What never enters Git")
- R11. `make security` MUST run `gitleaks`, `govulncheck` and `trivy` with
  the same arguments and the same exit-code semantics `ci.yml`'s
  `security` job uses. (source: task-170)
- R12. No workflow file this spec adds MUST introduce a GitHub Action not
  already pinned elsewhere in `.github/workflows/ci.yml`, and every
  `uses:` step MUST be pinned to a full commit SHA with a version
  comment. (source: C-22)
- R13. `SECURITY.md`'s "Controls in CI" table and "Release checklist"
  section MUST name every scanner this spec adds and MUST NOT describe a
  control this spec does not implement. (source: ADR 0013 §2; task-170)
- R14. `release.yml` MUST NOT gain a step that parses checklist checkbox
  state out of any PR or issue body. (source: task-170, explicit
  instruction)
- R15. `security-schedule.yml` MUST NOT be a required status check (it
  has no `pull_request` trigger to attach one to). (source: ADR 0013 §1;
  design)

## Implementation steps

1. Add `.gitleaks.toml` and `scripts/forbidden-domains.txt` (empty, one
   comment line). Test: `gitleaks detect --config .gitleaks.toml --no-git
   --source .` exits 0 locally against a scratch commit containing
   `sk-vkllm-REPLACE_ME`, and exits 1 against the same commit with a real-
   shaped `sk-vkllm-` string.
2. Add the `security` job to `.github/workflows/ci.yml` (Design and
   contracts steps 1–10), pinning `gitleaks`/`trivy` versions and sha256
   sums at this step, not before. Test: `actionlint
   .github/workflows/ci.yml` exits 0.
3. Add `.github/workflows/security-schedule.yml`. Test: `actionlint`
   exits 0; the file has no `pull_request:` trigger.
4. Add the `.gitignore` entries. Test: `git check-ignore -v .env
   kubeconfig.yaml values.local.yaml` each print a matching rule.
5. Add `make security`, calling the three scanners with step 2's flags.
   Test: `make security` exits 0 on a clean checkout.
6. Add `security` to `ci-ok`'s `needs:` (CORE-010's job). Test: a test PR
   shows `security` among its checks; `ci-ok` fails when a scratch commit
   adds a real-shaped `sk-vkllm-` key, naming `security` as the reason.
7. Review `SECURITY.md` against R13; edit only on drift (expected: none,
   the file already matches). Test: manual read-through, recorded in
   Evidence.

## Tests / acceptance criteria

- AC1. A scratch branch adds a real-shaped `sk-vkllm-` string: `gitleaks`
  and the grep guard both fail; `ci-ok` is `failure`. (R1, R2, R9)
- AC2. Same branch with the string replaced by `sk-vkllm-REPLACE_ME`:
  both steps pass. (R3)
- AC3. On `main` with no code beyond an empty `go.mod`, `govulncheck`
  exits 0. (R4)
- AC4. `trivy image --input image.tar ...` against the built image on
  `main` exits 0 (no known HIGH/CRITICAL fixable vulnerability at
  spec-writing time; re-verified at implementation time per research.md's
  six-month rule). (R6)
- AC5. Before CORE-080, the chart-scan step exits 0 with a log line
  noting the missing directory; after CORE-080, it invokes `trivy
  config`. (R7)
- AC6. `actionlint` is clean on both workflow files this spec adds. (R12)
- AC7. `security-schedule.yml` shows `permissions: {}` at top level and
  `contents: read` on its one job. (R8)
- AC8. `git check-ignore -v .env *.pem kubeconfig.yaml values.local.yaml`
  each resolve to a rule from this spec's `.gitignore` addition. (R10)
- AC9. `make security` locally produces the same pass/fail result as the
  `security` job's three scanner steps on the same commit. (R11)
- AC10. Documented procedure (not a committed fixture, per task-170):
  throwaway branch, fake key pattern, push, observe `security` fail,
  delete branch. Recorded once in Evidence, not re-run per spec change.
  (R1, R9)
- AC11. `security-schedule.yml` has no `pull_request:` key under `on:`.
  (R15)
- AC12. `SECURITY.md`'s "Controls in CI" table lists exactly the jobs
  under `ci-ok` once this spec lands, no extra row. (R13)
- AC13. `git grep -n "checklist" .github/workflows/release.yml` finds no
  checkbox-parsing (`- \[[ x]\]`) against a GitHub context object. (R14)

## Definition of done

- [ ] AC1–AC13 pass; `make specs-check` passes.
- [ ] `.gitleaks.toml` reviewed with the user before merge (ADR 0013 Risks).
- [ ] AC10's fixture-branch procedure run once; branch/run link recorded
  in Evidence, branch deleted.
- [ ] Status flipped to DONE in the same PR, folder renamed
  `170-D-security-ci-and-release`.
- [ ] Evidence filled with the actual pinned versions/sha256 sums used.

## Open questions

| Question | Why it matters | Settled by | Default |
|---|---|---|---|
| Exact `gitleaks`/`trivy` release versions and sha256 sums | `docs/research.md` names neither tool; R1/R5 need a real pin | Step 2, read from each project's GitHub Releases page at implementation time | No default — required manual lookup |
| Pinned `govulncheck` module version | `golang.org/x/vuln` is not in research.md's module table | Step 2 | Latest tagged release at implementation time |
| Any real hostname to seed `scripts/forbidden-domains.txt`? | research.md records no real `fqdn`, only the platform's wildcard (research §5) | Whoever configures a real deployment target | File stays empty until a real domain is known |
| Does GHCR's own image scanning duplicate `trivy image`? | could make the PR-time scan redundant after publish | CORE-120, once GHCR is public | No duplication assumed; PR-time scan is the only gate, it runs before publish |
| Does `trivy`'s vulnerability DB need pinning for determinism? | an un-pinned DB can fail a later PR with no code change | Step 2, against `trivy`'s documented DB behavior | Fetch latest DB each run; a DB-only failure is still a real finding |

## Evidence

None yet.
