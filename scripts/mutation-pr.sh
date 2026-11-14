#!/bin/sh
# Run gremlins on the lines that a change touches, and fail if no mutant was applied.
#
# Usage: scripts/mutation-pr.sh [BASE]
#   BASE is the branch or commit to diff against. The default is origin/main.
#
# Run from the module root with the path ".". Gremlins matches the lines of the diff to mutants by file path.
# The diff has paths relative to the module root. Gremlins gives mutants paths relative to the folder that it
# was started on. The two forms match only when that folder is the module root. With a package path, every
# mutant is "skipped", nothing runs, and the exit code is 0.
#
# "Applied" means killed + lived + timed out. The run fails when applied is 0, or when a mutant lived.
set -eu

base=${1:-origin/main}
root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

# An empty diff makes gremlins treat every line as changed and mutate the whole tree. Refuse that.
if [ -z "$(git diff --merge-base "$base" --name-only -- '*.go' ':!*_test.go')" ]; then
	echo "mutation-pr: the diff against $base has no Go source change. No mutant can apply."
	exit 1
fi

out=$(mktemp)
status=0
timeout 1100 gremlins unleash --diff "$base" . | tee "$out" || status=$?
if [ "$status" -ne 0 ]; then
	echo "mutation-pr: gremlins exited with $status"
	exit "$status"
fi

count() { sed -n "s/.*$1: \([0-9][0-9]*\).*/\1/p" "$out" | head -1; }
killed=$(count Killed)
lived=$(count Lived)
timed=$(count "Timed out")
applied=$((${killed:-0} + ${lived:-0} + ${timed:-0}))
echo "mutation-pr: applied=$applied killed=${killed:-0} lived=${lived:-0} timed_out=${timed:-0}"

if [ "$applied" -eq 0 ]; then
	echo "mutation-pr: no mutant was applied. Check that the diff against $base touches covered Go code."
	exit 1
fi
if [ "${lived:-0}" -gt 0 ]; then
	echo "mutation-pr: $lived mutant(s) lived. Kill each one with a test."
	exit 1
fi
