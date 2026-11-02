#!/bin/sh
# Prints {"id", "case", "reply"} per calibration reply. $1: intent. Exit 0 done. The grades stay in the file.
# Columns of $DESK_HOME/gold/<intent>-replies.tsv: id, case_file, reply_file, grade_a, grade_b (good or poor).
set -eu
. "$(dirname "$0")/../gateway/lib.sh"
gold="$DESK_HOME/gold"
awk -F '\t' 'NR > 1 { print $1 "\t" $2 "\t" $3 }' "$gold/$1-replies.tsv" |
    while IFS="$(printf '\t')" read -r id case reply; do
        jq -n -c --arg id "$id" --rawfile case "$gold/$case" --rawfile reply "$gold/$reply" '{id: $id, case: $case, reply: $reply}'
    done
