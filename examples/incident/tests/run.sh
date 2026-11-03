#!/bin/sh
# Tests every desk script against a test desk folder made by mktemp -d under tests/ (the folder stays after the run; delete it by hand). A stand-in curl is early on PATH.
# The stand-in logs its arguments, reads stdin only when a call sends it (@-), and answers from files in $T/resp.
# Usage: sh tests/run.sh   (from any folder). Test folders stay in place after the run.
set -u
HERE=$(cd "$(dirname "$0")" && pwd)
S="$HERE/../scripts"
T=$(mktemp -d "$HERE/run.XXXXXX")
mkdir -p "$T/bin" "$T/resp" "$T/home/spool" "$T/work"
pass=0; fail=0
ok() { pass=$((pass + 1)); printf 'ok   %s\n' "$1"; }
bad() { fail=$((fail + 1)); printf 'FAIL %s\n' "$1"; }
check() { # name command...
    n="$1"; shift
    if "$@" > /dev/null 2>&1; then ok "$n"; else bad "$n"; fi
}
has() { grep -q -e "$1" "$2"; }
hasnt() { ! grep -q -e "$1" "$2"; }

# The stand-in for curl.
cat > "$T/bin/curl" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >> "$FAKE_LOG"
url=""; sends=0
for a in "$@"; do
    case "$a" in http*) url="$a" ;; @-) sends=1 ;; esac
done
if [ "$sends" -eq 1 ]; then cat >> "$FAKE_BODY"; printf '\n' >> "$FAKE_BODY"; fi
key=$(printf '%s' "$url" | sed -e 's#^[a-z]*://[^/]*##' -e 's#?.*##' -e 's#/#_#g')
[ -e "$FAKE_RESP/$key.fail" ] && { echo "boom" >&2; exit 22; }
if [ -e "$FAKE_RESP/$key.seq" ]; then # one line per call; the first line is used up
    head -n 1 "$FAKE_RESP/$key.seq"
    tail -n +2 "$FAKE_RESP/$key.seq" > "$FAKE_RESP/$key.tmp"
    [ -s "$FAKE_RESP/$key.tmp" ] && mv "$FAKE_RESP/$key.tmp" "$FAKE_RESP/$key.seq"
    exit 0
fi
[ -e "$FAKE_RESP/$key" ] && cat "$FAKE_RESP/$key"
exit 0
EOF
chmod +x "$T/bin/curl"
printf 'OPS_API=http://ops.test\nSPOOL=%s\n' "$T/home/spool" > "$T/home/config"
printf 'Authorization: Bearer TESTONLY\n' > "$T/home/credential.header"
export PATH="$T/bin:$PATH" INC_HOME="$T/home" FAKE_LOG="$T/curl.log" FAKE_BODY="$T/curl.body" FAKE_RESP="$T/resp" INC_TICK=1 INC_POLL=1 INC_MINUTE=1
: > "$FAKE_LOG"; : > "$FAKE_BODY"
reset() { : > "$FAKE_LOG"; : > "$FAKE_BODY"; }
ALERT='{"fingerprint":"abc123","severity":"sev2","title":"checkout errors","service":"checkout","detail":"db password=hunter2 failed"}'

echo "--- spool-alert"
printf '%s' "$ALERT" | sh "$S/spool-alert.sh" > "$T/o1" 2>&1; check "queues a new alert" has queued "$T/o1"
printf '%s' "$ALERT" | sh "$S/spool-alert.sh" > "$T/o2" 2>&1; check "a repeat is known" has known "$T/o2"
printf '%s' '{"fingerprint":"../x","severity":"sev1","title":"t","service":"s"}' | sh "$S/spool-alert.sh" > "$T/o3" 2>&1; rc=$?
check "refuses a path in the fingerprint" test "$rc" -eq 2
printf '%s' '{"fingerprint":"z9","severity":"urgent","title":"t","service":"s"}' | sh "$S/spool-alert.sh" > /dev/null 2>&1
check "unknown severity becomes sev1" jq -e '.severity == "sev1"' "$T/home/spool/new/alert-z9.json"
printf '%s' '{"severity":"sev1","title":"t","service":"s"}' | sh "$S/spool-alert.sh" > /dev/null 2>&1; rc=$?
check "refuses an alert with no fingerprint" test "$rc" -eq 2

