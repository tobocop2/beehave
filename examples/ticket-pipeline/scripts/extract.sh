#!/bin/sh
# $1: the day. Reads the tickets page by page into the stage. Prints "<count> tickets, sha <12 hex>".
set -eu
. "$(dirname "$0")/lib.sh"
check_day "$1"
day=$1
dir=$STAGE/$day
mkdir -p "$dir"
tmp=$dir/records.jsonl.tmp
: > "$tmp"
cursor=""
pages=0
while :; do
    pages=$((pages + 1))
    [ "$pages" -le 1000 ] || die "more than 1000 pages for $day"
    page=$(api -G "$SOURCE_API/tickets" --data-urlencode "day=$day" --data-urlencode "cursor=$cursor") || die "the source failed on page $pages for $day"
    printf '%s' "$page" | jq -c '.tickets[] | {id: (.id | tostring), subject, body}' >> "$tmp" || die "the page $pages for $day is not valid"
    cursor=$(printf '%s' "$page" | jq -r '.next // ""')
    [ -n "$cursor" ] || break
done
sort -u "$tmp" > "$tmp.sorted"
mv "$tmp.sorted" "$dir/records.jsonl"
# Empty the old labels, so labels of an earlier extract can never pass for the labels of this one. The script deletes nothing.
: > "$dir/labels.jsonl"
: > "$dir/labels.tsv"
sha=$(sha256 "$dir/records.jsonl")
printf '%s\n' "$sha" > "$dir/sha"
echo "$(count_lines "$dir/records.jsonl") tickets, sha $(printf '%s' "$sha" | cut -c1-12)"
