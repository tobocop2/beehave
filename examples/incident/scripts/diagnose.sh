#!/bin/sh
# Reads metrics, deploys and logs with GET calls and prints them scrubbed. Stdin: incident JSON. Exit 3: metrics unread.
# The text is collected first and scrubbed after, so the exit code of a failed read is not the exit code of the scrub.
set -eu
here=$(dirname "$0")
. "$here/lib.sh"
DEPLOY_HOURS=24
LOG_LINES=200
svc=$(jq -er '.service')
safe service "$svc"
metrics=$(api -G "$OPS_API/services/$svc/metrics") || exit 3
deploys=$(api -G "$OPS_API/services/$svc/deploys" --data-urlencode "hours=$DEPLOY_HOURS") || deploys="unavailable"
logs=$(api -G "$OPS_API/services/$svc/logs" --data-urlencode "lines=$LOG_LINES") || logs="unavailable"
printf '== metrics\n%s\n== deploys in the last %s hours\n%s\n== last %s log lines\n%s\n' "$metrics" "$DEPLOY_HOURS" "$deploys" "$LOG_LINES" "$logs" | sh "$here/scrub.sh"
