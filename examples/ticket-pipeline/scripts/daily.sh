#!/bin/sh
# [$1]: the day, default yesterday (UTC). Starts the backfill workflow for that one day. Needs PIPE_PROJECT (the folder of this example) and PIPE_DIR.
# Cron line: 15 2 * * * PIPE_PROJECT=/path/to/example PIPE_DIR=/path/to/example sh /path/to/example/scripts/daily.sh
set -eu
BEEHAVE=${BEEHAVE:-beehave}
PROJECT=${PIPE_PROJECT_NAME:-pipeline}
cd "${PIPE_PROJECT:?set PIPE_PROJECT to the folder of the example}"
day=${1:-$(date -u -v-1d +%Y-%m-%d 2>/dev/null || date -u -d yesterday +%Y-%m-%d)}
printf '%s' "$day" | grep -Eq '^[0-9]{4}-[0-9]{2}-[0-9]{2}$' || { echo "not a date: $day" >&2; exit 70; }
# A second add of the same id is expected. Any other error stops the script.
if ! out=$(printf '%s %s\n' "$day" "$day" | "$BEEHAVE" item add "$PROJECT" "daily-$day" --title "daily run $day" --body-file /dev/stdin 2>&1); then
  case $out in *exists*) ;; *) echo "$out" >&2; exit 1 ;; esac
fi
exec "$BEEHAVE" run "daily-$day" "$PROJECT" --workflow backfill.star
