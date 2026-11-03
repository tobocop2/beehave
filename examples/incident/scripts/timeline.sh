#!/bin/sh
# Appends a timed, scrubbed line to the incident timeline, or prints it. Usage: add <id> <text> | show <id>.
set -eu
here=$(dirname "$0")
. "$here/lib.sh"
safe incident "$2"
dir="$INC_HOME/incidents/$2"
mkdir -p "$dir"
case "$1" in
    add)
        text=$(printf '%s' "$3" | sh "$here/scrub.sh" | tr '\n\t' '  ')
        printf '%s\t%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$text" >> "$dir/timeline.tsv"
        ;;
    show) cat "$dir/timeline.tsv" 2> /dev/null || echo "(no entries)" ;;
    *) die "usage: timeline.sh add <id> <text> | show <id>" ;;
esac
