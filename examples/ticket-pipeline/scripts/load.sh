#!/bin/sh
# $1: the day. Replaces the day in one transaction and reads it back; same input at the same version changes nothing. Exit 70 on failure.
set -eu
. "$(dirname "$0")/lib.sh"
check_day "$1"
day=$1
check_int PIPE_VERSION "$PIPE_VERSION"
init_wh
rec=$STAGE/$day/records.jsonl
lab=$STAGE/$day/labels.jsonl
[ -f "$rec" ] || die "no extract for $day"
n=$(count_lines "$rec")
sha=$(cat "$STAGE/$day/sha")
have=$(sql "SELECT version || ' ' || input_sha FROM manifest WHERE day = '$day';")
if [ "$have" = "$PIPE_VERSION $sha" ]; then
    echo "unchanged: $day is loaded at version $PIPE_VERSION"
    exit 0
fi
if [ "$n" = "0" ]; then
    : > "$STAGE/$day/labels.tsv"
else
    [ "$(count_lines "$lab")" = "$n" ] || die "the labels of $day do not match the extract; run validate.sh first"
    jq -r '[.id, .label] | @tsv' "$lab" > "$STAGE/$day/labels.tsv"
fi
# -bail stops at the first error and the open transaction rolls back when the shell closes. The exit code 70 marks a failure.
sqlite3 -bail "$WH" <<SQL || die "the load of $day failed and was rolled back"
.timeout 20000
BEGIN IMMEDIATE;
CREATE TEMP TABLE incoming(id TEXT, label TEXT);
.mode tabs
.import '$STAGE/$day/labels.tsv' incoming
DELETE FROM labeled WHERE day = '$day';
INSERT INTO labeled(day, id, label) SELECT '$day', id, label FROM incoming;
INSERT OR REPLACE INTO manifest(day, version, rows, input_sha, loaded_at)
    SELECT '$day', '$PIPE_VERSION', COUNT(*), '$sha', strftime('%Y-%m-%dT%H:%M:%SZ', 'now') FROM incoming;
COMMIT;
SQL
back=$(sql "SELECT (SELECT COUNT(*) FROM labeled WHERE day = '$day') || ' ' || COALESCE((SELECT version || ' ' || rows FROM manifest WHERE day = '$day'), 'none');")
[ "$back" = "$n $PIPE_VERSION $n" ] || die "read-back of $day failed: got '$back', wanted '$n $PIPE_VERSION $n'"
echo "loaded $n rows for $day at version $PIPE_VERSION"
