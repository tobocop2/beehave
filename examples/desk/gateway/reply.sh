#!/bin/sh
# Sends a reply once. $1: channel. $2: reply key. $3: masked sender. stdin: masked reply. Exit 0 sent or already sent, 2 unknown channel.
# A key already in the sent log is not sent again.
set -eu
here=$(dirname "$0")
. "$here/lib.sh"
grep -q "^$2	" "$DESK_HOME/sent.tsv" 2>/dev/null && exit 0
text=$(sh "$here/unmask.sh")
to=$(printf '%s\n' "$3" | sh "$here/unmask.sh")
case "$1" in
    email) printf 'To: %s\nSubject: Your request\n\n%s\n' "$to" "$text" | sendmail -t ;;
    chat)  jq -n -c --arg to "$to" --arg text "$text" '{to: $to, text: $text}' | curl -sS --fail -X POST "$CHAT_API/send" --data-binary @- > /dev/null ;;
    *) die "unknown channel $1" ;;
esac
printf '%s\t%s\n' "$2" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" >> "$DESK_HOME/sent.tsv"
