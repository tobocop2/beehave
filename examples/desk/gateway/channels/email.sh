#!/bin/sh
# Prints one JSON line per message file in the new/ folder of the Maildir. No arguments. Exit 0 always. It does not read a folded header line.
set -eu
. "$(dirname "$0")/../lib.sh"
for f in "$MAILDIR"/new/*; do
    [ -f "$f" ] || continue
    awk -v src="$f" '
        BEGIN { body = 0 }
        body == 0 && /^$/ { body = 1; next }
        body == 0 && tolower($0) ~ /^message-id:/ { sub(/^[^:]*: */, ""); gsub(/[<>]/, ""); id = $0 }
        body == 0 && tolower($0) ~ /^from:/       { sub(/^[^:]*: */, ""); from = $0 }
        body == 0 && tolower($0) ~ /^subject:/    { sub(/^[^:]*: */, ""); subject = $0 }
        body == 1 { text = text $0 "\n" }
        END { printf "%s\t%s\t%s\t%s\t%s", id, src, from, subject, text }
    ' "$f" | jq -R -s -c 'split("\t") | {key: ("mail-" + .[0]), channel: "email", src: .[1], from: .[2], subject: .[3], text: .[4]}'
done
