#!/usr/bin/env bash
# Acceptance script of item 432: refuse a build_host name with a trailing dot, ".." or ".-".
#
# Usage: acceptance.sh [TREE]   TREE is the checkout under test, by default the checkout that holds this script.
#   MODE=full   loads each input through config.LoadFolder (go test -overlay). This needs the module cache.
#   MODE=regex  applies the pattern of internal/config/files.go to each input, with no loader, forms or contexts.
#   MODE=auto   (the default) tries full, and falls back to regex when the full mode cannot build.
#   GATE=1      runs the gate of the project (make check) in TREE after the rows.
# Each cell prints one ROW line: form, context, input, class, base value, head value, actual value, first problem.
# The exit code is 0 when every cell equals its head value, 1 when a cell is red, 2 when the instrument failed.
set -u
HERE=$(cd "$(dirname "$0")" && pwd)
TREE=$(cd "${1:-$HERE/../../../..}" && pwd)
MODE=${MODE:-auto}
WORK=$(mktemp -d "${TMPDIR:-/tmp}/accept432.XXXXXX")
ROWS=$WORK/rows.tsv
: > "$ROWS"

run_full() {
  sed 's/^package PKG$/package config_test/' "$HERE/gen.go.in" > "$WORK/gen_test.go"
  cp "$HERE/accept_test.go.in" "$WORK/accept_test.go"
  printf '{"Replace":{"%s":"%s","%s":"%s"}}\n' \
    "$TREE/internal/config/zz_accept432_gen_test.go" "$WORK/gen_test.go" \
    "$TREE/internal/config/zz_accept432_test.go" "$WORK/accept_test.go" > "$WORK/overlay.json"
  (cd "$TREE" && ACCEPT432_OUT=$ROWS go test -count=1 -overlay "$WORK/overlay.json" -run '^TestAccept432$' ./internal/config/ > "$WORK/full.log" 2>&1)
  rc=$?
  echo "full mode: go test rc=$rc ($(grep -c '' "$WORK/full.log") log lines, log $WORK/full.log)"
  return $rc
}

run_regex() {
  line=$(grep -E '^var clusterName = regexp\.MustCompile\(`.*`\)$' "$TREE/internal/config/files.go")
  if [ -z "$line" ]; then
    echo "regex mode: no clusterName pattern line in $TREE/internal/config/files.go; the regex mode cannot read this tree" >&2
    return 2
  fi
  pattern=${line#*\`}; pattern=${pattern%\`*}
  echo "regex mode: pattern $pattern"
  mkdir -p "$WORK/regex"
  sed 's/^package PKG$/package main/' "$HERE/gen.go.in" > "$WORK/regex/gen.go"
  cp "$HERE/regex_main.go.in" "$WORK/regex/main.go"
  (cd "$WORK/regex" && GO111MODULE=off GOFLAGS= go run . "$pattern" > "$ROWS")
}

case $MODE in
  full) run_full || { echo "instrument failed: full mode did not run" >&2; exit 2; } ;;
  regex) run_regex || exit 2 ;;
  auto) if ! run_full; then echo "full mode did not build; falling back to the regex mode"; : > "$ROWS"; run_regex || exit 2; MODE=regex; else MODE=full; fi ;;
  *) echo "MODE must be full, regex or auto" >&2; exit 2 ;;
esac

cat "$ROWS"
echo "== summary (mode $MODE, tree $TREE at $(git -C "$TREE" rev-parse --short HEAD 2>&1))"
awk -F'\t' '
  $1 == "ROW" {
    rows++; inputs[$4] = 1
    split($6, b, "="); split($7, h, "="); split($8, a, "=")
    if (a[2] == "acc") acc++; else ref++
    if (a[2] != h[2]) { red++; redc[$5]++ }
    if (a[2] != b[2]) { diff++; diffc[$5]++ }
    if ($2 ~ /^(string|name-only)$/ && ($3 == "skypilot" || $3 == "regex")) {
      if ($4 == "\"gpu\"") gpu = a[2]
      if ($4 == "\"a b\"") space = a[2]
    }
  }
  END {
    n = 0; for (k in inputs) n++
    printf "inputs %d, cells %d, accepted %d, refused %d\n", n, rows, acc, ref
    printf "control: \"gpu\" %s (want acc), \"a b\" %s (want ref)\n", gpu, space
    printf "red cells (actual != head value): %d\n", red
    for (k in redc) printf "  red %s: %d\n", k, redc[k]
    printf "cells that differ from the base value: %d\n", diff
    for (k in diffc) printf "  differs %s: %d\n", k, diffc[k]
    if (rows == 0 || acc == 0 || ref == 0 || gpu != "acc" || space != "ref") { print "INSTRUMENT BROKEN"; exit 2 }
    exit red > 0 ? 1 : 0
  }' "$ROWS"
rc=$?

if [ "${GATE:-0}" = 1 ]; then
  (cd "$TREE" && export PATH=$HOME/go/bin:/usr/local/go/bin:$PATH && make check > "$WORK/gate.log" 2>&1)
  echo "gate: make check rc=$? (log $WORK/gate.log)"
fi
exit $rc
