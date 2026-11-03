#!/bin/sh
# Moves the alert files named on stdin (intake lines) from new/ to taken/. Exit 2: a path is outside the spool.
set -eu
. "$(dirname "$0")/lib.sh"
jq -r '.src' | while IFS= read -r src; do
    case "$src" in
        "$SPOOL"/new/alert-*.json) ;;
        *) die "ack: the path is outside the spool: $src" ;;
    esac
    [ -f "$src" ] || continue
    mkdir -p "$SPOOL/taken"
    mv "$src" "$SPOOL/taken/"
done
