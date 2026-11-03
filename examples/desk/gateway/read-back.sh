#!/bin/sh
# Reads a write back. $1: saga step. The engine sets OBEE_WRITE_KEY: the key of the step. It changes nothing.
# Exit 0: the write is applied (for a reply: the key is in the sent log). Exit 1: the write is not applied. Exit 2: the script cannot tell.
# The engine runs this before each write, and write.sh runs it before an undo, so one reader answers both.
set -eu
. "$(dirname "$0")/lib.sh"
key="${OBEE_WRITE_KEY:-}"
[ -n "$key" ] || die "OBEE_WRITE_KEY is not set"
if [ "$1" = reply ]; then
    [ -r "$DESK_HOME/sent.tsv" ] || exit 2
    grep -q "^$key	" "$DESK_HOME/sent.tsv" || exit 1
    exit 0
fi
# curl runs on its own, so its exit status is checked: sh has no pipefail. A body that comes with an HTTP error is never read.
body=$(api -G "$DESK_API/writes/$1-$key") || exit 2
state=$(printf '%s' "$body" | jq -er .status) || exit 2
[ "$state" = applied ] || exit 1
