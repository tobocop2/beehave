#!/bin/sh
# Fetch one page and keep its text as a snapshot. Usage: fetch.sh <run> <url>
# It prints one line: the source id, the address and the path of the snapshot, tab separated. An address that was fetched before keeps its id.
# It refuses an address on a private network, and a page of more than 2 MB. HTML tags are stripped; use pandoc here when it is installed.
set -eu
. "$(dirname "$0")/lib.sh"
[ $# -eq 2 ] || die "usage: fetch.sh <run> <url>"
run=$1
url=$2
tab=$(printf '\t')

case $url in
  http://* | https://*) ;;
  *) die "REFUSE: only http and https addresses are fetched: $url" ;;
esac
host=${url#*://}
host=${host%%/*}
host=${host##*@}
case $host in
  # A bracketed address is an IPv6 literal. None is fetched.
  \[*) die "REFUSE: $host is an IPv6 address" ;;
esac
host=${host%%:*}
case $host in
  localhost | 127.* | 10.* | 192.168.* | 169.254.* | 172.1[6-9].* | 172.2[0-9].* | 172.3[0-1].* | 0.0.0.0 | *.local | *.internal) die "REFUSE: $host is on a private network" ;;
esac

dir=$(run_dir "$run")
mkdir -p "$dir/snap"
: >> "$dir/sources.tsv"
known=$(awk -F "$tab" -v u="$url" '$2 == u { print $1 }' "$dir/sources.tsv")
if [ -n "$known" ]; then
  printf '%s\t%s\t%s\n' "$known" "$url" "$dir/snap/$known.txt"
  exit 0
fi

count=$(wc -l < "$dir/sources.tsv")
id="s$((count + 1))"
curl -sS -L --fail-with-body --max-filesize 2000000 -o "$dir/snap/$id.raw" "$url"
sed -e 's/<[^>]*>//g' "$dir/snap/$id.raw" > "$dir/snap/$id.txt"
printf '%s\t%s\t%s\n' "$id" "$url" "$(sha < "$dir/snap/$id.txt")" >> "$dir/sources.tsv"
printf '%s\t%s\t%s\n' "$id" "$url" "$dir/snap/$id.txt"