echo "--- intake and ack"
rm -f "$T/home/spool/new/alert-z9.json"
sh "$S/intake.sh" 3 > "$T/in1" 2>&1
check "intake prints the alert" jq -e '.key == "alert-abc123" and .severity == "sev2"' "$T/in1"
check "intake scrubs the detail" hasnt hunter2 "$T/in1"
sh "$S/ack.sh" < "$T/in1"; check "ack moves the file" test -f "$T/home/spool/taken/alert-abc123.json"
check "ack leaves new/ empty" test ! -e "$T/home/spool/new/alert-abc123.json"
printf '%s' "$ALERT" | sh "$S/spool-alert.sh" > "$T/o4" 2>&1; check "a taken alert is still known" has known "$T/o4"
start=$(date +%s); sh "$S/intake.sh" 2 > "$T/in2" 2>&1; took=$(($(date +%s) - start))
check "intake of an empty spool waits and prints nothing" test ! -s "$T/in2"
check "intake waits about the given seconds" test "$took" -ge 2 -a "$took" -le 5
printf '%s\n' '{"src":"/etc/passwd"}' | sh "$S/ack.sh" > /dev/null 2>&1; rc=$?
check "ack refuses a path outside the spool" test "$rc" -ne 0

echo "--- scrub"
printf 'k AKIAABCDEFGHIJKLMNOP\nAuthorization: Bearer abcdef123456789\napi_key=sk-abcdefghijklmnopqrstuvwx\nurl postgres://app:s3cret@db:5432/x\ntoken: "abc.def.ghi"\nplain text and BK-123456 stay\n' | sh "$S/scrub.sh" > "$T/sc"
check "scrub removes the aws key" hasnt AKIAABC "$T/sc"
check "scrub removes the bearer value" hasnt abcdef123456789 "$T/sc"
check "scrub removes the sk key" hasnt sk-abcdef "$T/sc"
check "scrub removes the url password" hasnt s3cret "$T/sc"
check "scrub removes a quoted token" hasnt abc.def.ghi "$T/sc"
check "scrub keeps plain text" has "plain text and BK-123456 stay" "$T/sc"

echo "--- diagnose"
printf '{"cpu":0.9}' > "$T/resp/_services_checkout_metrics"
printf '[{"version":"v413"}]' > "$T/resp/_services_checkout_deploys"
printf 'line one password=hunter2\nline two\n' > "$T/resp/_services_checkout_logs"
reset; printf '{"service":"checkout"}' | sh "$S/diagnose.sh" > "$T/d1" 2>&1; rc=$?
check "diagnose exits 0" test "$rc" -eq 0
check "diagnose prints the metrics" has '"cpu"' "$T/d1"
check "diagnose scrubs the logs" hasnt hunter2 "$T/d1"
check "diagnose makes no write call" hasnt -- '-X' "$FAKE_LOG"
check "diagnose sends the credential as a header file" has '-H @' "$FAKE_LOG"
check "the credential is not in an argument" hasnt TESTONLY "$FAKE_LOG"
: > "$T/resp/_services_checkout_metrics.fail"
printf '{"service":"checkout"}' | sh "$S/diagnose.sh" > "$T/d2" 2>&1; rc=$?
check "diagnose exits nonzero when the metrics fail" test "$rc" -ne 0
rm -f "$T/resp/_services_checkout_metrics.fail"
printf '{"service":"../etc"}' | sh "$S/diagnose.sh" > /dev/null 2>&1; rc=$?
check "diagnose refuses an unsafe service name" test "$rc" -ne 0

echo "--- page"
reset; printf '%s' '{"kind":"rollback"}' | sh "$S/page.sh" INC-1 primary "approve?" > "$T/p1" 2>&1; rc=$?
check "page exits 0" test "$rc" -eq 0
check "page prints nothing" test ! -s "$T/p1"
check "page sends an idempotency key with the digest" grep -Eq 'Idempotency-Key: page-INC-1-primary-[0-9a-f]{16}' "$FAKE_LOG"
check "page body carries the proposal" has rollback "$FAKE_BODY"
: > "$T/resp/_incidents_INC-1_page.fail"
printf 'x' | sh "$S/page.sh" INC-1 primary t > /dev/null 2>&1; rc=$?
check "page fails when the API fails" test "$rc" -ne 0
rm -f "$T/resp/_incidents_INC-1_page.fail"
printf 'x' | sh "$S/page.sh" 'a b' primary t > /dev/null 2>&1; rc=$?
check "page refuses an unsafe id" test "$rc" -ne 0

