#!/bin/sh
# Shared helpers; source it. The pipeline home is $PIPE_HOME (default ~/.pipe) with config, credential.header, warehouse.db and stage/<day>/.
set -eu

PIPE_HOME=${PIPE_HOME:-$HOME/.pipe}
[ -f "$PIPE_HOME/config" ] && . "$PIPE_HOME/config"
WH=$PIPE_HOME/warehouse.db
STAGE=$PIPE_HOME/stage
SCRIPTS=$(cd "$(dirname "$0")" && pwd)
SCHEMA=$SCRIPTS/../schema
COST_CENTS_PER_100=${COST_CENTS_PER_100:-30}
UNSURE_MAX_PCT=${UNSURE_MAX_PCT:-20}
DRIFT_MAX_POINTS=${DRIFT_MAX_POINTS:-30}
PIPE_VERSION=${PIPE_VERSION:-1}

# Exit 70 marks a failure; codes 1 to 3 belong to each script.
die() { echo "$*" >&2; exit 70; }

# Every value that reaches SQL passes a check like this one.
check_day() {
    printf '%s' "$1" | grep -Eq '^[0-9]{4}-[0-9]{2}-[0-9]{2}$' || die "not a date: $1"
    # sqlite date() keeps 2026-02-30 as it is; the trip through julianday changes it.
    [ "$(sqlite3 :memory: "SELECT date(julianday('$1'))")" = "$1" ] || die "not a real date: $1"
}

check_int() { printf '%s' "$2" | grep -Eq '^[0-9]+$' || die "$1 is not a whole number: $2"; }

# Runs SQL against the warehouse and exits 70 on failure, so a database error never reads as the "odd labels" code 1.
sql() { sqlite3 -bail -cmd '.timeout 20000' "$WH" "$@" || die "the warehouse call failed"; }

init_wh() {
    mkdir -p "$PIPE_HOME" "$STAGE"
    sqlite3 -bail -cmd '.timeout 20000' "$WH" < "$SCHEMA/warehouse.sql" || die "the warehouse schema failed"
}

sha256() {
    if command -v sha256sum > /dev/null 2>&1; then sha256sum "$@" | cut -d' ' -f1; else shasum -a 256 "$@" | cut -d' ' -f1; fi
}

# The credential header comes from a file, so no process list or log shows it.
api() {
    curl -sS --fail --max-time 60 -H "@$PIPE_HOME/credential.header" "$@"
}

count_lines() { wc -l < "$1" | tr -d ' '; }
