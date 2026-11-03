#!/bin/sh
# $1: the day. Checks the labels against the extract. Exit 0 valid, exit 1 odd (a person decides), exit 2 broken.
set -eu
. "$(dirname "$0")/lib.sh"
check_day "$1"
day=$1
rec=$STAGE/$day/records.jsonl
lab=$STAGE/$day/labels.jsonl
[ -f "$rec" ] || die "no extract for $day"
n=$(count_lines "$rec")
if [ "$n" = "0" ]; then echo "no tickets"; exit 0; fi

bad() { echo "$*"; exit 2; }
[ -s "$lab" ] || bad "no labels for $n tickets"
jq -e . "$lab" > /dev/null 2>&1 || bad "the labels file is not JSON lines"
jq -r '.id' "$rec" | sort > "$STAGE/$day/ids.records"
jq -r '.id // empty' "$lab" | sort > "$STAGE/$day/ids.labels"
grep -Evq '^[A-Za-z0-9_-]+$' "$STAGE/$day/ids.records" && bad "a ticket id has a character outside A-Z a-z 0-9 _ -"
dup=$(uniq -d "$STAGE/$day/ids.labels" | head -3 | tr '\n' ' ')
[ -z "$dup" ] || bad "an id has two labels: $dup"
if ! cmp -s "$STAGE/$day/ids.records" "$STAGE/$day/ids.labels"; then
    miss=$(comm -23 "$STAGE/$day/ids.records" "$STAGE/$day/ids.labels" | wc -l | tr -d ' ')
    extra=$(comm -13 "$STAGE/$day/ids.records" "$STAGE/$day/ids.labels" | wc -l | tr -d ' ')
    bad "$miss tickets have no label and $extra labels have no ticket"
fi
other=$(jq -r '.label' "$lab" | sort -u | grep -vxFf "$SCHEMA/labels.txt" | head -3 | tr '\n' ' ' || true)
[ -z "$other" ] || bad "labels that are not allowed: $other"

unsure=$(jq -r 'select(.label == "unsure") | .id' "$lab" | wc -l | tr -d ' ')
if [ $((unsure * 100)) -gt $((n * UNSURE_MAX_PCT)) ]; then
    echo "$unsure of $n labels are unsure, and the limit is $UNSURE_MAX_PCT percent"
    exit 1
fi

# Drift: the share of each label against the pooled share of the last 7 loaded days. It needs at least 3 loaded days.
init_wh
days=$(sql "SELECT COUNT(*) FROM (SELECT day FROM manifest WHERE day < '$day' AND rows > 0 ORDER BY day DESC LIMIT 7);")
if [ "$days" -ge 3 ]; then
    sql "WITH d AS (SELECT day FROM manifest WHERE day < '$day' AND rows > 0 ORDER BY day DESC LIMIT 7)
         SELECT label, ROUND(100.0 * COUNT(*) / (SELECT COUNT(*) FROM labeled WHERE day IN d), 1)
         FROM labeled WHERE day IN d GROUP BY label;" > "$STAGE/$day/share.sql"
    tr '|' '\t' < "$STAGE/$day/share.sql" > "$STAGE/$day/share.before"
    jq -r '.label' "$lab" | sort | uniq -c | awk -v n="$n" '{ printf "%s\t%.1f\n", $2, 100 * $1 / n }' > "$STAGE/$day/share.today"
    drift=$(awk -F'\t' -v max="$DRIFT_MAX_POINTS" '
        NR == FNR { before[$1] = $2; next }
        { seen[$1] = 1; d = $2 - (($1 in before) ? before[$1] : 0); if (d < 0) d = -d; if (d > max) printf "%s moved %.1f points; ", $1, d }
        END { for (l in before) if (!(l in seen) && before[l] > max) printf "%s vanished from %.1f percent; ", l, before[l] }
    ' "$STAGE/$day/share.before" "$STAGE/$day/share.today")
    if [ -n "$drift" ]; then
        echo "the label shares drifted from the last $days loaded days: $drift"
        exit 1
    fi
fi
echo "valid: $n tickets, $unsure unsure"
