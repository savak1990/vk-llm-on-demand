# ADR 0015: The GitHub Actions management token lives in an Environment gated to main

## Status

Accepted (2026-11-21). Adds to [ADR 0012](0012-management-api-cli-and-github-actions.md) §5;
ADR 0012's Status carries a pointer here.

## Context

ADR 0012 §5 and spec 160 store `VK_LLM_TOKEN` as a repository secret. A
repository secret is handed to any workflow on any branch. A collaborator
with write access, or an attacker who holds a collaborator's session,
pushes a branch whose workflow reads the secret, or calls
`POST /api/v1/gpu/start` from it. Forks cannot: fork runs receive no
secrets. The attempt cap and the monthly budget bound the spend; they do
not stop it.

## Decision

1. `VK_LLM_TOKEN` is stored in the GitHub Environment `gpu-control`, not
   as a repository secret. The Environment's deployment branch policy
   allows protected branches only, which on this repository is `main`.
2. `gpu-control.yml`'s job declares `environment: gpu-control`. A run
   from any other ref receives no secret and fails at the first API call.
3. The token reaches `curl` only through a header read from stdin
   (spec 160 R5), never as a command-line argument.
4. `make repo-settings` creates or updates the Environment with that
   policy, so the gate is reproducible and not a click in the web UI.

## Consequences

A compromised collaborator account can still dispatch the workflow from
`main`; that is the exposure ADR 0012 §5 already accepts, and now the only
one. Spec 160's credentials table, `README.md` and the risk register (R13)
describe the Environment. The repository variable `VK_LLM_API_ENDPOINT`
is unchanged: it holds no secret.
