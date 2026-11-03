#!/bin/sh
# Waits for two healthy reads in a row. Arguments: service, minutes. Exit 0: healthy. Exit 1: not healthy at the deadline.
set -eu
. "$(dirname "$0")/lib.sh"
HEALTHY_READS=2
safe service "$1"
case "$2" in '' | *[!0-9]*) die "verify: the minutes must be a whole number" ;; esac
deadline=$(($(now) + $2 * ${INC_MINUTE:-60}))
poll="${INC_POLL:-10}"
good=0
while :; do
    if api -G "$OPS_API/services/$1/health" | jq -e '.healthy == true' > /dev/null 2>&1; then
        good=$((good + 1))
        [ "$good" -ge "$HEALTHY_READS" ] && { echo "healthy"; exit 0; }
    else
        good=0
    fi
    [ "$(now)" -ge "$deadline" ] && { echo "not healthy after $2 minutes"; exit 1; }
    sleep "$poll"
done
