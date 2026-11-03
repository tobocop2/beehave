set -eu
# Print the state of a request as one JSON object. Usage: tally.sh <req>
# It takes in the inbox first, so the answer includes every message that has arrived. It writes only through intake.
# The status is pending, approved, rejected, expired or closed. See tally.jq for the rules.
. "$(dirname "$0")/lib.sh"
[ $# -eq 1 ] || die "usage: tally.sh <req>"
safe req "$1"
[ -n "${NO_INTAKE:-}" ] || sh "$SELF/intake.sh" > /dev/null || die "intake failed"
doc=$(sql "SELECT doc FROM events WHERE type='opened' AND req='$(qt "$1")' LIMIT 1;")
[ -n "$doc" ] || die "no such request: $1"
h=$(doc_hash "$doc")
known=$(sql "SELECT count(*) FROM events WHERE type='version' AND doc='$(qt "$doc")' AND hash='$h';")
[ "$known" -gt 0 ] && known=true || known=false
ev=$(sqlite3 -batch -json -cmd '.timeout 5000' "$LEDGER" "SELECT * FROM events WHERE req IN ('$(qt "$1")','-') ORDER BY seq;")
[ -n "$ev" ] || ev='[]'
jq -nc -f "$SELF/tally.jq" --argjson ev "$ev" --arg req "$1" --arg hash "$h" --argjson known "$known" --argjson now "$(now)"
