#!/bin/sh
# $1: the day. Exit 0 when the source has closed the day, exit 2 when it is still open.
set -eu
. "$(dirname "$0")/lib.sh"
check_day "$1"
answer=$(api "$SOURCE_API/days/$1/status") || die "the source did not answer for $1"
printf '%s' "$answer" | jq -e '.closed == true' > /dev/null && exit 0
echo "the source has not closed $1"
exit 2
