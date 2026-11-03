set -eu
# Wait for a change in a request. Usage: await.sh <req> <seconds>
# It takes in the inbox on each look. It exits 0 and prints "changed" when the ledger or the document changes or the status leaves
# pending, and exits 3 and prints "quiet" when the time passes with no change. The deadline is a change: the status becomes expired.
# APPROVAL_POLL sets the seconds between looks (default 5). Only the tests set it.
. "$(dirname "$0")/lib.sh"
[ $# -eq 2 ] || die "usage: await.sh <req> <seconds>"
safe req "$1"
case "$2" in '' | *[!0-9]*) die "seconds must be a whole number" ;; esac
poll="${APPROVAL_POLL:-5}"
fp() { printf '%s|%s' "$(sql "SELECT coalesce(max(seq),0) FROM events;")" "$(doc_hash "$(sql "SELECT doc FROM events WHERE type='opened' AND req='$(qt "$1")' LIMIT 1;")")"; }
start=$(fp "$1")
t0=$(date +%s)
while [ $(( $(date +%s) - t0 )) -lt "$2" ]; do
    sh "$SELF/intake.sh" > /dev/null
    [ "$(fp "$1")" = "$start" ] || { echo changed; exit 0; }
    st=$(NO_INTAKE=1 sh "$SELF/tally.sh" "$1" | jq -r .status)
    [ "$st" = pending ] || { echo changed; exit 0; }
    sleep "$poll"
done
echo quiet
exit 3
