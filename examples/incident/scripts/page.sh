#!/bin/sh
# Pages one role, once per proposal digest. Arguments: incident, role, text. Stdin: payload. Exit 0: paged or known. Exit 22: the ops API refused.
set -eu
. "$(dirname "$0")/lib.sh"
safe incident "$1"
safe role "$2"
payload=$(cat)
digest=$(printf '%s' "$payload" | shasum -a 256 | cut -c1-16)
jq -n -c --arg role "$2" --arg text "$3" --arg digest "$digest" --arg payload "$payload" \
    '{role: $role, text: $text, digest: $digest, payload: $payload}' |
    api -X POST "$OPS_API/incidents/$1/page" -H "Idempotency-Key: page-$1-$2-$digest" -H 'Content-Type: application/json' --data-binary @- > /dev/null
