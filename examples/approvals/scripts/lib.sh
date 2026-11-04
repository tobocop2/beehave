# Shared setup for every desk script. The desk folder is outside the repository and outside every worktree.
#   config              sets NOTIFY_API
#   credential.header   the header that the notifier needs, mode 600
#   allowed_signers     the public keys of the people (ssh-keygen -Y format; the principal is the person id)
#   ledger.db           the append-only ledger (SQLite)
#   docs/               a git repository with one file for each document; a version is a commit
#   inbox/              signed messages that wait for intake: NAME.msg and NAME.msg.sig
#   taken/ refused/     messages that intake took or refused, kept as evidence
#   certs/              the certificate of each closed request
#   sent.tsv            the notices sent, by key
# APPROVAL_NOW sets the clock. Only the tests set it.
AP_HOME="${AP_HOME:-$HOME/.approval}"
. "$AP_HOME/config"
umask 077
LEDGER="$AP_HOME/ledger.db"
SELF=$(cd "$(dirname "$0")" && pwd)

die() { printf '%s\n' "$*" >&2; exit 2; }

# A name from outside must be safe in a path and in SQL.
safe() { # name value
    case "$2" in
        '' | *[!A-Za-z0-9._-]*) die "$1 is not a safe name: $2" ;;
    esac
}

now() { printf '%s' "${APPROVAL_NOW:-$(date +%s)}"; }
sha() { if command -v sha256sum > /dev/null 2>&1; then sha256sum | cut -d' ' -f1; else shasum -a 256 | cut -d' ' -f1; fi; }
qt() { printf '%s' "$1" | sed "s/'/''/g"; }
sql() { sqlite3 -batch -cmd '.timeout 5000' "$LEDGER" "$@"; }

mkdir -p "$AP_HOME/inbox" "$AP_HOME/taken" "$AP_HOME/refused" "$AP_HOME/certs"
[ -e "$AP_HOME/docs/.git" ] || git init -q "$AP_HOME/docs"
sql "CREATE TABLE IF NOT EXISTS events(
  seq INTEGER PRIMARY KEY AUTOINCREMENT, at INTEGER NOT NULL, type TEXT NOT NULL, req TEXT NOT NULL,
  doc TEXT NOT NULL, hash TEXT NOT NULL, who TEXT NOT NULL, role TEXT NOT NULL, decision TEXT NOT NULL,
  detail TEXT NOT NULL, prev TEXT NOT NULL, row_hash TEXT NOT NULL);
CREATE TRIGGER IF NOT EXISTS events_no_update BEFORE UPDATE ON events BEGIN SELECT RAISE(ABORT, 'the ledger is append-only'); END;
CREATE TRIGGER IF NOT EXISTS events_no_delete BEFORE DELETE ON events BEGIN SELECT RAISE(ABORT, 'the ledger is append-only'); END;" > /dev/null || die "the ledger cannot start"

# The lock is flock(1) on the file lock in the desk folder. The kernel drops it when the holder dies, so no lock goes stale.
# A caller waits at most 10 seconds. Every append to the ledger needs it: append_raw refuses to run without it.
acquire() {
    exec 9> "$AP_HOME/lock"
    flock -w 10 9 || return 1
    LOCKED=1
}
release() { LOCKED=; exec 9>&-; }

# One row of the ledger, with the hash of the row before it. Call it with the lock held (acquire).
# type req doc hash who role decision detail. The detail is one line.
append_raw() {
    [ -n "${LOCKED:-}" ] || die "a ledger row needs the lock"
    case "$8" in *"
"*) die "the detail of a ledger row must be one line" ;; esac
    prev=$(sql "SELECT row_hash FROM events ORDER BY seq DESC LIMIT 1;")
    [ -n "$prev" ] || prev=genesis
    at=$(now)
    rh=$(printf '%s|%s|%s|%s|%s|%s|%s|%s|%s|%s' "$at" "$1" "$2" "$3" "$4" "$5" "$6" "$7" "$8" "$prev" | sha)
    sql "INSERT INTO events(at,type,req,doc,hash,who,role,decision,detail,prev,row_hash) VALUES ($at,'$(qt "$1")','$(qt "$2")','$(qt "$3")','$(qt "$4")','$(qt "$5")','$(qt "$6")','$(qt "$7")','$(qt "$8")','$prev','$rh');" || die "the ledger refused the row"
}
append() { acquire || die "the ledger is busy"; append_raw "$@"; rc=$?; release; return $rc; }

# The rows that are a change a person made. The run's own notice rows are not: they are no reason to end a wait. This is the only place
# for the rule. await.sh reads it with seen_seq; tally.sh puts the same expression in its one query.
CHANGE_ROWS="type NOT IN ('notified','notify-failed')"
seen_seq() { sql "SELECT coalesce(max(seq),0) FROM events WHERE $CHANGE_ROWS;"; }

# The hash of the file of a document. submit.sh writes the file first, so the file can be ahead of the ledger for a moment.
doc_hash() { git hash-object "$AP_HOME/docs/$1.md" 2> /dev/null; }
# The current version of a document is the last version row of the ledger. submit.sh appends that row after the commit, so every reader
# that asks for the current version sees a hash that the docs repository holds.
current_version() { sql "SELECT hash FROM events WHERE type='version' AND doc='$(qt "$1")' ORDER BY seq DESC LIMIT 1;"; }
