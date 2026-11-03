#!/bin/sh
# $1: the day. Reads the labels of the agent from stdin, one JSON object on each line, and saves them to the stage. Blank lines are dropped.
set -eu
. "$(dirname "$0")/lib.sh"
check_day "$1"
dir=$STAGE/$1
[ -d "$dir" ] || die "no extract for $1"
grep -v '^[[:space:]]*$' > "$dir/labels.jsonl.tmp" || true
mv "$dir/labels.jsonl.tmp" "$dir/labels.jsonl"
echo "collected $(count_lines "$dir/labels.jsonl") labels"
