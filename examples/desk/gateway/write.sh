#!/bin/sh
# Runs one saga step, the only write. $1: step (credit, uncredit, rebook, unrebook, reply). The engine sets BEEHAVE_WRITE_KEY: the key of the step.
# stdin: {"args", "reply", "channel", "to"}. Exit 0 done, 2 unknown step or no key. The key goes in the Idempotency-Key header.
# An undo step gets the key of the write that it undoes, so the system of record sees one pair of keys for one write.
set -eu
here=$(dirname "$0")
. "$here/lib.sh"
key="${BEEHAVE_WRITE_KEY:-}"
[ -n "$key" ] || die "BEEHAVE_WRITE_KEY is not set"
payload=$(cat)
case "$1" in
    reply)
        printf '%s' "$payload" | jq -r .reply |
            sh "$here/reply.sh" "$(printf '%s' "$payload" | jq -r .channel)" "$key" "$(printf '%s' "$payload" | jq -r .to)"
        ;;
    credit|uncredit|rebook|unrebook)
        # The engine also runs an undo for a write that failed. An undo reads its write back first with read-back.sh (the reader that the engine uses),
        # and does nothing only when the answer is "not applied" (exit 1). "Cannot tell" (exit 2) fails the undo, and the engine reports it.
        # The system of record must answer a read of an unknown key with HTTP 200 and {"status":"absent"}. A 404 or any HTTP error counts as "cannot tell".
        case "$1" in un*)
            seen=0
            sh "$here/read-back.sh" "${1#un}" || seen=$?
            case "$seen" in
                0) ;;
                1) exit 0 ;;
                *) die "could not read back ${1#un}-$key, so the undo cannot tell if it is needed" ;;
            esac ;;
        esac
        printf '%s' "$payload" | jq -c .args | sh "$here/unmask.sh" |
            api -X POST "$DESK_API/writes/$1" -H "Idempotency-Key: $1-$key" -H 'Content-Type: application/json' --data-binary @- > /dev/null
        ;;
    *) die "unknown saga step $1" ;;
esac
