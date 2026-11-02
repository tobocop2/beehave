#!/bin/sh
# Prints one masked JSON line per new message of every channel. No arguments. Exit 0 always.
# A message that the mask cannot read is left out with a line on stderr, and it stays in its channel.
set -eu
here=$(dirname "$0")
CHANNELS="$here/channels"
MASK="$here/mask.sh"
for adapter in "$CHANNELS"/*.sh; do
    sh "$adapter"
done | while IFS= read -r line; do
    printf '%s\n' "$line" | sh "$MASK" json ||
        printf 'intake: the mask failed on %s; the message stays in its channel\n' "$(printf '%s' "$line" | jq -r .key)" >&2
done
