#!/bin/sh
# Prints {"id", "case"} per gold case. $1: intent. $2: yes for the held-out slice, no for practice. Exit 0 done.
# Columns of $DESK_HOME/gold/<intent>.tsv: id, held_out, kind_a, amount_a, kind_b, amount_b, case_file.
# A kind is an intent name, "none" for a firm no, or "person". Only the case text leaves this script.
set -eu
. "$(dirname "$0")/../gateway/lib.sh"
gold="$DESK_HOME/gold"
awk -F '\t' -v slice="$2" 'NR > 1 && $2 == slice { print $1 "\t" $7 }' "$gold/$1.tsv" |
    while IFS="$(printf '\t')" read -r id file; do
        jq -n -c --arg id "$id" --rawfile case "$gold/$file" '{id: $id, case: $case}'
    done
