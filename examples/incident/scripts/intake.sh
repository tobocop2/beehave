#!/bin/sh
# Prints one scrubbed JSON line per queued alert. Argument: seconds to wait when the spool is empty. Exit 0.
set -eu
here=$(dirname "$0")
. "$here/lib.sh"
wait_seconds="${1:-60}"
tick="${INC_TICK:-1}"
mkdir -p "$SPOOL/new"

print_alerts() {
    found=0
    for file in "$SPOOL"/new/alert-*.json; do
        [ -f "$file" ] || continue
        found=1
        line=$(jq -c --arg src "$file" '. + {src: $src}' "$file")
        printf '%s\n' "$line" | sh "$here/scrub.sh"
    done
    [ "$found" -eq 1 ]
}

waited=0
while :; do
    print_alerts && exit 0
    [ "$waited" -ge "$wait_seconds" ] && exit 0
    sleep "$tick"
    waited=$((waited + tick))
done