echo "--- snapshot, mitigate, rollback"
printf '{"version":"v413","replicas":3}' > "$T/resp/_services_checkout_state"
sh "$S/snapshot.sh" INC-1 checkout > /dev/null 2>&1; check "snapshot saves the state" jq -e '.version == "v413"' "$T/home/incidents/INC-1/before.json"
printf '{}' > "$T/resp/_services_checkout_state"
sh "$S/snapshot.sh" INC-2 checkout > /dev/null 2>&1; rc=$?
check "snapshot refuses an empty state" test "$rc" -ne 0
printf '{"state":"applied"}' > "$T/resp/_mitigations_INC-1-rollback"
reset; printf '{"service":"checkout","to_version":"v412"}' | sh "$S/mitigate.sh" rollback INC-1-rollback > "$T/m1" 2>&1; rc=$?
check "mitigate exits 0" test "$rc" -eq 0
check "mitigate sends the idempotency key" has 'Idempotency-Key: INC-1-rollback' "$FAKE_LOG"
check "mitigate sends the args" has to_version "$FAKE_BODY"
check "mitigate reads the write back" has 'GET\|-G' "$FAKE_LOG"
printf '{"state":"failed"}' > "$T/resp/_mitigations_INC-1-rollback"
printf '{"service":"checkout"}' | sh "$S/mitigate.sh" rollback INC-1-rollback > /dev/null 2>&1; rc=$?
check "mitigate fails when the read-back is not applied" test "$rc" -ne 0
printf '{}' | sh "$S/mitigate.sh" drop_database INC-1-x > /dev/null 2>&1; rc=$?
check "mitigate refuses a kind outside the list" test "$rc" -ne 0
printf 'not json' | sh "$S/mitigate.sh" restart INC-1-x > /dev/null 2>&1; rc=$?
check "mitigate refuses args that are not an object" test "$rc" -ne 0
reset; sh "$S/rollback.sh" INC-1 checkout INC-1-rollback > /dev/null 2>&1; rc=$?
check "rollback exits 0" test "$rc" -eq 0
check "rollback uses its own key" has 'Idempotency-Key: rollback-INC-1-rollback' "$FAKE_LOG"
check "rollback sends the saved state file" has 'before.json' "$FAKE_LOG"
sh "$S/rollback.sh" INC-9 checkout k > /dev/null 2>&1; rc=$?
check "rollback refuses with no saved state" test "$rc" -ne 0

echo "--- verify"
printf '{"healthy":true}' > "$T/resp/_services_checkout_health"
sh "$S/verify.sh" checkout 1 > "$T/v1" 2>&1; rc=$?
check "verify passes on two healthy reads" test "$rc" -eq 0
printf '{"healthy":false}' > "$T/resp/_services_checkout_health"
sh "$S/verify.sh" checkout 1 > "$T/v2" 2>&1; rc=$?
check "verify fails at the deadline" test "$rc" -eq 1
printf '{"healthy":true}\n{"healthy":false}\n{"healthy":true}\n{"healthy":true}\n' > "$T/resp/_services_checkout_health.seq"
rm -f "$T/resp/_services_checkout_health"
INC_MINUTE=3 sh "$S/verify.sh" checkout 2 > "$T/v3" 2>&1; rc=$?
check "a flap resets the count" test "$rc" -eq 0

echo "--- status and timeline"
reset; printf 'text' | sh "$S/status.sh" INC-1 mitigated; rc=$?
check "status posts" test "$rc" -eq 0
check "status sends a key" has 'Idempotency-Key: status-INC-1-mitigated' "$FAKE_LOG"
printf 'x' | sh "$S/status.sh" INC-1 exploded > /dev/null 2>&1; rc=$?
check "status refuses an unknown state" test "$rc" -ne 0
sh "$S/timeline.sh" add INC-1 "opened: token=abc12345 line
two	tab"
sh "$S/timeline.sh" add INC-1 second
sh "$S/timeline.sh" show INC-1 > "$T/t1"
check "timeline has two lines" test "$(wc -l < "$T/t1" | tr -d ' ')" -eq 2
check "timeline scrubs a secret" hasnt abc12345 "$T/t1"
check "timeline joins a multi-line note" has 'line two tab' "$T/t1"
sh "$S/timeline.sh" show INC-7 > "$T/t2"; check "timeline of an unknown id says so" has 'no entries' "$T/t2"

echo "--- postmortem-save"
printf '# Impact\nx\n' | sh "$S/postmortem-save.sh" INC-1 > "$T/pm1" 2>&1; rc=$?
check "postmortem-save exits 0" test "$rc" -eq 0
check "postmortem-save prints the path" has "postmortems/INC-1.md" "$T/pm1"
check "the draft is in the incident home" has '# Impact' "$T/home/postmortems/INC-1.md"
printf '' | sh "$S/postmortem-save.sh" INC-2 > /dev/null 2>&1; rc=$?
check "postmortem-save refuses an empty draft" test "$rc" -ne 0
printf 'x' | sh "$S/postmortem-save.sh" '../x' > /dev/null 2>&1; rc=$?
check "postmortem-save refuses an unsafe id" test "$rc" -ne 0

echo "--- result"
echo "passed $pass, failed $fail. Test folder: $T"
[ "$fail" -eq 0 ]
