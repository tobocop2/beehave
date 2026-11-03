set -eu
# Tell people that a sign-off is wanted. Usage: notify.sh <req> <key> <roles>   (the message text on stdin)
# <roles> is a comma list of role names, or @escalate for the people that the policy names for the deadline.
# The people are the members of each role plus the delegates in force now. The key makes the notice happen once:
# the notifier gets it as the Idempotency-Key header, and sent.tsv records it. A second call with the same key sends nothing.
# The notifier (NOTIFY_API) is the one system outside the desk that this example does not provide.
. "$(dirname "$0")/lib.sh"
[ $# -eq 3 ] || die "usage: notify.sh <req> <key> <roles>"
safe req "$1"; safe key "$2"
text=$(cat)
if awk -F '\t' -v k="$2" '$1 == k { f = 1 } END { exit !f }' "$AP_HOME/sent.tsv" 2> /dev/null; then echo "already sent $2"; exit 0; fi
t=$(sh "$SELF/tally.sh" "$1") || die "no state for $1"
case "$3" in
    @escalate) to=$(printf '%s' "$t" | jq -c '.escalate_to') ;;
    @author) to=$(printf '%s' "$t" | jq -c '[.author]') ;;
    *) to=$(printf '%s' "$t" | jq -c --arg r "$3" '[.roles | to_entries[] | select(.key as $k | ($r | split(",")) | index($k)) | .value.people[]] | unique') ;;
esac
[ "$to" != '[]' ] || die "no one to tell for $3"
body=$(jq -cn --argjson to "$to" --arg req "$1" --arg doc "$(printf '%s' "$t" | jq -r .doc)" --arg hash "$(printf '%s' "$t" | jq -r .hash)" --arg text "$text" '{to: $to, req: $req, doc: $doc, hash: $hash, text: $text}')
curl -sS --fail-with-body -H @"$AP_HOME/credential.header" -H "Idempotency-Key: $2" -H 'Content-Type: application/json' -X POST "$NOTIFY_API/messages" -d "$body" > /dev/null || {
    append notify-failed "$1" - - - - - "$(jq -cn --arg k "$2" --argjson to "$to" '{key: $k, to: $to}')"
    die "the notifier refused the notice $2"
}
printf '%s\t%s\n' "$2" "$(now)" >> "$AP_HOME/sent.tsv"
append notified "$1" - - - - - "$(jq -cn --arg k "$2" --argjson to "$to" '{key: $k, to: $to}')"
echo "sent $2"
