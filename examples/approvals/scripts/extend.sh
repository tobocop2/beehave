set -eu
# Give a request more time. Usage: extend.sh <req> <seconds> <who> <reason>
# The new deadline is the time now plus the seconds. One extension only: a second call is refused with exit 3.
# The ledger row records who decided. In the workflow, <who> is the name in the answer to the addressed wait (the engine does not prove it).
. "$(dirname "$0")/lib.sh"
[ $# -eq 4 ] || die "usage: extend.sh <req> <seconds> <who> <reason>"
safe req "$1"; safe who "$3"
case "$2" in '' | *[!0-9]*) die "seconds must be a whole number" ;; esac
acquire || die "the ledger is busy"
open=$(sql "SELECT count(*) FROM events WHERE type='opened' AND req='$(qt "$1")';")
shut=$(sql "SELECT count(*) FROM events WHERE type='closed' AND req='$(qt "$1")';")
used=$(sql "SELECT count(*) FROM events WHERE type='extended' AND req='$(qt "$1")';")
if [ "$open" -eq 0 ] || [ "$shut" -gt 0 ]; then release; die "the request is not open"; fi
if [ "$used" -gt 0 ]; then release; echo "already extended"; exit 3; fi
d=$(( $(now) + $2 ))
append_raw extended "$1" - - "$3" - - "$(jq -cn --argjson d "$d" --arg r "$4" '{deadline: $d, reason: $r}')"
release
echo "$d"
