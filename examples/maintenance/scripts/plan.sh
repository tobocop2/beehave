#!/bin/sh
# Usage: plan.sh <dir> <repo> <week>
# Decides whether this week needs a bump. Exit 0: go. The first output line is fingerprint=<hash>, then the table of updates.
# Exit 10: skip. The only output line is the reason. Exit 2: the check failed.
# A skip costs no worktree, no agent and no run. The state of the last bump is in $STATE, outside every worktree.
set -u
. "$(dirname "$0")/lib.sh"
dir=${1:?usage: plan.sh <dir> <repo> <week>}; repo=${2:?}; week=${3:?}
valid "$repo" && valid "$week" || { echo "plan: bad repository or week" >&2; exit 2; }

if logged "$week" "$repo" opened red; then echo "skip: already ran in $week"; exit 10; fi

table=$(sh "$(dirname "$0")/scout.sh" "$dir") || exit 2
[ -n "$table" ] || { echo "skip: nothing is outdated"; exit 10; }

fp=$(printf '%s\n' "$table" | sha)
if [ "$(state_get "$repo" last_fp)" = "$fp" ]; then
    echo "skip: same updates as $(state_get "$repo" last_week), no new version since"
    exit 10
fi

# One open bump pull request at a time. A person who has not reviewed last week's change does not get a second one.
open=$(cd "$dir" && gh pr list --state open --limit 200 --json number,headRefName --jq '.[] | select(.headRefName | startswith("upkeep/")) | .number' 2> /dev/null) || { echo "plan: gh pr list failed" >&2; exit 2; }
if [ -n "$open" ]; then echo "skip: pull request #$(printf '%s' "$open" | head -n 1) is still open"; exit 10; fi

printf 'fingerprint=%s\n%s\n' "$fp" "$table"
