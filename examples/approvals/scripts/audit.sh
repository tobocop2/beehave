set -eu
# Check the audit trail of a request and print it. Usage: audit.sh <req> <hash> <head>
# 1. The chain: each row must carry the hash of the row before it, and its own hash must match its fields.
#    This finds a row that was changed, a row removed from the middle and rows in another order.
# 2. Each signed message that a row cites must still verify against allowed_signers.
# 3. The request must be closed as approved at exactly <hash>. A release pipeline calls it this way.
#    The hash "-" skips this check, for a request that ended as rejected or expired.
# 4. The ledger must still hold the row that the close recorded. <head> is "seq:row_hash", as workflow.star
#    reports it in the outcome comment of the item and as finalize.sh prints it. The head lives outside the ledger file,
#    so this finds rows removed from the tail, which the chain alone cannot see. The head is required: a call
#    without it is refused (exit 2), because a check that cannot see a cut tail must not pass.
#    A person who can write the ledger file can still rebuild every row from the first change on and make a new chain.
#    The head defeats that only when the verifier holds the head from the time of the close.
# Exit 0 when all hold. Exit 1 and a "BROKEN" line when one does not. Rows print as tab-separated text.
. "$(dirname "$0")/lib.sh"
[ $# -eq 3 ] || die "REFUSE: usage: audit.sh <req> <hash> <head> (the head from the close is required; use - for the hash of a request that was not approved)"
safe req "$1"
case "$3" in [0-9]*:[0-9a-f]*) ;; *) die "REFUSE: head must be seq:row_hash" ;; esac
rows=$(sqlite3 -batch -json -cmd '.timeout 5000' "$LEDGER" "SELECT * FROM events ORDER BY seq;")
[ -n "$rows" ] || { echo "BROKEN the ledger is empty"; exit 1; }
US=$(printf '\037')

# Print one BROKEN line for each row that does not follow the row before it or does not match its hash.
check_chain() {
    prev=genesis
    printf '%s' "$rows" | jq -r '.[] | [(.seq|tostring), .prev, .row_hash, "\(.at)|\(.type)|\(.req)|\(.doc)|\(.hash)|\(.who)|\(.role)|\(.decision)|\(.detail)|\(.prev)"] | join("\u001f")' |
        {
            while IFS="$US" read -r seq p rh text; do
                [ "$p" = "$prev" ] || echo "BROKEN row $seq does not follow the row before it"
                [ "$(printf '%s' "$text" | sha)" = "$rh" ] || echo "BROKEN row $seq does not match its hash"
                prev="$rh"
            done
        }
}

# Print one BROKEN line for each signed message that does not verify.
check_signatures() {
    printf '%s' "$rows" | jq -r '.[] | select(.type == "signoff" or .type == "delegate") | .detail | fromjson | .msg' |
        while read -r m; do
            [ -n "$m" ] || continue
            by=$(sed -n 's/^by=//p' "$AP_HOME/taken/$m.msg" 2> /dev/null | head -n 1)
            ssh-keygen -Y verify -f "$AP_HOME/allowed_signers" -I "$by" -n beehave-signoff -s "$AP_HOME/taken/$m.msg.sig" < "$AP_HOME/taken/$m.msg" > /dev/null 2>&1 ||
                echo "BROKEN the signed message $m does not verify"
        done
}

problems=$(check_chain; check_signatures)
n=$(printf '%s' "$rows" | jq 'length')
printf '%s' "$rows" | jq -r --arg r "$1" '.[] | select(.req == $r or .req == "-") | [(.at|tostring), .type, .who, .role, .decision, .hash[0:10], .detail] | @tsv'
if [ "$2" != "-" ]; then
    closed=$(printf '%s' "$rows" | jq -r --arg r "$1" --arg h "$2" '[.[] | select(.type == "closed" and .req == $r and .decision == "approved" and .hash == $h)] | length')
    [ "$closed" -eq 1 ] || problems="$problems
BROKEN the request is not closed as approved at $2"
fi
found=$(printf '%s' "$rows" | jq -r --arg s "${3%%:*}" --arg h "${3#*:}" '[.[] | select((.seq|tostring) == $s and .row_hash == $h)] | length')
[ "$found" -eq 1 ] || problems="$problems
BROKEN the row $3 that the close recorded is gone: the tail of the ledger was cut or rewritten"
problems=$(printf '%s\n' "$problems" | sed '/^$/d')
if [ -n "$problems" ]; then printf '%s\n' "$problems"; exit 1; fi
echo "chain ok: $n rows"
