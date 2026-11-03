#!/bin/sh
# Saves the postmortem draft on stdin as the file postmortems/<incident>.md in the incident home. Argument: incident. Prints the path.
# A real desk commits the file to the ops repository and opens a review there. This script stays a file write, so a person reviews it before it goes anywhere.
set -eu
. "$(dirname "$0")/lib.sh"
safe incident "$1"
draft=$(cat)
[ -n "$draft" ] || die "postmortem: the draft is empty"
mkdir -p "$INC_HOME/postmortems"
file="$INC_HOME/postmortems/$1.md"
printf '%s\n' "$draft" > "$file"
echo "$file"
