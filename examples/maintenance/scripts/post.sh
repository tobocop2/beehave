#!/bin/sh
# Usage: post.sh <week>
# Posts the report as a comment on the status issue (STATUS_REPO and STATUS_ISSUE in $UPKEEP_HOME/settings.sh).
# It posts once for each week. A second call prints "already posted" and writes nothing.
set -eu
. "$(dirname "$0")/lib.sh"
wk=${1:?usage: post.sh <week>}
valid "$wk" || exit 2
file="$UPKEEP_HOME/reports/$wk.md"
[ -f "$file" ] || { echo "post: no report for $wk" >&2; exit 2; }
if [ -z "${STATUS_REPO:-}" ] || [ -z "${STATUS_ISSUE:-}" ]; then echo "no status issue set; report kept in $file"; exit 0; fi
if [ "$(state_get _report posted_week)" = "$wk" ]; then echo "already posted"; exit 0; fi
gh issue comment "$STATUS_ISSUE" --repo "$STATUS_REPO" --body-file "$file"
state_put _report posted_week "$wk"
echo "posted"
