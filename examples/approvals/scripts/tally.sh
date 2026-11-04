set -eu
# Print the state of a request as one JSON object. Usage: tally.sh <req>
# It takes in the inbox first, so the answer includes every message that has arrived. It writes only through intake.
# The status is pending, approved, rejected, expired or closed. See tally.jq for the rules.
. "$(dirname "$0")/lib.sh"
[ $# -eq 1 ] || die "usage: tally.sh <req>"
safe req "$1"
[ -n "${NO_INTAKE:-}" ] || sh "$SELF/intake.sh" > /dev/null || die "intake failed"
# One query, so one read. It returns the rows of the request and the rows for everybody ("-": version and delegate rows). Each row
# carries "seen", the seq of the last change (CHANGE_ROWS, the rule await.sh uses too). The snapshot goes to jq on stdin: it can be large.
state=$(sqlite3 -batch -json -cmd '.timeout 5000' "$LEDGER" "SELECT *, (SELECT coalesce(max(seq),0) FROM events WHERE $CHANGE_ROWS) AS seen FROM events WHERE req IN ('$(qt "$1")','-') ORDER BY seq;" \
    | jq -c -f "$SELF/tally.jq" --arg req "$1" --argjson now "$(now)") || die "no such request: $1"
[ -n "$state" ] || die "no such request: $1"
printf '%s\n' "$state"
