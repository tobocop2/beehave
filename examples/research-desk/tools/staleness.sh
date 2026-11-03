#!/bin/sh
# The staleness check before the publish. Usage: staleness.sh <run>. Run it as a program gate.
# It reads every cited source again and tests that each cited quote is still in the page. A quote that left its page prints a STALE line and exits 1.
set -eu
# A gate reports the standard output, so the errors go there too.
exec 2>&1
# A gate call has no env argument, so the store folder is the first argument.
[ $# -eq 2 ] || { echo "usage: staleness.sh <store> <run>" >&2; exit 1; }
RESEARCH_HOME=$1
export RESEARCH_HOME
shift
. "$(dirname "$0")/lib.sh"
run=$1
dir=$(run_dir "$run")
tab=$(printf '\t')
store=$(dirname "$0")/store.sh
fresh=$dir/fresh
mkdir -p "$fresh"
# Each step writes to a file, so a failure of get-plan, of gen or of jq stops the script (set -e), where a pipe would hide it.
sh "$store" get-plan "$run" > "$fresh/plan" || { echo "FAIL: the run has no plan, so there is no quote to test"; exit 1; }
: > "$fresh/rows"
while IFS="$tab" read -r n _ _; do
  gen=$(sh "$store" gen "$run" "$n")
  jq -r 'select(.quote_ok and (.canary | not) and .verdict == "supported") | "\(.source)\t\(.id)\t\(.quote)"' "$(section_dir "$run" "$n" "$gen")/claims.jsonl" >> "$fresh/rows"
done < "$fresh/plan"
# A gate that tests nothing must not pass. A run that reaches this check has at least one supported claim.
if [ ! -s "$fresh/rows" ]; then
  echo "FAIL: no quote to test, so the check proves nothing"
  exit 1
fi
while IFS="$tab" read -r source id quote; do
  [ -n "$id" ] || continue
  url=$(awk -F "$tab" -v s="$source" '$1 == s { print $2 }' "$dir/sources.tsv")
  # One fetch for each cited quote, into the same scratch file, so no reading of an earlier gate stands in for this one.
  curl -sS -L --fail-with-body --max-filesize 2000000 -o "$fresh/page.raw" "$url" || { echo "STALE: $url did not answer"; continue; }
  sed -e 's/<[^>]*>//g' "$fresh/page.raw" > "$fresh/page.txt"
  grep -F -q -- "$quote" "$fresh/page.txt" || echo "STALE: the quote of $id left $url: $quote"
done < "$fresh/rows" > "$fresh/report"
cat "$fresh/report"
[ ! -s "$fresh/report" ] || exit 1
