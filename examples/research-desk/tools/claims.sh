#!/bin/sh
# The claims of one section generation. Usage: claims.sh <verb> <run> <n> <gen>
#   reset       drop the claims of this generation
#   add         read a JSON array of text, source (a number) and quote on the standard input. A claim whose quote is not in the snapshot of its source
#               is kept with quote_ok false, and a line "REJECT" names it on the standard output. An agent cannot cite what this script cannot find.
#   plant       add one false claim. It takes a real source and a real quote, and states the opposite. Prints its id.
#   export      print the claims that have a found quote, the planted claim among them, as JSON for the judge
#   verdicts    read a JSON object of id and verdict on the standard input. If the judge accepts the planted claim, exit 3 and record nothing.
#   supported   print one line for each supported claim: [id] text, and the quote
#   count       print the number of found quotes (quote) or of supported claims (supported): claims.sh count <run> <n> <gen> <quote|supported>
set -eu
. "$(dirname "$0")/lib.sh"
[ $# -ge 4 ] || die "usage: claims.sh <verb> <run> <n> <gen>"
verb=$1
run=$2
folder=$(section_dir "$run" "$3" "$4")
file=$folder/claims.jsonl
mkdir -p "$folder"
: >> "$file"
NL='
'
TAB=$(printf '\t')
dir=$(run_dir "$run")

next_id() {
  seq=$(cat "$dir/claim-seq" 2>/dev/null || echo 0)
  echo $((seq + 1)) > "$dir/claim-seq"
  echo "c$((seq + 1))"
}

case $verb in
  reset)
    # A section that is not done starts from its pages again, so a claim of a stopped run does not mix with the claims of the new run.
    : > "$file"
    ;;
  add)
    input=$(cat)
    printf '%s' "$input" | jq -e 'type == "array"' >/dev/null || die "the claims are not a JSON array"
    count=$(printf '%s' "$input" | jq 'length')
    i=0
    while [ "$i" -lt "$count" ]; do
      row=$(printf '%s' "$input" | jq -c ".[$i]")
      i=$((i + 1))
      text=$(printf '%s' "$row" | jq -r '.text // ""')
      source=$(printf '%s' "$row" | jq -r '.source // "" | tostring')
      quote=$(printf '%s' "$row" | jq -r '.quote // ""')
      ok=false
      # A quote must sit on one line of the page. The snapshot is a text file of lines, and a fixed-string search takes each line of a multi-line pattern
      # as its own pattern (an empty line matches every line). So a quote with a newline, or a tab (the staleness table splits at tabs), is refused.
      multi=false
      case $quote in
        *"$NL"* | *"$TAB"*) multi=true ;;
      esac
      if [ -n "$text" ] && [ -n "$quote" ] && [ "$multi" = false ] && uint "$source" && [ -f "$dir/snap/s$source.txt" ] && grep -F -q -- "$quote" "$dir/snap/s$source.txt"; then
        ok=true
      else
        printf 'REJECT: the quote is not on one line of source s%s: %s\n' "$source" "$(printf '%s' "$quote" | tr '\n\t' '  ')"
      fi
      jq -n -c --arg id "$(next_id)" --arg text "$text" --arg source "s$source" --arg quote "$quote" --argjson ok "$ok" \
        '{id: $id, text: $text, source: $source, quote: $quote, quote_ok: $ok, canary: false, verdict: ""}' >> "$file"
    done
    ;;
  plant)
    base=$(jq -c -s 'map(select(.quote_ok)) | .[0] // empty' "$file")
    [ -n "$base" ] || exit 0
    id=$(next_id)
    printf '%s' "$base" | jq -c --arg id "$id" '.id = $id | .canary = true | .verdict = "" | .text = "The effect was the opposite of what the source states, and it grew by 300 percent."' >> "$file"
    echo "$id"
    ;;
  export)
    jq -c -s 'map(select(.quote_ok) | {id, text, quote})' "$file"
    ;;
  verdicts)
    input=$(cat)
    printf '%s' "$input" | jq -e 'type == "object"' >/dev/null || die "the verdicts are not a JSON object"
    jq -r -s --argjson v "$input" '
      map(select(.quote_ok)) as $c
      | if ($c | any(.canary and ($v[.id] == "supported"))) then "void"
        elif ($c | any($v[.id] == null)) then "missing"
        else "ok" end' "$file" > "$folder/check"
    case $(cat "$folder/check") in
      void) echo "The judge accepted the planted false claim, so its verdicts are void and none is recorded."; exit 3 ;;
      missing) die "the judge gave no verdict for a claim" ;;
    esac
    jq -c --argjson v "$input" 'if .quote_ok then .verdict = $v[.id] else . end' "$file" > "$folder/claims.next"
    mv "$folder/claims.next" "$file"
    ;;
  supported)
    jq -r 'select(.quote_ok and (.canary | not) and .verdict == "supported") | "[\(.id)] \(.text) -- quote: \"\(.quote)\" (\(.source))"' "$file"
    ;;
  count)
    case ${5:-quote} in
      quote) jq -s 'map(select(.quote_ok and (.canary | not))) | length' "$file" ;;
      supported) jq -s 'map(select(.quote_ok and (.canary | not) and .verdict == "supported")) | length' "$file" ;;
      *) die "unknown count: $5" ;;
    esac
    ;;
  *) die "unknown verb: $verb" ;;
esac
