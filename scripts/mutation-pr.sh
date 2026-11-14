#!/bin/sh
# Run gremlins on the lines that a change touches, and fail if no mutant was applied.
#
# Usage: scripts/mutation-pr.sh BASE
#   BASE is the branch or commit that the pull request targets, for example origin/integration/r1.
#   There is no default, because the base differs from one pull request to the next.
#
# Run from the module root with the path ".". Gremlins matches the lines of the diff to mutants by file path.
# The diff has paths relative to the module root. Gremlins gives mutants paths relative to the folder that it
# was started on. The two forms match only when that folder is the module root. With a package path, every
# mutant is "skipped", nothing runs, and the exit code is 0.
#
# "Applied" means killed + lived + timed out, counted from the JSON report of gremlins.
# The run fails when gremlins exits with a non-zero status, when applied is 0, or when a mutant lived.
# A diff with no Go source change prints "not applicable" and exits 0.
set -eu

if [ $# -ne 1 ] || [ -z "$1" ]; then
	echo "usage: scripts/mutation-pr.sh BASE   (BASE is the branch the pull request targets, for example origin/integration/r1)" >&2
	exit 2
fi
base=$1

command -v gremlins >/dev/null 2>&1 || { echo "mutation-pr: gremlins is not on the path"; exit 1; }
command -v jq >/dev/null 2>&1 || { echo "mutation-pr: jq is not on the path"; exit 1; }
git rev-parse --verify --quiet "$base^{commit}" >/dev/null || { echo "mutation-pr: $base is not a commit or branch"; exit 1; }

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

# An empty diff makes gremlins treat every line as changed and mutate the whole tree. Stop before that.
if [ -z "$(git diff --merge-base "$base" --name-only -- '*.go' ':!*_test.go')" ]; then
	echo "mutation-pr: not applicable: no Go source change against $base"
	exit 0
fi

report=$(mktemp "${TMPDIR:-/tmp}/mutation-pr.XXXXXX")
trap 'rm -f "$report"' EXIT INT TERM

status=0
gremlins unleash --diff "$base" --output "$report" . || status=$?
if [ "$status" -ne 0 ]; then
	echo "mutation-pr: gremlins exited with status $status"
	exit "$status"
fi

[ -s "$report" ] || { echo "mutation-pr: gremlins wrote no report"; exit 1; }
counts=$(jq -r '[.files[]?.mutations[]?.status] | [(map(select(. == "KILLED")) | length), (map(select(. == "LIVED")) | length), (map(select(. == "TIMED OUT")) | length)] | @tsv' "$report") || { echo "mutation-pr: the gremlins report is not readable"; exit 1; }
killed=$(echo "$counts" | cut -f1)
lived=$(echo "$counts" | cut -f2)
timed=$(echo "$counts" | cut -f3)
applied=$((killed + lived + timed))
echo "mutation-pr: applied=$applied killed=$killed lived=$lived timed_out=$timed"

if [ "$applied" -eq 0 ]; then
	echo "mutation-pr: no mutant was applied. Check that the diff against $base touches covered Go code."
	exit 1
fi
if [ "$lived" -gt 0 ]; then
	echo "mutation-pr: $lived mutant(s) lived. Kill each one with a test."
	exit 1
fi
