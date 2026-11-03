#!/bin/sh
# Search with SearXNG. Usage: search.sh <question> [MAX]. It prints the addresses of the first MAX results (default 3), one for each line.
# SEARCH_API is the base address of the SearXNG server.
set -eu
: "${SEARCH_API:?SEARCH_API is not set}"
[ $# -ge 1 ] || { echo "usage: search.sh <question> [MAX]" >&2; exit 1; }
# The answer goes to a variable first: a failed curl then stops the script, where a pipe into jq would hide its exit code and read as "no results".
answer=$(curl -sS --fail-with-body --get --data-urlencode "q=$1" --data-urlencode "format=json" "$SEARCH_API/search")
printf '%s' "$answer" |
  jq -r --argjson max "${2:-3}" '[.results[].url] | unique | .[:$max][]'
