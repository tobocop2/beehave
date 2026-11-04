set -eu
# Take the signed messages from the inbox into the ledger. Usage: intake.sh
# A message is NAME.msg (lines of key=value) with NAME.msg.sig, made by sign.sh. The signature must verify against
# allowed_signers for the person in the by= line, in the namespace beehave-signoff. Anything else goes to refused/ and the
# ledger records why. A taken message moves to taken/ under the SHA-256 of its text, so a replay is not taken twice.
# Types: signoff (req, doc, hash, role, decision, reason) and delegate (role, to, until, reason).
. "$(dirname "$0")/lib.sh"
NS=beehave-signoff

kv() { sed -n "s/^$1=//p" "$2" | head -n 1; }

refuse() { # file reason
    b=$(basename "$1")
    d=$(( $(date +%s) ))
    mv "$1" "$AP_HOME/refused/$d-$$-$b" 2> /dev/null || true
    mv "$1.sig" "$AP_HOME/refused/$d-$$-$b.sig" 2> /dev/null || true
    append_raw refused "${REQ:--}" "${DOC:--}" - "${BY:--}" - - "$(jq -cn --arg r "$2" --arg f "$b" '{reason: $r, file: $f}')"
    printf 'refused %s %s\n' "$b" "$2"
}

# The lock is held for the whole pass, so two intakes cannot take the same message.
acquire || die "the ledger is busy"
for f in "$AP_HOME"/inbox/*.msg; do
    [ -e "$f" ] || continue
    [ -e "$f.sig" ] || continue   # the signature has not arrived yet
    REQ=; DOC=; BY=
    BY=$(kv by "$f"); type=$(kv type "$f")
    case "$BY" in '' | *[!A-Za-z0-9._-]*) BY=; refuse "$f" "the by line is missing or unsafe"; continue ;; esac
    m=$(sha < "$f")
    if [ -e "$AP_HOME/taken/$m.msg" ]; then refuse "$f" "the message was taken before"; continue; fi
    if ! ssh-keygen -Y verify -f "$AP_HOME/allowed_signers" -I "$BY" -n "$NS" -s "$f.sig" < "$f" > /dev/null 2>&1; then
        refuse "$f" "the signature does not verify for $BY"; continue
    fi
    role=$(kv role "$f"); reason=$(kv reason "$f" | tr -d '\n' | cut -c1-300)
    case "$role" in '' | *[!A-Za-z0-9._-]*) refuse "$f" "the role is missing or unsafe"; continue ;; esac
    case "$type" in
        signoff)
            REQ=$(kv req "$f"); DOC=$(kv doc "$f"); h=$(kv hash "$f"); dec=$(kv decision "$f")
            case "$REQ$DOC$h" in '' | *[!A-Za-z0-9._-]*) refuse "$f" "req, doc or hash is missing or unsafe"; continue ;; esac
            case "$dec" in approve | reject) ;; *) refuse "$f" "decision must be approve or reject"; continue ;; esac
            open=$(sql "SELECT count(*) FROM events WHERE type='opened' AND req='$(qt "$REQ")' AND doc='$(qt "$DOC")';")
            [ "$open" -gt 0 ] || { refuse "$f" "no open request $REQ for $DOC"; continue; }
            shut=$(sql "SELECT count(*) FROM events WHERE type='closed' AND req='$(qt "$REQ")';")
            [ "$shut" -eq 0 ] || { refuse "$f" "the request is closed"; continue; }
            ver=$(sql "SELECT count(*) FROM events WHERE type='version' AND doc='$(qt "$DOC")' AND hash='$(qt "$h")';")
            [ "$ver" -gt 0 ] || { refuse "$f" "the hash is not a version of $DOC"; continue; }
            mv "$f" "$AP_HOME/taken/$m.msg"; mv "$f.sig" "$AP_HOME/taken/$m.msg.sig"
            append_raw signoff "$REQ" "$DOC" "$h" "$BY" "$role" "$dec" "$(jq -cn --arg m "$m" --arg r "$reason" '{msg: $m, reason: $r}')"
            printf 'signoff %s %s %s\n' "$REQ" "$BY" "$dec"
            ;;
        delegate)
            to=$(kv to "$f"); until=$(kv until "$f")
            case "$to" in '' | *[!A-Za-z0-9._-]*) refuse "$f" "to is missing or unsafe"; continue ;; esac
            case "$until" in '' | *[!0-9]*) refuse "$f" "until must be an epoch second"; continue ;; esac
            [ "$until" -gt "$(now)" ] || { refuse "$f" "until is in the past"; continue; }
            mv "$f" "$AP_HOME/taken/$m.msg"; mv "$f.sig" "$AP_HOME/taken/$m.msg.sig"
            append_raw delegate - - - "$BY" "$role" - "$(jq -cn --arg m "$m" --arg t "$to" --argjson u "$until" --arg r "$reason" '{msg: $m, to: $t, until: $u, reason: $r}')"
            printf 'delegate %s %s %s\n' "$BY" "$to" "$role"
            ;;
        *) refuse "$f" "type must be signoff or delegate" ;;
    esac
done
release
exit 0
