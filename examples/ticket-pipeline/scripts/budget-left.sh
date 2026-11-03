#!/bin/sh
# Takes no argument. Exit 0 while the backfill has budget left, exit 3 when the reservations reach the budget.
set -eu
. "$(dirname "$0")/lib.sh"
check_int PIPE_BUDGET_CENTS "${PIPE_BUDGET_CENTS:-x}"
printf '%s' "${PIPE_BACKFILL:-}" | grep -Eq '^[A-Za-z0-9._-]+$' || die "PIPE_BACKFILL is not set to an item id"
init_wh
total=$(sql "SELECT COALESCE(SUM(est_cents), 0) FROM spend WHERE backfill = '$PIPE_BACKFILL';")
if [ "$total" -ge "$PIPE_BUDGET_CENTS" ]; then
    echo "budget spent: $total of $PIPE_BUDGET_CENTS cents"
    exit 3
fi
echo "budget left: $((PIPE_BUDGET_CENTS - total)) of $PIPE_BUDGET_CENTS cents"
