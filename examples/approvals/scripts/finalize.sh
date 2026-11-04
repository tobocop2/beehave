set -eu
# Close a request. Usage: finalize.sh <req> <outcome>     outcome: approved, rejected or expired
# It reads the state again with the ledger locked, and closes only when the state is now what the caller saw.
# If a document change or a sign-off came between the caller's look and this call, it exits 4 and closes nothing.
# On success it writes certs/<req>.json (the state and every ledger row of the request) and prints its path.
# A request that is already closed with the same outcome prints the same path and exits 0.
# The second output line is "head <seq>:<row_hash>": the last row of the ledger at the close. The workflow records it in the
# item, outside the ledger file, and audit.sh checks it (see audit.sh).
. "$(dirname "$0")/lib.sh"
[ $# -eq 2 ] || die "usage: finalize.sh <req> <outcome>"
safe req "$1"
case "$2" in approved | rejected | expired) ;; *) die "outcome must be approved, rejected or expired" ;; esac
sh "$SELF/intake.sh" > /dev/null || die "intake failed"
acquire || die "the ledger is busy"
t=$(NO_INTAKE=1 sh "$SELF/tally.sh" "$1") || { release; die "no state for $1"; }
st=$(printf '%s' "$t" | jq -r .status)
if [ "$st" = closed ]; then
    release
    [ "$(printf '%s' "$t" | jq -r .outcome)" = "$2" ] || { echo "closed as $(printf '%s' "$t" | jq -r .outcome)"; exit 4; }
    echo "$AP_HOME/certs/$1.json"
    echo "head $(jq -r .head "$AP_HOME/certs/$1.json")"; exit 0
fi
if [ "$st" != "$2" ]; then release; echo "state is $st"; exit 4; fi
doc=$(printf '%s' "$t" | jq -r .doc); h=$(printf '%s' "$t" | jq -r .hash)
append_raw closed "$1" "$doc" "$h" - - "$2" "$(printf '%s' "$t" | jq -c '{satisfied, missing, extensions}')"
ev=$(sqlite3 -batch -json -cmd '.timeout 5000' "$LEDGER" "SELECT * FROM events WHERE req IN ('$(qt "$1")','-') ORDER BY seq;")
head=$(sql "SELECT seq || ':' || row_hash FROM events ORDER BY seq DESC LIMIT 1;")
release
# The state and the rows go to jq on stdin: the rows can be far over the size of one argument.
{ printf '%s\n' "$t"; printf '%s\n' "$ev"; } | jq -n --arg head "$head" '[inputs] as $in | {state: $in[0], head: $head, ledger: $in[1]}' > "$AP_HOME/certs/$1.json"
echo "$AP_HOME/certs/$1.json"
echo "head $head"
