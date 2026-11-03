#!/bin/sh
# Tells the ops system the incident state. Arguments: incident, state (mitigated, rejected, no-action). Stdin: text.
set -eu
. "$(dirname "$0")/lib.sh"
safe incident "$1"
case "$2" in mitigated | rejected | no-action) ;; *) die "status: unknown state $2" ;; esac
jq -n -c --arg state "$2" --arg text "$(cat)" '{state: $state, text: $text}' |
    api -X POST "$OPS_API/incidents/$1/status" -H "Idempotency-Key: status-$1-$2" -H 'Content-Type: application/json' --data-binary @- > /dev/null
