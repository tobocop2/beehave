#!/bin/sh
# The check on the prose of one section generation, run as a program gate. Usage: cites.sh <store> <run> <n> <gen>
# Each paragraph needs a marker such as [c4] that points to a supported claim of the section. A number in a paragraph must be in the quote of a claim
# that the paragraph cites. A paragraph that starts with "Not shown:" is exempt. Exit 0 when every paragraph passes; else it prints one FAIL line for each problem.
set -eu
# A gate reports the standard output, so the errors go there too.
exec 2>&1
# A gate call has no env argument, so the store folder is the first argument.
[ $# -eq 4 ] || { echo "usage: cites.sh <store> <run> <n> <gen>" >&2; exit 1; }
RESEARCH_HOME=$1
export RESEARCH_HOME
shift
. "$(dirname "$0")/lib.sh"
folder=$(section_dir "$1" "$2" "$3")
[ -s "$folder/prose.md" ] || { echo "FAIL: the section has no prose"; exit 1; }

# One line for each supported claim: id, a tab, the quote.
jq -r 'select(.quote_ok and (.canary | not) and .verdict == "supported") | "\(.id)\t\(.quote)"' "$folder/claims.jsonl" > "$folder/supported.tsv"

awk -v RS= -v ORS='\n' -v map="$folder/supported.tsv" '
# True when hay holds num as a whole number: no digit on either side, and no decimal point or comma with a digit behind it on either side.
function whole(hay, num,   at, off, rest, before, before2, after, after2) {
  off = 0
  rest = hay
  while ((at = index(rest, num)) > 0) {
    before = (off + at > 1) ? substr(hay, off + at - 1, 1) : ""
    before2 = (off + at > 2) ? substr(hay, off + at - 2, 1) : ""
    after = substr(hay, off + at + length(num), 1)
    after2 = substr(hay, off + at + length(num) + 1, 1)
    if (before !~ /[0-9]/ && after !~ /[0-9]/ && !(after ~ /[.,]/ && after2 ~ /[0-9]/) && !(before ~ /[.,]/ && before2 ~ /[0-9]/)) return 1
    off += at
    rest = substr(hay, off + 1)
  }
  return 0
}
BEGIN {
  FS = "\t"
  while ((getline line < map) > 0) { split(line, parts, "\t"); quote[parts[1]] = parts[2] }
}
{
  paragraph = $0
  gsub(/\n/, " ", paragraph)
  if (paragraph ~ /^Not shown:/) next
  cited = ""
  rest = paragraph
  while (match(rest, /\[c[0-9]+\]/)) {
    id = substr(rest, RSTART + 1, RLENGTH - 2)
    if (!(id in quote)) { print "FAIL: paragraph " NR " cites " id ", which is not a supported claim of this section"; bad = 1 }
    else cited = cited " " quote[id]
    rest = substr(rest, RSTART + RLENGTH)
  }
  if (cited == "" ) { if (paragraph !~ /\[c[0-9]+\]/) { print "FAIL: paragraph " NR " has no citation"; bad = 1 } next }
  bare = paragraph
  gsub(/\[c[0-9]+\]/, "", bare)
  while (match(bare, /[0-9]+([.,][0-9]+)?/)) {
    number = substr(bare, RSTART, RLENGTH)
    if (!whole(cited, number)) { print "FAIL: paragraph " NR " states the number " number ", which is not in a cited quote"; bad = 1 }
    bare = substr(bare, RSTART + RLENGTH)
  }
}
END { exit bad ? 1 : 0 }
' "$folder/prose.md"
