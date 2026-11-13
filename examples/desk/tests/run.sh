#!/bin/sh
# Tests the desk scripts against a test desk folder made by mktemp -d under the temporary folder. Usage: sh tests/run.sh. Exit 0 when all pass.
# Stand-ins for curl and sendmail are early on PATH. They log their arguments and read stdin only when the call sends some.
set -u
HERE=$(cd "$(dirname "$0")" && pwd)
DESK="$HERE/.."
GATEWAY="$DESK/gateway"
EVALS="$DESK/evals"
T=$(mktemp -d "${TMPDIR:-/tmp}/desk-test.XXXXXX")
H="$T/home"
mkdir -p "$T/bin" "$T/resp" "$H/mail/new" "$H/mail/cur" "$H/chat" "$H/gold"
pass=0
fail=0

ok() { pass=$((pass + 1)); printf 'ok   %s\n' "$1"; }
bad() { fail=$((fail + 1)); printf 'FAIL %s\n' "$1"; }
check() {
    name="$1"
    shift
    if "$@" > /dev/null 2>&1; then ok "$name"; else bad "$name"; fi
}
has() { grep -q -e "$1" "$2"; }
hasnt() { ! grep -q -e "$1" "$2"; }
reset() { : > "$FAKE_LOG"; : > "$FAKE_BODY"; : > "$FAKE_MAIL"; }

make_failgrep() {
    mkdir -p "$T/failgrep"
    cat > "$T/failgrep/grep" <<'EOF'
#!/bin/sh
# Fails only the card scan pattern; every other grep call runs the real grep.
real=/usr/bin/grep
[ -x "$real" ] || real=/bin/grep
case "$*" in
*'[0-9]{4}([ -]?[0-9]{4}){3}'*)
    printf 'grep: simulated failure of the mask card scan\n' >&2
    exit 2
    ;;
esac
exec "$real" "$@"
EOF
    chmod 755 "$T/failgrep/grep"
}

make_curl() {
    cat > "$T/bin/curl" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >> "$FAKE_LOG"
url=""
sends=0
for arg in "$@"; do
    case "$arg" in http*) url="$arg" ;; @-) sends=1 ;; esac
done
if [ "$sends" -eq 1 ]; then cat >> "$FAKE_BODY"; printf '\n' >> "$FAKE_BODY"; fi
key=$(printf '%s' "$url" | sed -e 's#^[a-z]*://[^/]*##' -e 's#?.*##' -e 's#/#_#g')
[ -e "$FAKE_RESP/$key.fail" ] && { echo "boom" >&2; exit 22; }
[ -e "$FAKE_RESP/$key" ] && cat "$FAKE_RESP/$key"
exit 0
EOF
    cat > "$T/bin/sendmail" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >> "$FAKE_LOG"
cat >> "$FAKE_MAIL"
EOF
    chmod +x "$T/bin/curl" "$T/bin/sendmail"
}

make_desk() {
    printf 'DESK_API=http://desk.test\nMAILDIR="%s"\nCHAT_SPOOL="%s"\nCHAT_API=http://chat.test\nMASK_SALT=testsalt\n' "$H/mail" "$H/chat" > "$H/config"
    printf 'Authorization: Bearer TESTONLY\n' > "$H/credential.header"
    printf 'Message-Id: <m1@mail.test>\nFrom: Ann Smith <ann@example.com>\nSubject: Refund BK-123456\n\nThanks, Ann Smith. My card 4111 1111 1111 1111 and phone +1 555 123-4567. Travel 2026-12-01.\n' > "$H/mail/new/m1"
    printf '{"id":"c1","user":"bob","text":"Please check BK-654321 for me"}' > "$H/chat/c1.json"
    printf '{"ref":"BK-123456","status":"cancelled","paid":120,"currency":"EUR","date":"2026-12-01","customer_name":"Ann Smith","customer_email":"ann@example.com","refunded":0}' > "$T/resp/_bookings_BK-123456"
}

