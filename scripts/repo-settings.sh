#!/usr/bin/env bash
# Applies the repository settings this project depends on: branch protection,
# merge strategy, and squash-only enforcement. A setting clicked in the web UI
# is not reproducible; this is.
#
# Idempotent. Run it as often as you like.
set -euo pipefail

REPO="${REPO:-savak1990/vk-llm-on-demand}"
BRANCH="${BRANCH:-main}"

command -v gh >/dev/null || { echo "REPO-SETTINGS: gh is not installed." >&2; exit 1; }

echo "REPO-SETTINGS: applying to $REPO"

# Bootstrap: create a base protection object if none exists yet. New repos have
# no protection, so sub-resource PATCH endpoints (required_status_checks,
# enforce_admins) return 404. Create the full object once; subsequent runs use
# PATCH to update parts idempotently.
if ! gh api "repos/$REPO/branches/$BRANCH/protection" >/dev/null 2>&1; then
  echo "REPO-SETTINGS: creating initial branch protection"
  gh api -X PUT "repos/$REPO/branches/$BRANCH/protection" \
    -f required_status_checks='{"strict":false,"checks":[{"context":"ci-ok"}]}' \
    -F enforce_admins=true \
    -F required_linear_history=true \
    -F allow_force_pushes=false \
    -F allow_deletions=false \
    -F required_pull_request_reviews=null \
    -F restrictions=null \
    --silent
  echo "REPO-SETTINGS: initial protection created"
fi

# One required check, not a list of job names. A renamed job used to leave
# branch protection waiting on a context that never reported again.
#
# The sub-resource, never PUT .../protection: the parent endpoint replaces the
# whole object, so a partial body silently drops enforce_admins, linear
# history and the force-push ban.
#
# strict stays false: with one developer, forcing every branch up to date
# before merge only re-runs CI for no new information.
# PATCH, not PUT: PUT exists only on the parent .../protection and answers 404
# here, which reads like a missing branch rather than a wrong method.
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
