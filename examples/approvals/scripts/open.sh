set -eu
# Open a request. Usage: open.sh <req> <doc> <kind> <author>   (the policy row of the kind, as JSON, on stdin)
# The ledger keeps a copy of the row and the deadline, so later changes to policy.star do not change this request.
# A second call for the same request prints the first answer.
. "$(dirname "$0")/lib.sh"
[ $# -eq 4 ] || die "usage: open.sh <req> <doc> <kind> <author>"
safe req "$1"; safe doc "$2"; safe kind "$3"; safe author "$4"
policy=$(jq -c . ) || die "the policy on stdin is not JSON"
window=$(printf '%s' "$policy" | jq -r '.window // empty')
case "$window" in '' | *[!0-9]*) die "the policy has no window" ;; esac
[ "$window" -ge 60 ] || die "the window must be at least 60 seconds"
acquire || die "the ledger is busy"
done_already=$(sql "SELECT count(*) FROM events WHERE type='opened' AND req='$(qt "$1")';")
if [ "$done_already" -eq 0 ]; then
    h=$(doc_hash "$2")
    [ -n "$h" ] || { release; die "the document $2 has no version; run submit.sh first"; }
    deadline=$(( $(now) + window ))
    detail=$(jq -cn --argjson p "$policy" --argjson d "$deadline" --arg a "$4" --arg k "$3" '{policy: $p, deadline: $d, author: $a, kind: $k}')
    append_raw opened "$1" "$2" "$h" "$4" - - "$detail"
fi
release
sql "SELECT detail FROM events WHERE type='opened' AND req='$(qt "$1")' LIMIT 1;" | jq -c '{deadline, author, kind}'
