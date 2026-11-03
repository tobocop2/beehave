set -eu
# Store a new version of a document. Usage: submit.sh <doc> <file> <author>
# The version is a commit in the docs repository. The ledger gets a version row with the hash of the file.
# A signer signs that hash, so a sign-off belongs to one exact version. A second submit of the same text changes nothing.
. "$(dirname "$0")/lib.sh"
[ $# -eq 3 ] || die "usage: submit.sh <doc> <file> <author>"
safe doc "$1"; safe author "$3"
[ -f "$2" ] || die "no such file: $2"
acquire || die "the ledger is busy"
cp "$2" "$AP_HOME/docs/$1.md"
h=$(doc_hash "$1")
known=$(sql "SELECT count(*) FROM events WHERE type='version' AND doc='$(qt "$1")' AND hash='$h';")
if [ "$known" -eq 0 ]; then
    git -C "$AP_HOME/docs" add "$1.md"
    git -C "$AP_HOME/docs" -c user.name=approval -c user.email=approval@desk.invalid commit -q -m "$1 $h by $3" || { release; die "the commit failed"; }
    append_raw version - "$1" "$h" "$3" - - '{}'
fi
release
printf '%s\n' "$h"
