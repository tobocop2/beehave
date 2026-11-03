#!/bin/sh
# Usage: apply.sh <week>      (run in the worktree)
# Updates the dependencies and commits the manifest files. Exit 11 when the update changed nothing.
# The commit names its files one by one. It never stages the whole tree.
set -eu
week=${1:?usage: apply.sh <week>}
if [ -f go.mod ]; then
    go get -u ./... > /dev/null
    go mod tidy
fi
if [ -f package.json ]; then
    npm update > /dev/null
fi
files=""
for f in go.mod go.sum package.json package-lock.json; do
    # status, not diff: a lock file that the update creates for the first time is untracked, and diff does not see it.
    if [ -f "$f" ] && [ -n "$(git status --porcelain -- "$f")" ]; then files="$files $f"; fi
done
[ -n "$files" ] || { echo "apply: the update changed no manifest file"; exit 11; }
# shellcheck disable=SC2086
git add -- $files
git commit --quiet -m "Bump dependencies ($week)" -m "Updates within the version ranges that the manifests allow."
git diff --stat HEAD~1 HEAD
