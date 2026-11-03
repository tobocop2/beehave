# Shared setup for the desk scripts: loads the desk config, defines die, safe, api and now.
set -eu
INC_HOME="${INC_HOME:-$HOME/.incident}"
CREDENTIAL_HEADER="$INC_HOME/credential.header"
. "$INC_HOME/config"
umask 077

die() { printf '%s\n' "$*" >&2; exit 2; }

# Refuse a value that is unsafe in a path or a URL.
safe() { # name value
    case "$2" in
        '' | *[!A-Za-z0-9._-]*) die "$1 is not a safe name: $2" ;;
    esac
}

# The credential is a header file, so it is never in the environment or on a command line.
api() { curl -sS --fail-with-body -H @"$CREDENTIAL_HEADER" "$@"; }

now() { date +%s; }
