#!/bin/sh
# Prints one JSON line per chat message file {id, user, text} in the spool folder. No arguments. Exit 0 always.
set -eu
. "$(dirname "$0")/../lib.sh"
for f in "$CHAT_SPOOL"/*.json; do
    [ -f "$f" ] || continue
    jq -c --arg src "$f" '{key: ("chat-" + .id), channel: "chat", src: $src, from: .user, subject: (.text | .[0:60]), text: .text}' "$f"
done
