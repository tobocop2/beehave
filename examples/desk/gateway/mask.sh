#!/bin/sh
# Masks personal values with tokens and stores each pair in the vault. Exit 0 done, 2 bad usage.
#   mask.sh json: one message on stdin; masks from, subject and text.
#   mask.sh text [name]: plain text on stdin; name is one more literal to mask.
# A booking reference stays unmasked. Patterns miss some values, so a production desk adds an entity model.
set -eu
. "$(dirname "$0")/lib.sh"

token() { # kind value
    h=$(printf '%s%s' "$MASK_SALT" "$2" | sha256 | cut -c1-10)
    t="M_$1_$h"
    grep -q "^$t	" "$VAULT" 2>/dev/null || printf '%s\t%s\n' "$t" "$2" >> "$VAULT"
    printf '%s' "$t"
}

scan() { # One grep of $text: $1 the kind, $2 the pattern. Prints "kind<TAB>value" per hit, no line when nothing matches.
    rc=0
    found=$(printf '%s\n' "$text" | grep -oE "$2") || rc=$?
    [ "$rc" -le 1 ] || die "mask.sh: the $1 scan failed"
    if [ -n "$found" ]; then printf '%s\n' "$found" | sed "s/^/$1	/"; fi
}

# Prints "kind<TAB>value" for each personal value on stdin. A phone number has a country code or spaces, so a date is not one. A failed scan dies; a missing match is not an error.
find_values() {
    text=$(cat)
    scan EMAIL '[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}'
    scan CARD '[0-9]{4}([ -]?[0-9]{4}){3}'
    scan PHONE '(\+[0-9]{1,3} ?)?(\([0-9]{3}\) ?|[0-9]{3} )[0-9]{3}[ -][0-9]{4}'
}

mask_text() { # stdin: the text. $1: one more literal to mask, the sender's name. Prints the masked text.
    MASK_IN=$(cat)
    export MASK_IN
    values=$(printf '%s\n' "$MASK_IN" | find_values)
    pairs=$(
        if [ -n "$values" ]; then
            printf '%s\n' "$values" | sort -u | while IFS="$(printf '\t')" read -r kind value; do
                printf '%s\t%s\n' "$value" "$(token "$kind" "$value")"
            done
        fi
        if [ -n "${1:-}" ]; then printf '%s\t%s\n' "$1" "$(token NAME "$1")"; fi
    )
    # A literal replace in awk. The text comes in through the environment, because awk -v refuses a newline.
    printf '%s\n' "$pairs" | awk '
        BEGIN { FS = "\t"; text = ENVIRON["MASK_IN"] }
        NF == 2 { n++; old[n] = $1; new[n] = $2 }
        END {
            for (i = 1; i <= n; i++) {
                out = ""; rest = text
                while ((p = index(rest, old[i])) > 0) { out = out substr(rest, 1, p - 1) new[i]; rest = substr(rest, p + length(old[i])) }
                text = out rest
            }
            printf "%s", text
        }'
}

case "${1:-}" in
    text) mask_text "${2:-}" ;;
    json)
        msg=$(cat)
        name=$(printf '%s' "$msg" | jq -r '.from | sub(" *<.*>$"; "") | gsub("\""; "")')
        from=$(printf '%s' "$msg" | jq -r .from | mask_text "$name")
        subject=$(printf '%s' "$msg" | jq -r .subject | mask_text "$name")
        text=$(printf '%s' "$msg" | jq -r .text | mask_text "$name")
        printf '%s' "$msg" | jq -c --arg f "$from" --arg s "$subject" --arg t "$text" '.from = $f | .subject = $s | .text = $t'
        ;;
    *) die "usage: mask.sh json|text" ;;
esac
