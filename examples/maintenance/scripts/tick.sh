#!/bin/sh
# Usage: tick.sh      Started once a week by the scheduler (see launchd/ and the crontab line in the README).
# For each repository: refresh the clone, decide, and run the bump workflow only when there is something to do.
# Then it files and runs the report item. One repository that fails never stops the others.
set -u
. "$(dirname "$0")/lib.sh"
H=$UPKEEP_HOME
mkdir -p "$H/tmp" "$H/reports"
# One tick at a time. A tick that is still running when the next one starts means a week of work took more than a week.
# The lock is a kernel lock on a file, so a tick that a kill or a power cut stops leaves no lock to remove. The script starts again under the lock.
# The lock stays on descriptor 9 of this shell. Each long-running command runs through child, which closes descriptor 9 first, so a command that outlives a killed tick does not keep the lock.
# Short commands (log.sh, jq, sed, head, cat) keep descriptor 9 until they end.
if [ -z "${UPKEEP_TICK_LOCKED:-}" ]; then
    UPKEEP_TICK_LOCKED=1
    export UPKEEP_TICK_LOCKED
    take_lock "$H/tick.flock" sh "$0" "$@"
fi
unset UPKEEP_TICK_LOCKED
wk=$(week)
tab=$(printf '\t')

# The list goes in on descriptor 3. beehave reads standard input, and a read from the list would swallow the next rows.
while IFS="$tab" read -r name slug base _gate <&3; do
    case "$name" in "" | \#*) continue ;; esac
    valid "$name" || { echo "tick: skip bad name $name" >&2; continue; }
    if ! child sh "$H/scripts/sync.sh" "$name" "$slug" "$base" < /dev/null > "$H/tmp/$name.dir" 2> "$H/tmp/$name.err"; then
        sh "$H/scripts/log.sh" "$wk" "$name" error "sync failed: $(head -n 1 "$H/tmp/$name.err")"
        continue
    fi
    dir=$(cat "$H/tmp/$name.dir")
    child sh "$H/scripts/plan.sh" "$dir" "$name" "$wk" < /dev/null > "$H/tmp/$name.plan"
    rc=$?
    out=$(cat "$H/tmp/$name.plan")
    case "$rc" in
        10) sh "$H/scripts/log.sh" "$wk" "$name" skipped "$out"; continue ;;
        0) ;;
        *) sh "$H/scripts/log.sh" "$wk" "$name" error "plan failed"; continue ;;
    esac
    id="bump-$name-$wk"
    # The item carries the decision: the workflow plans nothing again.
    fp=$(printf '%s\n' "$out" | sed -n '1s/^fingerprint=//p')
    table=$(printf '%s\n' "$out" | sed '1d')
    jq -n --arg repo "$name" --arg week "$wk" --arg base "$base" --arg fingerprint "$fp" --arg table "$table" \
        '{repo: $repo, week: $week, base: $base, fingerprint: $fingerprint, table: $table}' > "$H/tmp/$id.json"
    # An item left from an earlier try of this week makes item add fail. The run below continues it, so the failure is not fatal.
    child beehave item add "$name" "$id" --title "Bump dependencies in $name ($wk)" --body-file "$H/tmp/$id.json" < /dev/null > /dev/null 2>&1
    child beehave run "$id" "$name" --workflow "$H/bump.star" < /dev/null
    rc=$?
    if [ "$rc" -ne 0 ] && ! logged "$wk" "$name"; then
        # Exit 3 is the usage cap: no agent login is under its cap, and beehave refuses to start.
        if [ "$rc" -eq 3 ]; then
            sh "$H/scripts/log.sh" "$wk" "$name" deferred "no agent login under its usage cap"
        else
            sh "$H/scripts/log.sh" "$wk" "$name" error "beehave run exited $rc"
        fi
    fi
done 3< "$H/repos.tsv"

rid="report-$wk"
printf '{"week":"%s"}\n' "$wk" > "$H/tmp/$rid.json"
child beehave item add upkeep "$rid" --title "Weekly report $wk" --body-file "$H/tmp/$rid.json" < /dev/null > /dev/null 2>&1
child beehave run "$rid" upkeep --workflow "$H/report.star" < /dev/null
