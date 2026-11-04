# Shared by every script. Source it: . "$(dirname "$0")/lib.sh"
# UPKEEP_HOME holds everything that must outlive a worktree: the repository list, the state, the run log and the reports.
UPKEEP_HOME=${UPKEEP_HOME:-$HOME/.upkeep}
STATE="$UPKEEP_HOME/state"
RUNLOG="$UPKEEP_HOME/runs.tsv"
[ -f "$UPKEEP_HOME/settings.sh" ] && . "$UPKEEP_HOME/settings.sh"
# GIT_BASE is the place where the repositories live. STATUS_REPO and STATUS_ISSUE name the issue that receives the weekly report.
GIT_BASE=${GIT_BASE:-https://github.com}

# The ISO week, such as 2026-W44. UPKEEP_WEEK overrides it for a test or for a catch-up run.
week() { printf '%s\n' "${UPKEEP_WEEK:-$(date -u +%G-W%V)}"; }

# A repository name or a week goes into a path and a branch name, so only plain characters pass.
valid() { case "$1" in "" | *[!A-Za-z0-9._-]* | .*) return 1 ;; esac; return 0; }

sha() { if command -v sha256sum > /dev/null 2>&1; then sha256sum | cut -d' ' -f1; else shasum -a 256 | cut -d' ' -f1; fi; }

# state_get <repo> <key> prints the value, or nothing.
state_get() { [ -f "$STATE/$1.env" ] && sed -n "s/^$2=//p" "$STATE/$1.env" | tail -n 1; return 0; }

# state_put <repo> <key> <value> replaces one key. The new file is written beside the old one and moved over it, so a crash leaves a whole file.
state_put() {
    mkdir -p "$STATE"
    t=$(mktemp "$STATE/.put.XXXXXX") || return 1
    { [ -f "$STATE/$1.env" ] && grep -v "^$2=" "$STATE/$1.env"; printf '%s=%s\n' "$2" "$3"; } > "$t"
    mv "$t" "$STATE/$1.env"
}

# runlog <week> <repo> <outcome> <pr> <detail> appends one row. The report reads these rows, because beehave deletes finished runs after seven days.
runlog() {
    mkdir -p "$UPKEEP_HOME"
    [ -f "$RUNLOG" ] || printf 'week\trepo\toutcome\tpr\tdetail\twhen\n' > "$RUNLOG"
    printf '%s\t%s\t%s\t%s\t%s\t%s\n' "$1" "$2" "$3" "$4" "$(printf '%s' "$5" | tr '\t\n' '  ')" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" >> "$RUNLOG"
}

# logged <week> <repo> [outcome...] is true when the log has a row for the week and the repository, with one of the outcomes when given.
logged() {
    w=$1; r=$2; shift 2
    [ -f "$RUNLOG" ] || return 1
    awk -F'\t' -v w="$w" -v r="$r" -v list="$*" 'BEGIN { n = split(list, o, " ") } $1 == w && $2 == r { if (n == 0) found = 1; for (i = 1; i <= n; i++) if ($3 == o[i]) found = 1 } END { exit !found }' "$RUNLOG"
}