make_gold() {
    printf 'id\theld\tka\taa\tkb\tab\tfile\ng1\tno\trefund\t80\trefund\t80\tg1.txt\ng2\tno\tnone\t0\tnone\t0\tg2.txt\ng3\tyes\trefund\t50\trefund\t50\tg3.txt\n' > "$H/gold/refund.tsv"
    printf 'case one\n' > "$H/gold/g1.txt"
    printf 'case two\n' > "$H/gold/g2.txt"
    printf 'case three\n' > "$H/gold/g3.txt"
    printf 'none\tperson\t2\n' > "$H/gold/costs.tsv"
    printf 'id\tcase\treply\tga\tgb\nr1\tg1.txt\tr1.txt\tgood\tgood\nr2\tg2.txt\tr2.txt\tgood\tgood\nr3\tg3.txt\tr3.txt\tpoor\tpoor\nr4\tg1.txt\tr4.txt\tpoor\tpoor\n' > "$H/gold/refund-replies.tsv"
    printf 'reply one\n' > "$H/gold/r1.txt"
    printf 'reply two\n' > "$H/gold/r2.txt"
    printf 'reply three\n' > "$H/gold/r3.txt"
    printf 'reply four\n' > "$H/gold/r4.txt"
}

make_curl
make_desk
make_gold
export PATH="$T/bin:$PATH" DESK_HOME="$H" FAKE_LOG="$T/curl.log" FAKE_BODY="$T/curl.body" FAKE_MAIL="$T/mail.out" FAKE_RESP="$T/resp"
reset

echo "--- channels"
sh "$GATEWAY/channels/email.sh" > "$T/email.out" 2>&1
check "email adapter prints the message key" jq -e '.key == "mail-m1@mail.test" and .channel == "email"' "$T/email.out"
sh "$GATEWAY/channels/chat.sh" > "$T/chat.out" 2>&1
check "chat adapter prints the message key" jq -e '.key == "chat-c1" and .channel == "chat"' "$T/chat.out"

echo "--- mask and intake"
sh "$GATEWAY/intake.sh" > "$T/intake.out" 2> "$T/intake.err"
check "intake prints both messages" test "$(wc -l < "$T/intake.out" | tr -d ' ')" -eq 2
check "intake masks the email address" hasnt 'ann@example.com' "$T/intake.out"
check "intake masks the card number" hasnt '4111' "$T/intake.out"
check "intake masks the phone number" hasnt '555 123' "$T/intake.out"
check "intake masks the sender name" hasnt 'Ann Smith' "$T/intake.out"
check "intake keeps the booking reference" has 'BK-123456' "$T/intake.out"
check "intake keeps the travel date" has '2026-12-01' "$T/intake.out"
check "intake uses tokens" has 'M_EMAIL_' "$T/intake.out"
check "the vault holds the email address" has 'ann@example.com' "$H/vault.tsv"
token=$(printf 'ann@example.com' | sh "$GATEWAY/mask.sh" text)
check "the same value gets the same token" has "$token" "$T/intake.out"
check "the mask refuses a bad mode" sh -c "! sh '$GATEWAY/mask.sh' nope < /dev/null"

echo "--- mask fails closed"
make_failgrep
printf 'My card 4111 1111 1111 1111.' | PATH="$T/failgrep:$PATH" sh "$GATEWAY/mask.sh" text > "$T/fail.out" 2> "$T/fail.err"
rc=$?
check "a failed card scan stops the mask" test "$rc" -ne 0
check "a failed card scan prints no card number" hasnt '4111' "$T/fail.out"
check "a failed card scan names the failure on stderr" has 'the CARD scan failed' "$T/fail.err"

echo "--- unmask"
printf 'Hello %s.' "$token" | sh "$GATEWAY/unmask.sh" > "$T/unmask.out"
check "unmask restores the value" has 'Hello ann@example.com.' "$T/unmask.out"
printf 'Hello M_EMAIL_0000000000.' | sh "$GATEWAY/unmask.sh" > "$T/unmask2.out"
check "unmask keeps an unknown token" has 'M_EMAIL_0000000000' "$T/unmask2.out"

echo "--- ack"
sh "$GATEWAY/ack.sh" < "$T/intake.out"
check "ack moves the email to cur" test -f "$H/mail/cur/m1"
check "ack empties new" test ! -e "$H/mail/new/m1"
check "ack moves the chat file to done" test -f "$H/chat/done/c1.json"

