#!/bin/sh
# Usage: pr.sh <repo> <week> <fingerprint> <base>      (run in the worktree; the pull request body is on stdin)
# Pushes the branch and opens a draft pull request. A second call finds the first pull request and opens none.
# Then it records the fingerprint, so next week skips the same set of updates.
set -eu
. "$(dirname "$0")/lib.sh"
repo=${1:?}; week=${2:?}; fp=${3:?}; base=${4:?}
valid "$repo" && valid "$week" || { echo "pr: bad repository or week" >&2; exit 2; }
branch="upkeep/$repo-$week"
body=$(mktemp "${TMPDIR:-/tmp}/prbody.XXXXXX")
cat > "$body"
git push --quiet --force origin "HEAD:refs/heads/$branch"
url=$(gh pr list --head "$branch" --state all --json url --jq '.[0].url // empty')
if [ -z "$url" ]; then
    url=$(gh pr create --draft --head "$branch" --base "$base" --title "Bump dependencies ($week)" --body-file "$body")
fi
state_put "$repo" last_fp "$fp"
state_put "$repo" last_week "$week"
state_put "$repo" last_pr "$url"
runlog "$week" "$repo" opened "$url" "draft pull request"
printf '%s\n' "$url"
