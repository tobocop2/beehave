#!/bin/sh
# Replaces tokens with vault values. stdin: masked text. Exit 0 done. Only write.sh and reply.sh call it.
set -eu
. "$(dirname "$0")/lib.sh"
awk -v vault="$VAULT" '
    BEGIN { FS = "\t"; while ((getline line < vault) > 0) { split(line, f, "\t"); v[f[1]] = f[2] } }
    {
        out = ""; rest = $0
        while (match(rest, /M_[A-Z]+_[0-9a-f]{10}/)) {
            t = substr(rest, RSTART, RLENGTH)
            out = out substr(rest, 1, RSTART - 1) ((t in v) ? v[t] : t)
            rest = substr(rest, RSTART + RLENGTH)
        }
        print out rest
    }'
