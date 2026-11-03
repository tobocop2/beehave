#!/bin/sh
# $1: the day. Reserves the agent cost of the day in one SQL statement, once for each day. Exit 0 reserved, exit 3 the budget cannot cover the day.
set -eu
. "$(dirname "$0")/lib.sh"
check_day "$1"
day=$1
check_int PIPE_BUDGET_CENTS "${PIPE_BUDGET_CENTS:-x}"
printf '%s' "${PIPE_BACKFILL:-}" | grep -Eq '^[A-Za-z0-9._-]+$' || die "PIPE_BACKFILL is not set to an item id"
init_wh
n=$(count_lines "$STAGE/$day/records.jsonl")
est=$(((n * COST_CENTS_PER_100 + 99) / 100))
sql "INSERT OR IGNORE INTO spend(backfill, day, est_cents)
     SELECT '$PIPE_BACKFILL', '$day', $est
     WHERE COALESCE((SELECT SUM(est_cents) FROM spend WHERE backfill = '$PIPE_BACKFILL'), 0) + $est <= $PIPE_BUDGET_CENTS;"
held=$(sql "SELECT COUNT(*) FROM spend WHERE backfill = '$PIPE_BACKFILL' AND day = '$day';")
if [ "$held" = "1" ]; then
    echo "reserved $est cents for $n tickets"
    exit 0
fi
total=$(sql "SELECT COALESCE(SUM(est_cents), 0) FROM spend WHERE backfill = '$PIPE_BACKFILL';")
echo "budget spent: $total of $PIPE_BUDGET_CENTS cents reserved, and $day needs $est"
exit 3
