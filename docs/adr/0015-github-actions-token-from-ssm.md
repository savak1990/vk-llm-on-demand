# ADR 0015: The GitHub Actions management token is read from AWS SSM via OIDC

## Status

Accepted (2026-11-21). Adds to [ADR 0012](0012-management-api-cli-and-github-actions.md) §5;
ADR 0012's Status carries a pointer here.

## Context

ADR 0012 §5 originally stored `VK_LLM_TOKEN` as a plain GitHub repository
secret, readable by any workflow on any branch. A first revision of this
ADR moved it into the GitHub Environment `gpu-control`, gated to `main` by
that Environment's deployment branch policy — safe, but it required a
human to hand-copy the token's value into GitHub, a second place for the
same bearer credential to go stale or leak, next to the value already
managed as the source of truth in `vk-lab-platform`'s
`secrets/llm-on-demand-api-token.enc` → SSM parameter
`/vk-hetzner-lab/persistent/llm/api_token`.

`vk-lab-platform` already has an established, audited pattern for exactly
this: a per-repository IAM role, trusted via GitHub OIDC
(`AssumeRoleWithWebIdentity`), that a workflow assumes at run time to read
one SSM `SecureString` parameter — used today by six `vk-ahorro` workflow
jobs via `aws-actions/configure-aws-credentials`. There is no reason for
`vk-llm-on-demand` to invent a second mechanism.

## Decision

1. `VK_LLM_TOKEN` is never stored in GitHub — not as a repository secret,
   not as an Environment secret. `gpu-control.yml` reads it at run time
   from AWS SSM Parameter Store, `/vk-hetzner-lab/persistent/llm/api_token`
   (`SecureString`), the same parameter `AWS-037` already created.
2. `gpu-control.yml`'s job declares `environment: gpu-control` and
   `permissions: {contents: read, id-token: write}`, then assumes IAM role
   `vk-llm-ci-role` (`vk-lab-platform`) via
   `aws-actions/configure-aws-credentials`, SHA-pinned, the same action
   and pin already used by `vk-ahorro`'s CI.
3. `vk-llm-ci-role`'s OIDC trust is scoped to the `gpu-control` Environment's
   `sub` claim (`repo:savak1990/vk-llm-on-demand:environment:gpu-control`),
   not a wildcard branch — a run from any other ref or without that
   Environment cannot assume the role at all. The Environment's deployment
   branch policy (protected branches only) is what makes that claim possible
   only from `main`; it remains the identity gate, it is just no longer a
   secret store.
4. The role's ARN reaches this repository as the plain GitHub Actions
   variable `AWS_ROLE_ARN` (not a secret — a role ARN is configuration, the
   same reasoning `vk-lab-platform`'s own docs give for every other
   consuming repository), set once by that repository's `make account-up`.
5. The value read from SSM is masked (`::add-mask::`) before it is exported,
   the same protection a GitHub secret gets automatically.

## Consequences

Rotating the GitHub Actions copy of the token needs no GitHub-side step at
all: re-encrypt `secrets/llm-on-demand-api-token.enc` in `vk-lab-platform`
and re-apply (`make persistent-up`); the next `gpu-control` run reads the
new value. A compromised collaborator account can still dispatch the
workflow from `main` and get a valid token — the same exposure the
Environment-secret version already accepted, and now the only one. Losing
network access to AWS STS or SSM fails the workflow closed (no fallback to
a cached value), which is the intended behavior for a credential this
sensitive. `make repo-settings` (this repo) still creates the `gpu-control`
Environment and its branch policy; `vk-lab-platform`'s `make account-up`
creates `vk-llm-ci-role` and sets this repository's `vars.AWS_ROLE_ARN`.
