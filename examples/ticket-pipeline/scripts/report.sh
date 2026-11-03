#!/bin/sh
# $1 $2: first and last day. Prints the days by version, the days not loaded and the label counts.
set -eu
. "$(dirname "$0")/lib.sh"
check_day "$1"
check_day "$2"
init_wh
echo "Range $1 to $2"
echo "Loaded days by version:"
sql "SELECT '  version ' || version || ': ' || COUNT(*) || ' days, ' || SUM(rows) || ' rows' FROM manifest WHERE day BETWEEN '$1' AND '$2' GROUP BY version ORDER BY version;"
missing=$(sql "WITH RECURSIVE d(x) AS (SELECT date('$1') UNION ALL SELECT date(x, '+1 day') FROM d WHERE x < date('$2'))
               SELECT COUNT(*) FROM d WHERE x NOT IN (SELECT day FROM manifest);")
echo "Days not loaded: $missing"
echo "Labels:"
sql "SELECT '  ' || label || ': ' || COUNT(*) FROM labeled WHERE day BETWEEN '$1' AND '$2' GROUP BY label ORDER BY label;"
