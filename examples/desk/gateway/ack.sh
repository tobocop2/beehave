#!/bin/sh
# Marks the messages of an intake as read. stdin: the intake lines. Exit 0 when done.
set -eu
. "$(dirname "$0")/lib.sh"
jq -r '[.channel, .src] | @tsv' | while IFS="$(printf '\t')" read -r channel src; do
    [ -f "$src" ] || continue
    case "$channel" in
        email) mv "$src" "$MAILDIR/cur/" ;;
        chat)  mkdir -p "$CHAT_SPOOL/done" && mv "$src" "$CHAT_SPOOL/done/" ;;
    esac
done
