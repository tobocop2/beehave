#!/bin/sh
# Saves the service state before the first write. Arguments: incident, service. Exit 2: the state is empty.
set -eu
. "$(dirname "$0")/lib.sh"
safe incident "$1"
safe service "$2"
before="$INC_HOME/incidents/$1/before.json"
mkdir -p "$INC_HOME/incidents/$1"
state=$(api -G "$OPS_API/services/$2/state")
printf '%s' "$state" | jq -e 'type == "object" and length > 0' > /dev/null || die "snapshot: the state of $2 is empty"
printf '%s\n' "$state" > "$before"
