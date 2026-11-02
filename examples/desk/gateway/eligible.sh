#!/bin/sh
# Checks eligibility against the live system. $1: intent kind. stdin: intent args. Exit 0 eligible, 1 not eligible (stdout: reason), 2 check failed.
set -eu
. "$(dirname "$0")/lib.sh"
args=$(cat)
answer=$(api -G "$DESK_API/eligibility/$1" --data-urlencode "args=$args") || exit 2
printf '%s' "$answer" | jq -e '.eligible == true' > /dev/null && exit 0
printf '%s' "$answer" | jq -r '.reason // "The request does not meet the policy."'
exit 1
