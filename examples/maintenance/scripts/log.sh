#!/bin/sh
# Usage: log.sh <week> <repo> <outcome> <detail>     (the pull request column stays empty)
# The workflow and the tick call this for every outcome except "opened", which pr.sh writes together with the state.
set -eu
. "$(dirname "$0")/lib.sh"
valid "$1" && valid "$2" || exit 2
case "$3" in skipped | red | error | deferred) ;; *) echo "log: unknown outcome $3" >&2; exit 2 ;; esac
runlog "$1" "$2" "$3" "" "$4"