echo "--- read-case"
sh "$GATEWAY/read-case.sh" > "$T/facts.out" 2>&1 <<EOF
{"subject":"Refund BK-123456","text":"please"}
EOF
check "read-case prints the booking" has 'Booking BK-123456: cancelled' "$T/facts.out"
check "read-case masks the customer name" hasnt 'Ann Smith' "$T/facts.out"
check "read-case masks the customer email" hasnt 'ann@example.com' "$T/facts.out"
check "read-case reads with GET only" hasnt -- '-X' "$FAKE_LOG"
printf '{"subject":"hi","text":"no reference"}' | sh "$GATEWAY/read-case.sh" > "$T/facts2.out" 2>&1
check "read-case says when there is no reference" has 'No booking reference' "$T/facts2.out"

echo "--- eligible"
printf '{"eligible":true}' > "$T/resp/_eligibility_refund"
printf '{"booking_id":"BK-123456"}' | sh "$GATEWAY/eligible.sh" refund > "$T/el1.out" 2>&1
check "eligible exits 0 when eligible" test "$?" -eq 0
printf '{"eligible":false,"reason":"Past the deadline."}' > "$T/resp/_eligibility_refund"
printf '{"booking_id":"BK-123456"}' | sh "$GATEWAY/eligible.sh" refund > "$T/el2.out" 2>&1
rc=$?
check "eligible exits 1 when not eligible" test "$rc" -eq 1
check "eligible prints the reason" has 'Past the deadline.' "$T/el2.out"
: > "$T/resp/_eligibility_refund.fail"
printf '{}' | sh "$GATEWAY/eligible.sh" refund > /dev/null 2>&1
rc=$?
check "eligible exits 2 when the check fails" test "$rc" -eq 2

echo "--- write"
reset
PAYLOAD=$(jq -n -c --arg to "$token" '{args: {booking_id: "BK-123456", amount: 80, reason: $to}, reply: "Refund done.", channel: "email", to: $to}')
printf '%s' "$PAYLOAD" | BEEHAVE_WRITE_KEY=key1 sh "$GATEWAY/write.sh" credit > /dev/null 2>&1
check "credit posts to the credit route" has 'writes/credit' "$FAKE_LOG"
check "credit sends the Idempotency-Key header" has 'Idempotency-Key: credit-key1' "$FAKE_LOG"
check "credit unmasks the arguments" has 'ann@example.com' "$FAKE_BODY"
printf '%s' "$PAYLOAD" | BEEHAVE_WRITE_KEY=key1 sh "$GATEWAY/write.sh" bogus > /dev/null 2>&1
rc=$?
check "write refuses an unknown step" test "$rc" -eq 2
printf '%s' "$PAYLOAD" | sh "$GATEWAY/write.sh" credit > /dev/null 2>&1
rc=$?
check "write refuses a call with no engine key" test "$rc" -eq 2

echo "--- reply and read-back"
reset
printf '%s' "$PAYLOAD" | BEEHAVE_WRITE_KEY=key1 sh "$GATEWAY/write.sh" reply > /dev/null 2>&1
check "reply sends one mail" test "$(grep -c '^To: ' "$FAKE_MAIL")" -eq 1
check "reply unmasks the recipient" has 'To: ann@example.com' "$FAKE_MAIL"
check "reply logs its key" has 'key1' "$H/sent.tsv"
printf '%s' "$PAYLOAD" | BEEHAVE_WRITE_KEY=key1 sh "$GATEWAY/write.sh" reply > /dev/null 2>&1
check "reply does not send a second time" test "$(grep -c '^To: ' "$FAKE_MAIL")" -eq 1
chat_payload=$(jq -n -c '{args: {}, reply: "Done.", channel: "chat", to: "bob"}')
printf '%s' "$chat_payload" | BEEHAVE_WRITE_KEY=key2 sh "$GATEWAY/write.sh" reply > /dev/null 2>&1
check "chat reply posts to the chat API" has 'chat.test/send' "$FAKE_LOG"
check "chat reply sends the text" has '"text":"Done."' "$FAKE_BODY"
BEEHAVE_WRITE_KEY=key1 sh "$GATEWAY/read-back.sh" reply > /dev/null 2>&1
check "read-back finds a sent reply" test "$?" -eq 0
BEEHAVE_WRITE_KEY=missing sh "$GATEWAY/read-back.sh" reply > /dev/null 2>&1
rc=$?
check "read-back fails for an unsent reply" test "$rc" -ne 0
printf '{"status":"applied"}' > "$T/resp/_writes_credit-key1"
BEEHAVE_WRITE_KEY=key1 sh "$GATEWAY/read-back.sh" credit > /dev/null 2>&1
check "read-back accepts an applied write" test "$?" -eq 0
printf '{"status":"pending"}' > "$T/resp/_writes_credit-key1"
BEEHAVE_WRITE_KEY=key1 sh "$GATEWAY/read-back.sh" credit > /dev/null 2>&1
rc=$?
check "read-back fails for a pending write" test "$rc" -ne 0

