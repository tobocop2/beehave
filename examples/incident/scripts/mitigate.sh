#!/bin/sh
# Runs one mitigation with an idempotency key and reads it back. Arguments: kind, key. Stdin: args JSON. Exit 2: refused or not applied.
set -eu
. "$(dirname "$0")/lib.sh"
case "$1" in rollback | restart | scale | flag_off) ;; *) die "mitigate: unknown kind $1" ;; esac
safe key "$2"
args=$(cat)
printf '%s' "$args" | jq -e 'type == "object"' > /dev/null || die "mitigate: the args are not an object"
printf '%s' "$args" |
    api -X POST "$OPS_API/mitigations/$1" -H "Idempotency-Key: $2" -H 'Content-Type: application/json' --data-binary @- > /dev/null
back=$(api -G "$OPS_API/mitigations/$2")
[ "$(printf '%s' "$back" | jq -r .state)" = "applied" ] || die "mitigate: the read-back of $2 says $(printf '%s' "$back" | jq -r .state)"
echo "applied $2"
