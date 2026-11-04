#!/bin/sh
# The fake claude of the desk scripts. It names the step from the first words of the shipped prompt, and logs the step to calls.log in the work folder.
# The program holds the work folder in place of @WORK@.
# The clerk reads the request in the case text:
#   "refund N" proposes a refund of N for the booking BK-123456, and the reply says so.
#   "decline" declines the request.
#   "GARBLE" proposes an intent that is not JSON, and "BLANK" proposes an empty intent.
# The judge grades the reply good, unless the reply holds the word POORREPLY.
prompt=$(cat)
printf '%s\n' "$prompt" > "@WORK@/last-prompt.txt"
write=$(printf '%s\n' "$prompt" | sed -n 's/.* to \(\/[^ ]*\) as JSON.*/\1/p' | head -n 1)
case $prompt in
  'You are a clerk'*) step=clerk ;;
  'You grade one reply'*) step=judge ;;
  *) step=unknown ;;
esac
echo "$step" >> "@WORK@/calls.log"
case $step in
  clerk)
    amount=$(printf '%s\n' "$prompt" | sed -n 's/.*refund \([0-9][0-9]*\).*/\1/p' | head -n 1)
    if printf '%s\n' "$prompt" | grep -q 'GARBLE'; then
      printf '{"decision":"propose","intent":"refund eighty please","reply":"We refund.","note":"","procedure_note":""}\n' > "$write"
    elif printf '%s\n' "$prompt" | grep -q 'BLANK'; then
      printf '{"decision":"propose","intent":"","reply":"We refund.","note":"","procedure_note":""}\n' > "$write"
    elif [ -n "$amount" ]; then
      printf '{"decision":"propose","intent":"{\\"kind\\": \\"refund\\", \\"args\\": {\\"booking_id\\": \\"BK-123456\\", \\"amount\\": %s, \\"reason\\": \\"flight cancelled\\"}}","reply":"We refund %s EUR for BK-123456.","note":"The flight was cancelled.","procedure_note":""}\n' "$amount" "$amount" > "$write"
    else
      printf '{"decision":"decline","intent":"","reply":"This desk does not do that.","note":"Not a refund request.","procedure_note":""}\n' > "$write"
    fi
    ;;
  judge)
    case $prompt in
      *POORREPLY*) printf '{"grade":"poor"}\n' > "$write" ;;
      *) printf '{"grade":"good"}\n' > "$write" ;;
    esac
    ;;
esac
printf '{"type":"result","session_id":"s1","result":"done"}\n'
