#!/bin/sh
# Print the report of a run as Markdown: the abstract, the newest generation of each section, and the sources that the claims cite. Usage: assemble.sh <run>
set -eu
. "$(dirname "$0")/lib.sh"
[ $# -eq 1 ] || die "usage: assemble.sh <run>"
run=$1
dir=$(run_dir "$run")
tab=$(printf '\t')
printf '# Report %s\n\n## Abstract\n\n' "$run"
sh "$(dirname "$0")/store.sh" get-abstract "$run"
printf '\n'
sh "$(dirname "$0")/store.sh" sections-text "$run"
printf '## Sources\n\n'
awk -F "$tab" '{ print "- " $1 ": " $2 " (sha256 " $3 ")" }' "$dir/sources.tsv"
