#!/bin/sh
# Usage: report.sh <week>
# Writes $UPKEEP_HOME/reports/<week>.md from the run log and the state files, and prints it.
# The report is a plain script, not an agent: every number in it comes from a log row, so it cannot drift from the facts.
set -eu
. "$(dirname "$0")/lib.sh"
wk=${1:?usage: report.sh <week>}
valid "$wk" || exit 2
mkdir -p "$UPKEEP_HOME/reports"
out="$UPKEEP_HOME/reports/$wk.md"
tab=$(printf '\t')
t=$(mktemp "${TMPDIR:-/tmp}/report.XXXXXX")

count() { if [ -f "$RUNLOG" ]; then awk -F'\t' -v w="$wk" -v o="$1" '$1 == w && $3 == o { n++ } END { print n + 0 }' "$RUNLOG"; else echo 0; fi; }

{
    printf '# Dependency upkeep, %s\n\n' "$wk"
    printf '%s repositories listed. %s opened a pull request, %s skipped, %s failed the gate, %s had an error, %s were deferred.\n\n' \
        "$(grep -c -v -e '^#' -e '^$' "$UPKEEP_HOME/repos.tsv")" "$(count opened)" "$(count skipped)" "$(count red)" "$(count error)" "$(count deferred)"
    printf '## This week\n\n'
    missing=""
    while IFS="$tab" read -r name _slug _base _gate <&3; do
        case "$name" in "" | \#*) continue ;; esac
        row=""
        [ -f "$RUNLOG" ] && row=$(awk -F'\t' -v w="$wk" -v r="$name" '$1 == w && $2 == r { last = $3 "\t" $4 "\t" $5 } END { print last }' "$RUNLOG")
        if [ -z "$row" ]; then missing="$missing $name"; continue; fi
        outcome=$(printf '%s' "$row" | cut -f1); pr=$(printf '%s' "$row" | cut -f2); detail=$(printf '%s' "$row" | cut -f3)
        printf -- '- %s: %s%s%s\n' "$name" "$outcome" "${pr:+, $pr}" "${detail:+ ($detail)}"
    done 3< "$UPKEEP_HOME/repos.tsv"
    printf '\n'
    if [ -n "$missing" ]; then printf '## No record this week\n\nThe tick wrote no row for:%s. The tick did not reach them. Check the scheduler.\n\n' "$missing"; fi

    printf '## Waiting for review\n\n'
    waiting=0
    for f in "$STATE"/*.env; do
        [ -f "$f" ] || continue
        name=$(basename "$f" .env)
        pr=$(state_get "$name" last_pr)
        [ -n "$pr" ] || continue
        st=$(gh pr view "$pr" --json state --jq .state 2> /dev/null || echo UNKNOWN)
        if [ "$st" = OPEN ]; then printf -- '- %s: %s, opened in %s\n' "$name" "$pr" "$(state_get "$name" last_week)"; waiting=$((waiting + 1)); fi
    done
    [ "$waiting" -gt 0 ] || printf 'None.\n'
} > "$t"
mv "$t" "$out"
cat "$out"
