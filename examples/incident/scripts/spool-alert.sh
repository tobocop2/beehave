#!/bin/sh
# Writes the alert on stdin to the spool as a file named by its fingerprint. Exit 0: queued or known. Exit 2: refused.
set -eu
. "$(dirname "$0")/lib.sh"
MAX_BYTES=65536
body=$(head -c $((MAX_BYTES + 1)))
[ "${#body}" -le "$MAX_BYTES" ] || die "the alert is larger than 64 KB"
fp=$(printf '%s' "$body" | jq -er '.fingerprint') || die "the alert has no fingerprint"
safe fingerprint "$fp"
service=$(printf '%s' "$body" | jq -er '.service') || die "the alert has no service"
safe service "$service"
mkdir -p "$SPOOL/new" "$SPOOL/taken"
key="alert-$fp"
if [ -e "$SPOOL/new/$key.json" ] || [ -e "$SPOOL/taken/$key.json" ]; then
    echo "known"
    exit 0
fi
# An unknown severity becomes sev1, so a wrong label never makes an alert wait longer.
printf '%s' "$body" |
    jq -c --arg key "$key" '{key: $key, severity: (if (.severity // "") | IN("sev1","sev2","sev3") then .severity else "sev1" end), title: (.title // "alert"), service: .service, detail: (.detail // "")}' \
        > "$SPOOL/new/.tmp-$key"
mv "$SPOOL/new/.tmp-$key" "$SPOOL/new/$key.json"
echo "queued"
