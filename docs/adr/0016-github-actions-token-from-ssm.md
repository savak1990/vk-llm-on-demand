# ADR 0016: The GitHub Actions management token is read from AWS SSM via OIDC

## Status

Accepted (2026-11-21). Adds to [ADR 0012](0012-management-api-cli-and-github-actions.md) §5;
ADR 0012's Status carries a pointer here. Amended 2026-11-22: the parameter
is `management_tokens`, not `api_token` (see [Amendment](#amendment-2026-11-22)).

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
one SSM `SecureString` parameter — already used by `vk-ahorro`'s CI jobs
via `aws-actions/configure-aws-credentials`. There is no reason for
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
   `aws-actions/configure-aws-credentials` echoes `role-to-assume` in the
   step's log header, so the AWS account ID embedded in the ARN appears in
   this public repository's run logs; accepted, because an account ID is
   not a credential and the same ARN pattern is already public in
   `vk-ahorro`'s run logs. Storing the ARN as a GitHub secret instead was
   rejected — that would put a secret back into GitHub.
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

The `api_token` value this parameter holds must also be one line of the
deployed `management-tokens` list; rotating it means re-encrypting the SSM
value and updating `management-tokens` in the same change, otherwise
`gpu-control` gets 401 on its next run. The owner has chosen one shared
token for now, so the earlier property that the GitHub Actions token is a
line distinct from the CLI's (spec 110 R19) no longer holds for this
deployment — both read the same line.

## Amendment (2026-11-22)

vk-lab-platform #112 gave each key list its own SSM parameter:
`/<project>/persistent/llm/api_token` now holds the inference client keys,
and `/<project>/persistent/llm/management_tokens` (from
`secrets/llm-on-demand-management-tokens.enc`) holds the management tokens.
The decision stands; only the parameter changes:

- `gpu-control.yml` reads `/vk-hetzner-lab/persistent/llm/management_tokens`,
  and `vk-llm-ci-role` may read and decrypt only that parameter
  (vk-lab-platform #114). This replaces `api_token` in Decision 1 and in the
  rotation paragraph above: rotate by re-encrypting
  `llm-on-demand-management-tokens.enc` and re-applying.
- The previous paragraph's rule, that `api_token` must also be a line of
  `management-tokens`, no longer applies: the workflow reads the management
  list itself.
- `gpu-control.yml` refuses a value with more than one line. A second line
  would not be masked and would be read as a new variable in the step's env
  file. So the CLI and GitHub Actions share one management token; separate
  tokens would need a dedicated parameter, not a line position.
