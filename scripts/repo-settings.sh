#!/usr/bin/env bash
# Applies the repository settings this project depends on (branch protection,
# merge strategy, squash-only enforcement) so they are reproducible instead of
# clicked in the web UI. Idempotent; run it as often as you like.
set -euo pipefail

REPO="${REPO:-savak1990/vk-llm-on-demand}"
BRANCH="${BRANCH:-main}"

command -v gh >/dev/null || { echo "REPO-SETTINGS: gh is not installed." >&2; exit 1; }

echo "REPO-SETTINGS: applying to $REPO"

# New repos have no protection, so the sub-resource PATCH endpoints below
# 404. Create the full object once; later runs PATCH parts idempotently.
if ! gh api "repos/$REPO/branches/$BRANCH/protection" >/dev/null 2>&1; then
  echo "REPO-SETTINGS: creating initial branch protection"
  cat <<'PROTECTION' |
{
  "required_status_checks": {
    "strict": false,
    "checks": [{"context": "ci-ok"}]
  },
  "enforce_admins": true,
  "required_linear_history": true,
  "allow_force_pushes": false,
  "allow_deletions": false,
  "required_pull_request_reviews": null,
  "restrictions": null
}
PROTECTION
    gh api -X PUT "repos/$REPO/branches/$BRANCH/protection" --input - --silent
  echo "REPO-SETTINGS: initial protection created"
fi

# PATCH this sub-resource: PUT here 404s (reads like a missing branch), and PUT
# on the parent .../protection replaces the whole object, dropping enforce_admins.
# strict=false: one developer gains nothing from forcing branches up to date.
echo '{"strict":false,"checks":[{"context":"ci-ok"}]}' |
  gh api -X PATCH "repos/$REPO/branches/$BRANCH/protection/required_status_checks" \
    --input - --silent
echo "REPO-SETTINGS: required status check is ci-ok"

gh api -X POST "repos/$REPO/branches/$BRANCH/protection/enforce_admins" --silent
echo "REPO-SETTINGS: enforce_admins on"

# Linear history, no force push and no deletion have no sub-resource of their
# own, so they are read back rather than written. Failing loudly beats a PUT
# that would clear what it does not mention.
protection="$(gh api "repos/$REPO/branches/$BRANCH/protection")"
for field in required_linear_history:true allow_force_pushes:false allow_deletions:false; do
  key="${field%%:*}"
  want="${field##*:}"
  got="$(printf '%s' "$protection" | jq -r ".${key}.enabled")"
  if [ "$got" != "$want" ]; then
    echo "REPO-SETTINGS: $key is $got, expected $want - fix it in the branch protection UI." >&2
    exit 1
  fi
done
echo "REPO-SETTINGS: linear history on, force push and deletion off"

# Squash is the only merge method and the branch goes on merge, so the history
# stays linear without anyone remembering to make it so.
gh api -X PATCH "repos/$REPO" \
  -F allow_squash_merge=true \
  -F allow_merge_commit=false \
  -F allow_rebase_merge=false \
  -F delete_branch_on_merge=true \
  --silent
echo "REPO-SETTINGS: squash-only, branch deleted on merge"

echo "REPO-SETTINGS: done."
