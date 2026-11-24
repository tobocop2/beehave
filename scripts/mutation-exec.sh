#!/bin/sh
# Run one test binary for "go test -exec" and record its exit code for scripts/mutation-pr.sh.
#
# Usage: GOFLAGS=-exec=/path/to/mutation-exec.sh go test ...   (go test calls: mutation-exec.sh TESTBINARY ARGS...)
#   MUTATION_ROOT is the module root that gremlins copies. MUTATION_EXIT_LOG is the file that receives the records.
#   Without both variables, the script only runs the test binary.
#
# Gremlins runs "go test" in a copy of the module, with one mutant applied, and keeps only its status: LIVED covers
# exit 0, a kill by a signal and every exit code other than 1 and 2. This script sees the exit code of the test binary.
# Gremlins writes the mutant with go/printer, which reformats the whole file, so a plain diff against the original
# names the first reformatted line and not the mutated one. The script formats the original and the copy with gofmt,
# so that the mutated token is the only difference. It records one line for each differing file, with tabs between
# the fields: path, line, column, after, exit code and first failing line. The line is the first line that differs,
# not counting white space. The column is the first byte of that line that differs and is not white space, and
# "after" is the byte before it that is not white space (0 for none), both in the formatted file, which equals the
# original file when the original is formatted. Gremlins gives the test run no mutator, so the record holds no mutator.
# The mutant is read before the test binary starts, because gremlins restores the copy after a timeout while this
# script, and the test binary, can still run: a late record then still names its own mutant.
# A test run in the module itself (the coverage run of gremlins) differs from nothing and leaves no record.
# The script exits with the exit code of the test binary, and prints the output of the test binary unchanged.
set -u

if [ -z "${MUTATION_ROOT:-}" ] || [ -z "${MUTATION_EXIT_LOG:-}" ]; then
	exec "$@"
fi

tmp="$MUTATION_EXIT_LOG.$$"
trap 'rm -f "$tmp.out" "$tmp.orig" "$tmp.copy"' EXIT INT TERM

# The copy of the module is the nearest folder above the package that holds a go.mod.
copy=$PWD
while [ ! -f "$copy/go.mod" ] && [ "$copy" != / ]; do
	copy=$(dirname "$copy")
done
rel=${PWD#"$copy"}
rel=${rel#/}
original=$MUTATION_ROOT
[ -z "$rel" ] || original=$MUTATION_ROOT/$rel

# One line for each mutated file: path, line and column. The two formatted files differ in the mutated token only.
mutants=
for file in "$PWD"/*.go; do
	name=${file##*/}
	[ -f "$original/$name" ] || continue
	cmp -s "$original/$name" "$file" && continue
	gofmt "$original/$name" >"$tmp.orig" 2>/dev/null || continue
	gofmt "$file" >"$tmp.copy" 2>/dev/null || continue
	header=$(diff -w "$tmp.orig" "$tmp.copy" | head -n 1)
	[ -n "$header" ] || continue
	# The header of a change reads "12c12" or "12,13c12,13". The line is the number before the first letter or comma.
	line=${header%%[acd,]*}
	# The columns ignore spaces and tabs: "column" is the first changed byte that is not white space, and "after" is the
	# last byte before it that is not white space (0 for none). A mutation that changes the spacing before its token,
	# or that adds or removes the "=" of a boundary token, then leaves the token where gremlins puts it.
	places=$(LC_ALL=C awk -v line="$line" '
		function squeeze(text, bytes, cols,   i, n, c) {
			n = 0
			for (i = 1; i <= length(text); i++) {
				c = substr(text, i, 1)
				if (c != " " && c != "\t") { n++; bytes[n] = c; cols[n] = i }
			}
			return n
		}
		NR == FNR { if (FNR == line) old = $0; next }
		FNR == line {
			n = squeeze(old, ob, oc)
			m = squeeze($0, nb, nc)
			k = 1
			while (k <= n && k <= m && ob[k] == nb[k]) k++
			column = (k <= n) ? oc[k] : (n > 0 ? oc[n] + 1 : 1)
			after = (k > 1) ? oc[k - 1] : 0
			print column, after
			exit
		}
	' "$tmp.orig" "$tmp.copy")
	path=$name
	[ -z "$rel" ] || path=$rel/$name
	mutants="$mutants$path	$line	${places% *}	${places#* }
"
done

rc=0
"$@" >"$tmp.out" 2>&1 || rc=$?
cat "$tmp.out"

# The first line of the output that names a failure. A run that fails with none of those words gives its first line.
text=
if [ "$rc" -ne 0 ]; then
	text=$(grep -m 1 -E '^[[:space:]]*(--- FAIL|panic:|fatal error:|FAIL)' "$tmp.out" || true)
	[ -n "$text" ] || text=$(head -n 1 "$tmp.out")
	text=$(printf '%s' "$text" | tr '\t\n' '  ')
fi

printf '%s' "$mutants" | while IFS='	' read -r path line column after; do
	[ -z "$path" ] || printf '%s\t%s\t%s\t%s\t%s\t%s\n' "$path" "$line" "$column" "$after" "$rc" "$text" >>"$MUTATION_EXIT_LOG"
done

exit "$rc"
