set -eu
# Print the change between two versions of a document. Usage: diff.sh <doc> <old-hash> <new-hash>
# Both hashes must be versions that the ledger knows. The docs repository keeps every version as a blob.
. "$(dirname "$0")/lib.sh"
[ $# -eq 3 ] || die "usage: diff.sh <doc> <old-hash> <new-hash>"
safe doc "$1"; safe old "$2"; safe new "$3"
for h in "$2" "$3"; do
    n=$(sql "SELECT count(*) FROM events WHERE type='version' AND doc='$(qt "$1")' AND hash='$h';")
    [ "$n" -gt 0 ] || die "the hash $h is not a version of $1"
done
git -C "$AP_HOME/docs" diff --no-color "$2" "$3"
