#!/bin/sh
# $1 $2: first and last day. Prints the days that the warehouse does not hold at PIPE_VERSION or later. Exit 70 on a bad range.
set -eu
. "$(dirname "$0")/lib.sh"
check_day "$1"
check_day "$2"
check_int PIPE_VERSION "$PIPE_VERSION"
max=${PIPE_MAX_DAYS:-5000}
check_int PIPE_MAX_DAYS "$max"
init_wh
span=$(sql "SELECT CAST(julianday('$2') - julianday('$1') AS INTEGER) + 1;")
[ "$span" -ge 1 ] || die "the last day $2 is before the first day $1"
[ "$span" -le "$max" ] || die "the range has $span days, and the limit is $max"
sql "WITH RECURSIVE d(x) AS (SELECT date('$1') UNION ALL SELECT date(x, '+1 day') FROM d WHERE x < date('$2'))
     SELECT x FROM d
     WHERE x NOT IN (SELECT day FROM manifest WHERE CAST(version AS INTEGER) >= $PIPE_VERSION)
     ORDER BY x;"