echo "--- gold and calib"
sh "$EVALS/gold.sh" refund no > "$T/gold.out" 2>&1
check "gold prints the practice cases" test "$(wc -l < "$T/gold.out" | tr -d ' ')" -eq 2
check "gold holds back the exam case" hasnt 'case three' "$T/gold.out"
check "gold prints only id and case" jq -e 'keys == ["case", "id"]' "$T/gold.out"
sh "$EVALS/gold.sh" refund yes > "$T/gold2.out" 2>&1
check "gold prints the exam case for yes" has 'case three' "$T/gold2.out"
sh "$EVALS/calib.sh" refund > "$T/calib.out" 2>&1
check "calib prints four replies" test "$(wc -l < "$T/calib.out" | tr -d ' ')" -eq 4
check "calib hides the people's grades" hasnt 'good' "$T/calib.out"

echo "--- record and score"
printf '{"id":"g1","decision":"propose","kind":"refund","amount":80,"judge":"good"}' | sh "$EVALS/record.sh" run1 cases g1
printf '{"id":"g2","decision":"decline","kind":"none","amount":0,"judge":"poor"}' | sh "$EVALS/record.sh" run1 cases g2
check "record writes the file outside the worktree" test -f "$H/evals/run1/cases/g1.json"
sh "$EVALS/score.sh" refund run1 2>&1 | tail -n 1 > "$T/score.out"
check "score counts two cases" jq -e '.cases == 2' "$T/score.out"
check "score has accuracy 0.5" jq -e '.accuracy == 0.5' "$T/score.out"
check "score prices the wrong outcome from costs.tsv" jq -e '.mean_cost == 1' "$T/score.out"
check "score reports that the people agree" jq -e '.people_agree == 1' "$T/score.out"
printf '{"id":"g1","decision":"propose","kind":"refund","amount":70,"judge":"good"}' | sh "$EVALS/record.sh" run2 cases g1
sh "$EVALS/score.sh" refund run2 2>&1 | tail -n 1 > "$T/score2.out"
check "score counts a wrong amount as wrong" jq -e '.accuracy == 0' "$T/score2.out"
check "a wrong amount costs 1 by default" jq -e '.mean_cost == 1' "$T/score2.out"
mkdir -p "$H/evals/empty/cases"
sh "$EVALS/score.sh" refund empty 2>&1 | tail -n 1 > "$T/score3.out"
check "score of an empty run has a high cost" jq -e '.mean_cost == 999' "$T/score3.out"

echo "--- agreement"
for pair in r1:good r2:good r3:poor r4:poor; do
    id=${pair%%:*}
    grade=${pair##*:}
    printf '{"id":"%s","judge":"%s"}' "$id" "$grade" | sh "$EVALS/record.sh" run1 calib "$id"
done
sh "$EVALS/agreement.sh" refund run1 > "$T/agree.out" 2>&1
check "agreement counts four replies" jq -e '.replies == 4' "$T/agree.out"
check "a judge that matches the people is trusted" jq -e '.judge_trusted == true' "$T/agree.out"
printf '{"id":"r1","judge":"poor"}' | sh "$EVALS/record.sh" run3 calib r1
printf '{"id":"r2","judge":"poor"}' | sh "$EVALS/record.sh" run3 calib r2
printf '{"id":"r3","judge":"good"}' | sh "$EVALS/record.sh" run3 calib r3
printf '{"id":"r4","judge":"good"}' | sh "$EVALS/record.sh" run3 calib r4
sh "$EVALS/agreement.sh" refund run3 > "$T/agree2.out" 2>&1
check "a judge that opposes the people is not trusted" jq -e '.judge_trusted == false' "$T/agree2.out"

echo "--- result"
echo "passed $pass, failed $fail. Test folder: $T"
[ "$fail" -eq 0 ]
