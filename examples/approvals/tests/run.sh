#!/bin/sh
# Tests the desk scripts against a desk folder made by mktemp -d under the temp folder. A stand-in curl is early on PATH.
# It uses the real jq, git, sqlite3, flock and ssh-keygen. The folder is deleted at the end.
# Usage: sh tests/run.sh   (from any folder).
set -u
HERE=$(cd "$(dirname "$0")" && pwd)
S="$HERE/../scripts"
T=$(mktemp -d "${TMPDIR:-/tmp}/approvals-test.XXXXXX")
case "$T" in /*/approvals-test.??????) ;; *) echo "bad test folder: $T"; exit 2 ;; esac
mkdir -p "$T/bin" "$T/keys" "$T/home"
pass=0; fail=0
ok() { pass=$((pass + 1)); printf 'ok   %s\n' "$1"; }
bad() { fail=$((fail + 1)); printf 'FAIL %s\n' "$1"; }
check() { # name command...
    n="$1"; shift
    if "$@" > /dev/null 2>&1; then ok "$n"; else bad "$n"; fi
}
has() { grep -q -e "$1" "$2"; }
rc() { "$@" > "$T/out" 2> "$T/err"; echo $?; }

printf '#!/bin/sh\nprintf "%%s\\n" "$*" >> "%s/curl.log"\n[ -e "%s/fail" ] && exit 22\nexit 0\n' "$T" "$T" > "$T/bin/curl"
chmod +x "$T/bin/curl"
printf 'NOTIFY_API=http://notify.test\n' > "$T/home/config"
printf 'Authorization: Bearer TESTONLY\n' > "$T/home/credential.header"
: > "$T/home/allowed_signers"
for p in ann lee lou sam pat mallory; do
    ssh-keygen -q -t ed25519 -N '' -C "$p" -f "$T/keys/$p"
    [ "$p" = mallory ] || printf '%s namespaces="obee-signoff" %s\n' "$p" "$(cat "$T/keys/$p.pub")" >> "$T/home/allowed_signers"
done
export PATH="$T/bin:$PATH" AP_HOME="$T/home" APPROVAL_NOW=1000
POLICY='{"window":3600,"extend":600,"remind_at":[50],"min_roles":2,"escalate_to":["rita"],"roles":{"security":{"members":["sam"],"need":1,"required":true},"product":{"members":["pat"],"need":1,"required":true},"legal":{"members":["lee","lou"],"need":2,"required":false}}}'
signoff() { # signer name role decision [hash]
    sh "$S/sign.sh" "$T/keys/$1" "$T/home/inbox" "$2" type=signoff req=r1 doc=msa hash="${5:-$H}" role="$3" by="$1" decision="$4" reason=ok
}
tally() { NO_INTAKE= sh "$S/tally.sh" r1; }

echo "--- submit and open"
printf 'The supplier delivers in 30 days.\n' > "$T/v1.md"
printf 'The supplier delivers in 45 days.\n' > "$T/v2.md"
H=$(sh "$S/submit.sh" msa "$T/v1.md" ann)
check "submit prints a 40 digit blob hash" test "$(printf '%s' "$H" | grep -Ec '^[0-9a-f]{40}$')" -eq 1
check "a second submit of the same text prints the same hash" test "$(sh "$S/submit.sh" msa "$T/v1.md" ann)" = "$H"
check "the same text adds one version row" test "$(sqlite3 "$T/home/ledger.db" "SELECT count(*) FROM events WHERE type='version';")" -eq 1
sh "$S/submit.sh" rev "$T/v1.md" ann > /dev/null
sh "$S/submit.sh" rev "$T/v2.md" ann > /dev/null
REVH=$(sh "$S/submit.sh" rev "$T/v1.md" ann)
check "v1, v2, v1 records three versions" test "$(sqlite3 "$T/home/ledger.db" "SELECT count(*) FROM events WHERE type='version' AND doc='rev';")" -eq 3
check "after v1, v2, v1 the head blob is v1" test "$(git -C "$T/home/docs" rev-parse HEAD:rev.md)" = "$REVH"
check "submit refuses an unsafe name" test "$(rc sh "$S/submit.sh" ../x "$T/v1.md" ann)" -ne 0
printf '%s' "$POLICY" | sh "$S/open.sh" r1 msa release ann > "$T/open1"
check "open prints the deadline" has '"deadline":4600' "$T/open1"
check "a second open prints the first answer" test "$(printf '%s' "$POLICY" | sh "$S/open.sh" r1 msa release ann)" = "$(cat "$T/open1")"
check "open refuses a window under 60 seconds" test "$(printf '%s' "$POLICY" | sed 's/3600/59/' | rc sh "$S/open.sh" r2 msa release ann)" -ne 0

echo "--- intake"
signoff pat p1 product approve
signoff mallory p2 product approve
signoff lee p3 legal approve
sh "$S/intake.sh" > "$T/in1"
check "a signed sign-off is taken" has 'signoff r1 pat approve' "$T/in1"
check "a key that the desk does not know is refused" has 'refused p2.msg the signature does not verify for mallory' "$T/in1"
check "the refused message is kept in refused/" test "$(ls "$T/home/refused" | grep -c 'p2.msg$')" -eq 1
tally > "$T/t1"
signoff lou p4 legal approve
cp "$T/home/inbox/p4.msg" "$T/p4.copy"; cp "$T/home/inbox/p4.msg.sig" "$T/p4.copy.sig"
printf 'tampered=1\n' >> "$T/home/inbox/p4.msg"
sh "$S/intake.sh" > "$T/in2"
check "a message changed after signing is refused" has 'refused p4.msg the signature does not verify' "$T/in2"
cp "$T/p4.copy" "$T/home/inbox/p5.msg"; cp "$T/p4.copy.sig" "$T/home/inbox/p5.msg.sig"
sh "$S/intake.sh" > "$T/in3"
check "a signed message is taken once" has 'signoff r1 lou approve' "$T/in3"
cp "$T/p4.copy" "$T/home/inbox/p6.msg"; cp "$T/p4.copy.sig" "$T/home/inbox/p6.msg.sig"
sh "$S/intake.sh" > "$T/in4"
check "a replay is refused" has 'the message was taken before' "$T/in4"
signoff pat p7 product approve 0000000000000000000000000000000000000000
sh "$S/intake.sh" > "$T/in5"
check "a hash that is not a version is refused" has 'the hash is not a version of msa' "$T/in5"

echo "--- tally: quorum, veto, min_roles"
check "product is satisfied" test "$(jq -r '.roles.product.ok' "$T/t1")" = true
check "one of two legal members is not enough" test "$(jq -r '.roles.legal.ok' "$T/t1")" = false
check "security is missing, so the request is pending" test "$(jq -r .status "$T/t1")" = pending
signoff sam s1 security approve
tally > "$T/t2"
check "two required roles satisfy min_roles 2 with legal short" test "$(jq -r .status "$T/t2")" = approved
signoff sam s2 security reject
tally > "$T/t3"
check "the last decision of a person wins, and a required reject is a veto" test "$(jq -r .status "$T/t3")" = rejected
check "the rejection names the person" test "$(jq -r '.rejections[0].who' "$T/t3")" = sam

echo "--- tally: change resets"
tally > "$T/t4"
cp "$T/v2.md" "$T/home/docs/msa.md"
tally > "$T/t4b"
check "a document file ahead of the ledger is not the current version" test "$(jq -r .hash "$T/t4b")" = "$H"
cp "$T/v1.md" "$T/home/docs/msa.md"
H2=$(sh "$S/submit.sh" msa "$T/v2.md" ann)
tally > "$T/t5"
echo "--- await: a change after the tally is not missed"
export APPROVAL_POLL=1
seen() { jq -r '"\(.seq) \(.hash)"' "$1"; }
check "a change between the tally and the await ends the wait at once" test "$(rc sh "$S/await.sh" r1 30 $(seen "$T/t4"))" -eq 0
has changed "$T/out" && ok "the early wait prints changed" || bad "the early wait prints changed"
sh "$S/notify.sh" r1 await-note security < /dev/null > /dev/null 2>&1
check "a notice row leaves the seq of the tally alone" test "$(NO_INTAKE=1 sh "$S/tally.sh" r1 | jq -r .seq)" = "$(jq -r .seq "$T/t5")"
check "the tally and the wait count the same rows as changes" test "$(rc sh "$S/await.sh" r1 1 $(NO_INTAKE=1 sh "$S/tally.sh" r1 | jq -r '"\(.seq) \(.hash)"'))" -eq 3
check "a notice row of the run does not end the wait" test "$(rc sh "$S/await.sh" r1 1 $(seen "$T/t5"))" -eq 3
check "no change since the tally keeps the wait until the time passes" test "$(rc sh "$S/await.sh" r1 1 $(seen "$T/t5"))" -eq 3
unset APPROVAL_POLL
check "a changed document makes the sign-offs stale" test "$(jq -r '.stale | length' "$T/t5")" -ge 1
check "no role is satisfied on the new version" test "$(jq -r .satisfied "$T/t5")" -eq 0
check "the ledger keeps the old sign-offs" test "$(sqlite3 "$T/home/ledger.db" "SELECT count(*) FROM events WHERE type='signoff';")" -ge 3

echo "--- tally: one snapshot"
# The stand-in sqlite3 submits a version of another document right after the first read of the events. A tally that takes its seq, hash and
# rows from one read answers as of that read. A tally that reads the seq apart from the rows shows a seq that the rows do not hold.
REAL=$(command -v sqlite3)
printf '#!/bin/sh\nif [ -e "%s/arm" ] && [ ! -e "%s/fired" ]; then\ncase "$*" in *"FROM events"*)\n: > "%s/fired"\n"%s" "$@"; r=$?\nsh "%s/submit.sh" snap "%s/v2.md" ann > /dev/null\nexit $r ;;\nesac\nfi\nexec "%s" "$@"\n' "$T" "$T" "$T" "$REAL" "$S" "$T" "$REAL" > "$T/bin/sqlite3"
chmod +x "$T/bin/sqlite3"
NO_INTAKE=1 sh "$S/tally.sh" r1 > "$T/s0"
: > "$T/arm"
NO_INTAKE=1 sh "$S/tally.sh" r1 > "$T/s1"
check "the stand-in submitted during the tally" test -e "$T/fired"
check "a tally prints a numeric seq" test "$(jq -r '.seq | type' "$T/s1")" = number
check "the seq of a tally is the seq of the rows it read" test "$(jq -r .seq "$T/s1")" = "$(jq -r .seq "$T/s0")"
check "the next tally sees the version that landed" test "$(NO_INTAKE=1 sh "$S/tally.sh" r1 | jq -r .seq)" -gt "$(jq -r .seq "$T/s0")"

echo "--- tally: deadline"
check "the deadline is the opening time plus the window" test "$(jq -r .deadline "$T/t5")" -eq 4600
APPROVAL_NOW=5000 tally > "$T/t6"
check "after the deadline the request is expired" test "$(jq -r .status "$T/t6")" = expired

echo "--- delegation"
printf '%s' "$POLICY" | sh "$S/open.sh" r3 msa release ann > /dev/null
sh "$S/sign.sh" "$T/keys/sam" "$T/home/inbox" d1 type=delegate role=security by=sam to=lou until=9000 reason=away
sh "$S/sign.sh" "$T/keys/mallory" "$T/home/inbox" d2 type=delegate role=security by=sam to=mallory until=9000 reason=forged
sh "$S/sign.sh" "$T/keys/lee" "$T/home/inbox" d3 type=delegate role=security by=lee to=lou until=9000 reason=not-a-member
sh "$S/sign.sh" "$T/keys/lou" "$T/home/inbox" g1 type=signoff req=r3 doc=msa hash="$H2" role=security by=lou decision=approve reason=ok
sh "$S/intake.sh" > "$T/in6"
check "a delegation signed by the member is taken" has 'delegate sam lou security' "$T/in6"
check "a delegation with a key that is not the member's is refused" has 'refused d2.msg' "$T/in6"
sh "$S/tally.sh" r3 > "$T/t4"
check "a delegate counts for the role while the delegation is in force" test "$(jq -r '.roles.security.approved[0]' "$T/t4")" = lou
check "a delegation by a non-member does not count" test "$(jq -r '.roles.security.people | length' "$T/t4")" -eq 2

echo "--- extend"
check "extend adds time from now" test "$(APPROVAL_NOW=5000 sh "$S/extend.sh" r1 600 rm "more time")" -eq 5600
check "a second extend is refused with exit 3" test "$(rc sh "$S/extend.sh" r1 600 rm again)" -eq 3
APPROVAL_NOW=5100 tally > "$T/t7"
check "the request is open again inside the extension" test "$(jq -r .status "$T/t7")" = pending

echo "--- notify"
printf 'Please sign.' | sh "$S/notify.sh" r1 k1 security > "$T/n1" 2>&1
check "notify sends once" has 'sent k1' "$T/n1"
check "notify sends the idempotency key" has 'Idempotency-Key: k1' "$T/curl.log"
printf 'Please sign.' | sh "$S/notify.sh" r1 k1 security > "$T/n2" 2>&1
check "a second call with the same key sends nothing" has 'already sent k1' "$T/n2"
check "the notifier saw one message" test "$(grep -c 'Idempotency-Key: k1' "$T/curl.log")" -eq 1
: > "$T/fail"
check "notify fails when the notifier fails" test "$(printf x | rc sh "$S/notify.sh" r1 k2 security)" -ne 0
check "a failed notice leaves a ledger row" test "$(sqlite3 "$T/home/ledger.db" "SELECT count(*) FROM events WHERE type='notify-failed';")" -eq 1
check "a failed key is not marked as sent" test "$(printf x | rc sh "$S/notify.sh" r1 k2 security)" -ne 0
rm -f "$T/fail"

echo "--- finalize and audit"
check "finalize refuses an outcome that is not the state" test "$(rc sh "$S/finalize.sh" r1 approved)" -eq 4
signoff sam s4 security approve "$H2"
signoff pat s5 product approve "$H2"
# 500 rows for everybody with 250 characters each, in the chain. The rows that finalize reads are then over 128 KB, the limit of one
# argument of a program.
printf '. "%s/lib.sh"\nd=$(cat)\nacquire\ni=0\nwhile [ $i -lt 500 ]; do append_raw refused - - - - - - "$d"; i=$((i + 1)); done\nrelease\n' "$S" > "$T/bigrow.sh"
awk 'BEGIN { for (i = 0; i < 25; i++) printf "xxxxxxxxxx" }' | sh "$T/bigrow.sh"
sh "$S/finalize.sh" r1 approved > "$T/f1" 2>&1; frc=$?
check "finalize closes when the state is approved" test "$frc" -eq 0
check "finalize writes a certificate over 128 KB" test "$(wc -c < "$T/home/certs/r1.json")" -gt 131072
HEAD=$(sed -n 's/^head //p' "$T/f1")
check "finalize prints the head" test -n "$HEAD"
sh "$S/audit.sh" r1 "$H2" "$HEAD" > "$T/a1" 2>&1; arc=$?
check "audit passes with the hash and the head" test "$arc" -eq 0
check "audit prints the chain result" has 'chain ok' "$T/a1"
check "audit fails for a hash that was not approved" test "$(rc sh "$S/audit.sh" r1 "$H" "$HEAD")" -eq 1
sqlite3 "$T/home/ledger.db" "DROP TRIGGER events_no_delete; DELETE FROM events WHERE seq >= (SELECT seq FROM events WHERE type='closed');"
check "a call without the head is refused" test "$(rc sh "$S/audit.sh" r1 "$H2")" -eq 2
check "a call with the hash only does not print chain ok" test "$(grep -c 'chain ok' "$T/out")" -eq 0
check "a cut tail fails with the head, with the hash check skipped" test "$(rc sh "$S/audit.sh" r1 - "$HEAD")" -eq 1
check "audit names the lost row" has 'BROKEN the row' "$T/out"
check "audit names no other problem for the cut" test "$(grep -c 'does not match\|does not follow' "$T/out")" -eq 0
sqlite3 "$T/home/ledger.db" "DROP TRIGGER events_no_update; UPDATE events SET who='mallory' WHERE type='signoff' AND who='pat';"
check "a changed row breaks the chain" test "$(rc sh "$S/audit.sh" r1 - "$HEAD")" -eq 1
check "audit names the row" has 'does not match its hash' "$T/out"

echo "--- lock"
i=0
while [ "$i" -lt 12 ]; do
    ( sh "$S/notify.sh" r1 "c$i" security < /dev/null > /dev/null 2>&1 ) &
    i=$((i + 1))
done
wait
total=$(sqlite3 "$T/home/ledger.db" "SELECT count(*) FROM events;")
distinct=$(sqlite3 "$T/home/ledger.db" "SELECT count(DISTINCT prev) FROM events;")
check "concurrent appends write every row once" test "$total" -eq "$distinct"
LAST=$(sqlite3 "$T/home/ledger.db" "SELECT seq || ':' || row_hash FROM events ORDER BY seq DESC LIMIT 1;")
sh "$S/audit.sh" r1 - "$LAST" > "$T/out" 2>&1
check "the chain holds after concurrent appends" test "$(grep -c 'does not follow' "$T/out")" -eq 0

echo "--- large ledger"
# A row for everybody with 200000 characters. The snapshot is far over 128 KB, the limit of one argument of a program.
sqlite3 "$T/home/ledger.db" "INSERT INTO events(at,type,req,doc,hash,who,role,decision,detail,prev,row_hash) VALUES (1,'refused','-','-','-','-','-','-',replace(hex(zeroblob(100000)),'0','x'),'p','h');"
check "a tally of a ledger over 128 KB succeeds" test "$(rc sh "$S/tally.sh" r1)" -eq 0
check "the tally of a large ledger still prints the seq" test "$(jq -r '.seq | type' "$T/out")" = number

rm -rf "$T"
printf '\n%s passed, %s failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
