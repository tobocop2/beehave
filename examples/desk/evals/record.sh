#!/bin/sh
# Writes one eval record outside every worktree. $1: eval run. $2: cases or calib. $3: case id. stdin: record. Exit 0 done.
set -eu
. "$(dirname "$0")/../gateway/lib.sh"
dir="$DESK_HOME/evals/$1/$2"
mkdir -p "$dir"
cat > "$dir/$3.json"
