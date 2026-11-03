#!/bin/sh
# Usage: sync.sh <repo> <owner/repo> <base>
# Keeps a shallow clone in $UPKEEP_HOME/cache/<repo> at the tip of the base branch. The planner reads it. The worktree of a run is separate.
set -eu
. "$(dirname "$0")/lib.sh"
repo=$1; slug=$2; base=$3
valid "$repo" || exit 2
dir="$UPKEEP_HOME/cache/$repo"
if [ -d "$dir/.git" ]; then
    git -C "$dir" fetch --quiet --depth 1 origin "$base"
    git -C "$dir" reset --quiet --hard FETCH_HEAD
else
    mkdir -p "$UPKEEP_HOME/cache"
    git clone --quiet --depth 1 --branch "$base" "$GIT_BASE/$slug" "$dir"
fi
printf '%s\n' "$dir"
