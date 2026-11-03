#!/bin/sh
# Sends the saved state back, once per key. Arguments: incident, service, mitigation key. Exit 2: no saved state.
set -eu
. "$(dirname "$0")/lib.sh"
safe incident "$1"
safe service "$2"
safe key "$3"
before="$INC_HOME/incidents/$1/before.json"
[ -s "$before" ] || die "rollback: no saved state for $1"
api -X POST "$OPS_API/services/$2/restore" -H "Idempotency-Key: rollback-$3" -H 'Content-Type: application/json' --data-binary @"$before" > /dev/null
echo "restored $2"
