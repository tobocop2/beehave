#!/bin/sh
# Save the prose of one section generation from the standard input. Usage: save-prose.sh <run> <n> <gen>
set -eu
. "$(dirname "$0")/lib.sh"
[ $# -eq 3 ] || die "usage: save-prose.sh <run> <n> <gen>"
folder=$(section_dir "$1" "$2" "$3")
mkdir -p "$folder"
cat > "$folder/prose.md"
