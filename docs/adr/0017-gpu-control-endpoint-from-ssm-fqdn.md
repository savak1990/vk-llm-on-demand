# ADR 0017: The gpu-control endpoint is built from the lab fqdn in SSM

## Status

Accepted (2026-11-22). Amends [ADR 0012](0012-management-api-cli-and-github-actions.md)
§5 ("endpoint from the repository variable `VK_LLM_API_ENDPOINT`"); ADR
0012's Status carries a pointer here.

## Context

`gpu-control.yml` needs the management API endpoint, `https://llm.<fqdn>`.
ADR 0012 §5 put it in a repository variable and passed it through the
job's `env:` block. Two facts rule that out:

- GitHub prints a job's `env:` block, values included, in the header of
  every step of the run log. In this public repository that would put the
  installation's hostname into a public log before `scripts/gpu-control.sh`
  can mask it, which `CLAUDE.md` and NFR-8.6 forbid. The run log of
  workflow run 44926841082 shows the block (`ENDPOINT:` printed in the
  checkout step's header).
- A GitHub secret avoids the log, but `vk-lab-platform` already removed a
  GitHub secret for the domain in favour of its SSM path (its ADR 0023),
  and a hand-copied secret is a second copy that goes stale when the lab
  is rebuilt.

`vk-lab-platform` already publishes each project's lab domain as the plain
`String` parameter `/<project>/bootstrap/route53/fqdn`, and its apps' hosts
are `<label>.<fqdn>` (this app's is `llm.<fqdn>`). `vk-ahorro`'s CI reads
its domain the same way: through its OIDC role, masked before use.

## Decision

1. `gpu-control.yml` reads `/vk-hetzner-lab/bootstrap/route53/fqdn` in the
   same step that reads the management token, registers
   `::add-mask::` for it, and exports `ENDPOINT=https://llm.<fqdn>`. The
   job's `env:` block does not contain `ENDPOINT`.
2. `vk-llm-ci-role` gets `ssm:GetParameter` on `/*/bootstrap/route53/fqdn`
   (vk-lab-platform #115). The parameter is a plain `String`: the domain is
   hygiene data, not a credential, so no `kms:Decrypt` is involved.
3. No GitHub variable or secret holds the endpoint. `VK_LLM_API_ENDPOINT`
   stays only as the `vk-llm` CLI's environment variable on a workstation.
4. The step fails closed when the parameter is missing or empty.

## Consequences

The endpoint follows the lab: rebuilding it or changing its domain needs no
GitHub change. A second environment (for example dev and prod) is a second
project, `/<project>/bootstrap/route53/fqdn` and
`/<project>/persistent/llm/management_tokens`; the IAM grants already
wildcard the project segment, so adding one means a GitHub Environment per
project and making the project name in the two SSM paths a variable of that
Environment. The `llm` host label is fixed in the workflow, matching
`gitops/values.yaml`'s `hostPrefix: llm`; changing one means changing both.
`scripts/gpu-control.sh` still masks the host it is given (spec 160 R12),
now as a second layer.
