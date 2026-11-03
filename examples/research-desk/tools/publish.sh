#!/bin/sh
# Publish the report once. Usage: publish.sh <run>
# The key is the run name and the SHA-256 of the report, so a second publish of the same report writes the same page. The script reads the page back
# and compares its hash with the hash of the report. A mismatch exits 3. WIKI_API is the address of the page store. The one system that this example
# does not ship is the wiki: it must answer PUT <WIKI_API>/pages/<key> and GET <WIKI_API>/pages/<key>.
# The header file holds the credential (mode 600). It is read by curl with -H @file, so the secret is never on a command line.
set -eu
. "$(dirname "$0")/lib.sh"
: "${WIKI_API:?WIKI_API is not set}"
[ $# -eq 1 ] || die "usage: publish.sh <run>"
run=$1
header=${RESEARCH_HEADER_FILE:-$HOME/.research/credential.header}
report=$(run_dir "$run")/report.md
sh "$(dirname "$0")/assemble.sh" "$run" > "$report"
hash=$(sha < "$report")
key="$run-$hash"
curl -sS --fail-with-body -X PUT -H "@$header" -H "Idempotency-Key: $key" --data-binary "@$report" "$WIKI_API/pages/$key"
back=$(run_dir "$run")/readback.md
curl -sS --fail-with-body -H "@$header" -o "$back" "$WIKI_API/pages/$key"
if [ "$(sha < "$back")" != "$hash" ]; then
  echo "The wiki page $key does not hold the report. The hash of the page differs from $hash." >&2
  exit 3
fi
echo "published $key"
