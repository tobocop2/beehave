#!/bin/sh
# Usage: scout.sh <dir>
# Prints one line for each dependency that has a newer version in reach: ecosystem, name, current, target (tab separated, sorted).
# "In reach" means what the update command can install: the same major version for Go, the range in package.json for npm.
# Exit 0 with no output means nothing is outdated. Exit 2 means the check itself failed.
# To use Renovate or another tool as the scout, replace this file with a call to the tool that prints the same four columns.
set -u
dir=${1:?usage: scout.sh <dir>}
cd "$dir" || exit 2
tmp=$(mktemp -d "${TMPDIR:-/tmp}/scout.XXXXXX") || exit 2
: > "$tmp/all"

if [ -f go.mod ]; then
    go list -u -m -json all > "$tmp/go.json" 2> "$tmp/go.err" || { echo "scout: go list failed: $(head -n 3 "$tmp/go.err")" >&2; exit 2; }
    jq -r 'select(.Update != null and (.Main | not) and (.Indirect | not)) | ["go", .Path, .Version, .Update.Version] | @tsv' "$tmp/go.json" >> "$tmp/all" || exit 2
fi

if [ -f package.json ]; then
    npm outdated --json > "$tmp/npm.json" 2> "$tmp/npm.err"
    rc=$?
    # npm outdated exits 1 when something is outdated. Any other code is a failure.
    [ "$rc" -le 1 ] || { echo "scout: npm outdated failed: $(head -n 3 "$tmp/npm.err")" >&2; exit 2; }
    if [ -s "$tmp/npm.json" ]; then
        jq -r 'to_entries[] | select(.value.current != null and .value.wanted != null and .value.current != .value.wanted) | ["npm", .key, .value.current, .value.wanted] | @tsv' "$tmp/npm.json" >> "$tmp/all" || exit 2
    fi
fi

LC_ALL=C sort "$tmp/all"
