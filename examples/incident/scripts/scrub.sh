#!/bin/sh
# Replaces each secret-like value in stdin with [REDACTED]. This is a floor of patterns; add a scanner here.
set -eu
sed -E \
    -e 's/(AKIA|ASIA)[0-9A-Z]{16}/[REDACTED]/g' \
    -e 's/(ghp|gho|ghs|github_pat)_[A-Za-z0-9_]{20,}/[REDACTED]/g' \
    -e 's/sk-[A-Za-z0-9_-]{20,}/[REDACTED]/g' \
    -e 's/xox[abprs]-[A-Za-z0-9-]{10,}/[REDACTED]/g' \
    -e 's/([Bb]earer|[Bb]asic) +[A-Za-z0-9._~+\/=-]{8,}/\1 [REDACTED]/g' \
    -e 's/([Aa]uthorization|[Cc]ookie|[Ss]et-[Cc]ookie)(["'"'"']?[:=] *["'"'"']?)[^",;'"'"']+/\1\2[REDACTED]/g' \
    -e 's/((api|access|secret|private)[_-]?key|token|passwd|password|secret)(["'"'"']?[:=] *["'"'"']?)[^ ",;&'"'"']+/\1\3[REDACTED]/gI' \
    -e 's/(postgres(ql)?|mysql|mongodb|amqp|redis):\/\/[^: \/]+:[^@ ]+@/\1:\/\/[REDACTED]@/g' \
    -e 's/-----BEGIN [A-Z ]*PRIVATE KEY-----.*/[REDACTED]/g' \
    -e 's/[0-9a-fA-F]{40,}/[REDACTED]/g'
