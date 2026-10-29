#!/bin/sh
# Check the notes of a research item: every claim cites a source, and every source answers.
#
# usage: check.sh <notes folder>
#
# Every line of a notes file is a claim, whatever its bullet. Only a blank line and a heading (a line that starts with "#") are not claims.
# A claim must end with one or more tags "[source: <URL>]", and each URL must start with http:// or https://.
# Each URL is fetched with curl. A URL answers when curl gets a status below 400 within 10 seconds, after it follows the redirects.
# Each problem is one line, "FAIL <file>: <reason>", on stdout.
#
# These variables of the environment set the limits:
#   CHECK_MAX_URLS     the most URLs that one run fetches. The default is 40.
#   CHECK_TOTAL_TIME   the most seconds that one run takes before it stops. The default is 120.
#   CHECK_PROBE        a known address. When no source answers, the check asks this address to tell a dead network from dead sources.
#                      The default is https://example.com, a public host. Set CHECK_PROBE to another address on a network that blocks it.
#   CHECK_QUESTIONS    the number of sub-questions. The notes files are 1.md to this number. A file with another name has no sub-question.
#                      When it is not set, any file named with a whole number and .md is accepted.
# CHECK_MAX_URLS and CHECK_TOTAL_TIME must be whole numbers. Another value ends the run with exit code 2.
# obee gives a command only the variables of its env argument, not those of your shell. To change a limit, add it to the env of the check call in workflow.star.
#
# Exit code 0: every claim cites a source and every URL answers.
# Exit code 1: one or more problems. The workflow sends the notes back to the research step. When the total time passes after
#   at least one source failed, the check prints the problems found so far and exits with code 1.
# Exit code 2: the check could not run (no folder, no notes, no curl, no network, a bad limit, a notes file with no sub-question,
#   or the total time passed before any problem was found).
set -u

folder=${1:-}
max_urls=${CHECK_MAX_URLS-40}
total_time=${CHECK_TOTAL_TIME-120}
probe_url=${CHECK_PROBE:-https://example.com}
questions=${CHECK_QUESTIONS:-}

# whole checks that the value $2 of the variable $1 is a whole number, and ends the run with code 2 when it is not.
whole() {
  case $2 in
    '' | *[!0-9]*) echo "check: $1 must be a whole number"; exit 2 ;;
  esac
}

whole CHECK_MAX_URLS "$max_urls"
whole CHECK_TOTAL_TIME "$total_time"
[ -z "$questions" ] || whole CHECK_QUESTIONS "$questions"

[ -d "$folder" ] || { echo "check: $folder is not a folder"; exit 2; }
command -v curl >/dev/null 2>&1 || { echo "check: curl is not on the path"; exit 2; }

nl='
'
started=$(date +%s)
out=
found=0
failed=0
fetched=0
answered=0
capped=0

fail() {
  out="$out$1$nl"
  failed=1
}

# fetch asks one URL for the file $1. It stops the run when the total time has passed, and it skips the URL when the cap is reached.
fetch() {
  if [ "$fetched" -ge "$max_urls" ]; then
    if [ "$capped" -eq 0 ]; then
      fail "FAIL $1: the notes hold more than $max_urls sources. The check reads at most $max_urls URLs in one run"
      capped=1
    fi

    return
  fi

  if [ $(($(date +%s) - started)) -ge "$total_time" ]; then
    # The problems found so far come first. A source that failed means research can try again (code 1). Nothing found means the check cannot run (code 2).
    printf '%s' "$out"
    echo "check: the check ran for more than $total_time seconds"
    [ "$fetched" -gt 0 ] && [ "$failed" -eq 1 ] && exit 1
    exit 2
  fi

  fetched=$((fetched + 1))

  if curl -fsSL --connect-timeout 5 --max-time 10 -o /dev/null "$2" 2>/dev/null; then
    answered=$((answered + 1))
  else
    fail "FAIL $1: the source does not answer: $2"
  fi
}

# urls prints the URL of each tag of the line $1, or fails when the line does not end with a tag or a tag does not hold a web address.
urls() {
  printf '%s\n' "$1" | grep -q '\[source: [^]]*\][[:space:]]*$' || return 1

  printf '%s\n' "$1" | grep -o '\[source: [^]]*\]' | while IFS= read -r tag; do
    url=${tag#\[source: }
    url=${url%\]}

    case $url in
      *' '*) exit 1 ;;
      http://?* | https://?*) printf '%s\n' "$url" ;;
      *) exit 1 ;;
    esac
  done
}

for file in "$folder"/*.md; do
  [ -f "$file" ] || continue
  found=1

  # The workflow names the notes of sub-question N notes/N.md. Another file goes to no research step, so the item stops at once.
  base=${file##*/}
  number=${base%.md}

  ok=1

  case $number in
    '' | 0* | *[!0-9]*) ok=0 ;;
  esac

  if [ "$ok" -eq 1 ] && [ -n "$questions" ] && [ "$number" -gt "$questions" ]; then
    ok=0
  fi

  if [ "$ok" -eq 0 ]; then
    fail "FAIL $file: the notes file has no sub-question"
    printf '%s' "$out"
    echo "check: a notes file has no sub-question: $file"
    exit 2
  fi

  claims=0

  while IFS= read -r line || [ -n "$line" ]; do
    printf '%s\n' "$line" | grep -q '[^[:space:]]' || continue

    case $line in
      '#'*) continue ;;
    esac

    claims=$((claims + 1))

    if ! list=$(urls "$line"); then
      fail "FAIL $file: the claim has no source: $line"
      continue
    fi

    while IFS= read -r url; do
      fetch "$file" "$url"
    done <<EOF
$list
EOF
  done < "$file"

  if [ "$claims" -eq 0 ]; then
    fail "FAIL $file: the notes hold no claim"
  fi
done

[ "$found" -eq 1 ] || { echo "check: $folder holds no notes"; exit 2; }

if [ "$fetched" -gt 0 ] && [ "$answered" -eq 0 ]; then
  if ! curl -fsSL --connect-timeout 5 --max-time 10 -o /dev/null "$probe_url" 2>/dev/null; then
    echo "check: no network: no source answers and $probe_url does not answer"
    exit 2
  fi
fi

printf '%s' "$out"
exit "$failed"
