#!/bin/sh
# The spend ledger of one swarm. Usage: ledger.sh add|check|report <swarm> [args]. Exit 0, 1 when check finds the cap or the STOP file, 64 for an unknown verb.
# The ledger lives outside every work folder: $HOME/.swarm/<swarm>/ledger.tsv. A person makes $HOME/.swarm/<swarm>/STOP to stop new runs.
set -eu
USAGE="usage: ledger.sh add <swarm> <item> <kind> <usd> | check <swarm> <cap-usd> | report <swarm>"
verb="${1:?$USAGE}"
swarm="${2:?$USAGE}"
dir="${HOME:?}/.swarm/$swarm"
file="$dir/ledger.tsv"

total() {
  if [ -f "$file" ]; then awk -F '\t' '{ s += $4 } END { printf "%.2f", s }' "$file"; else printf '0.00'; fi
}

case "$verb" in
add)
  mkdir -p "$dir"
  # The key makes a repeated add count once, so a resume does not count a step twice.
  key="${3:?$USAGE}.${4:?$USAGE}"
  if [ -f "$file" ] && awk -F '\t' -v k="$key" '$1 == k { f = 1 } END { exit !f }' "$file"; then
    echo "known $key"
    exit 0
  fi
  printf '%s\t%s\t%s\t%s\n' "$key" "$3" "$4" "${5:?$USAGE}" >> "$file"
  echo "added $key"
  ;;
check)
  if [ -f "$dir/STOP" ]; then
    echo "the file STOP exists"
    exit 1
  fi
  now=$(total)
  if awk -v t="$now" -v c="${3:?$USAGE}" 'BEGIN { exit !(t + 0 >= c + 0) }'; then
    echo "the spend $now reached the cap ${3}"
    exit 1
  fi
  echo "spend $now"
  ;;
report)
  echo "total $(total) dollars"
  ;;
*)
  echo "$USAGE"
  exit 64
  ;;
esac
