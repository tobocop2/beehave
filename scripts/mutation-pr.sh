#!/bin/sh
# Run gremlins on the lines that a change touches, and fail if no mutant was applied.
#
# Usage: scripts/mutation-pr.sh BASE
#   BASE is the branch or commit that the pull request targets, for example origin/integration/r1.
#   There is no default, because the base differs from one pull request to the next.
#   MUTATION_TIMEOUT is the time limit for the gremlins run, a positive whole number of seconds. The default is 3600 when
#   the diff changes a non-test Go file in internal/flow or below it, otherwise 1200.
#   The number of gremlins workers comes from .gremlins.yaml at the module root.
#   A run that exceeds the limit fails. The script needs git, go, gremlins, jq and timeout (or gtimeout) on the path.
#   The "not applicable" result needs git only.
#
# Gremlins reports LIVED for exit 0, for a kill by a signal and for every exit code other than 1 and 2 of its test run,
# so LIVED does not prove a weak test. The script sets GOFLAGS so that each mutant test run goes through
# scripts/mutation-exec.sh, which records the exit code and the first failing line of the run. A lived mutant whose run
# exited 0 is a finding. A lived mutant whose run did not exit 0, or left no record, prints "mutation-pr: run error
# path:line TYPE: <reason>", is not counted in the end marker, and fails the run. A run that stops with no report
# prints the recorded runs as "mutation-pr: test run of path:line:column exited with N: <first failing line>". A run that
# passes its time limit also prints, for each record, the verdicts that gremlins wrote for its place ("Gremlins wrote KILLED
# TYPE for this place."), read from the output of gremlins, so that a killed mutant does not read like an error.
# Gremlins gives the test run no mutator. The records and the mutants of the report pair by file, line and the changed byte.
# The script needs go and gofmt on the path.
#
# Gremlins has no package filter. Its coverage run and its mutants cover the module, or the folder that it starts on. So the
# script starts gremlins once for each folder that holds a changed non-test Go file, and drops each folder below another
# changed folder. A changed file in the module root gives one start on ".". A folder that the diff deleted is dropped.
# A start on a folder runs in that folder, with the absolute path of the folder as its argument and the git setting
# diff.relative=true. Gremlins matches the lines of the diff to mutants by file path, and gives mutants paths relative to
# the folder that it starts on, so the setting makes the paths of its own diff equal to them. The script prefixes the
# names of each report with the folder, and merges the reports, so that the checks below read paths from the module root.
# A start on a folder reads the .gremlins.yaml of the module root, because gremlins finds the module root from that folder.
#
# MUTATION_TIMEOUT is one deadline for the whole run. Each start gets the time that is left, and a start with no time left
# fails the run. The script also appends -timeout=<limit>s to GOFLAGS: the coverage run of gremlins sets no -timeout, so go
# test would stop it at 10 minutes. The test run of each mutant sets its own -timeout, which wins over GOFLAGS.
#
# "Applied" means killed + lived + timed out, counted from the JSON report of gremlins.
# The run fails when gremlins exits with a non-zero status, when applied is 0 with a mutant on a changed line,
# or when a mutant lived.
# A run with lived mutants prints two lines for each, at most 50 mutants: "finding: path:line: a mutant lived: TYPE"
# and, after the counts line, the marker line "mutation-pr: lived path:line TYPE". A summary line follows, with the
# full count and the number of finding lines printed.
# The end marker "mutation-pr: end count=N printed=P" is the last "mutation-pr:" line of every run that prints a result
# line ("not applicable", "no candidate" or the counts line). N is the number of lived mutants, and P is the number of
# marker lines. A run that stops before its result line is a tool error and prints no end marker.
# A diff with no Go source change prints "not applicable" and exits 0. A git diff that fails is an error.
# A diff whose changed non-test lines hold no operator that gremlins mutates prints "no candidate: <reason>" and
# exits 0, with no counts line. The script reads that from a report whose mutants are all skipped, and only after it
# proves that the report matches the diff: each file of the report exists, and the mutants that the report does not
# skip are exactly the mutants on the changed lines of the diff, built the way gremlins builds them. An empty report,
# or one that does not match, fails.
set -eu

if [ $# -ne 1 ] || [ -z "$1" ]; then
	echo "usage: scripts/mutation-pr.sh BASE   (BASE is the branch the pull request targets, for example origin/integration/r1)" >&2
	exit 2
fi
base=$1
limit=${MUTATION_TIMEOUT:-}
# Only a positive whole number: 0 would remove the bound, and a word would fail inside timeout as a gremlins exit code.
# An unset or empty value takes the default, which is chosen after the diff is read.
case $limit in
*[!0-9]* | 0*)
	echo "mutation-pr: MUTATION_TIMEOUT is \"$limit\". Set it to a positive whole number of seconds, such as 1200."
	exit 2
	;;
esac

# The end marker of a run with no lived mutant. A reader of the job log takes a log with no end marker as cut.
none_lived='mutation-pr: end count=0 printed=0'

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

git rev-parse --verify --quiet "$base^{commit}" >/dev/null || { echo "mutation-pr: $base is not a commit or branch"; exit 1; }
git merge-base "$base" HEAD >/dev/null 2>&1 || { echo "mutation-pr: no merge base between $base and HEAD (shallow clone?)"; exit 1; }

# An empty diff makes gremlins treat every line as changed and mutate the whole tree. Stop before that.
# The checks up to "not applicable" need git only, so a change with no Go source change needs no other tool.
changed=$(git diff --merge-base "$base" --name-only -- '*.go' ':!*_test.go') || { echo "mutation-pr: git diff against $base failed"; exit 1; }
if [ -z "$changed" ]; then
	echo "mutation-pr: not applicable: no Go source change against $base"
	echo "$none_lived"
	exit 0
fi

# A run that mutates internal/flow takes about 2500 s with 4 workers (21 mutants, 2478 s measured), so its default
# limit is 3600. Every other run keeps 1200.
if [ -z "$limit" ]; then
	limit=1200
	if printf '%s\n' "$changed" | grep -q '^internal/flow/'; then
		limit=3600
	fi
fi

# The paths of the diff are relative to the top of the repository, and the folders of the starts are relative to the module
# root. The two agree only when this folder is both the top of the repository and the module root. Check both.
[ -z "$(git rev-parse --show-prefix)" ] || { echo "mutation-pr: $root is not the top folder of its git repository"; exit 1; }
[ -f go.mod ] || { echo "mutation-pr: $root has no go.mod, so it is not the module root"; exit 1; }

# The folders of the starts: the folder of each changed file, with each folder below another changed folder dropped, and
# only ".", when a changed file is in the module root. A folder that the diff deleted is dropped.
folders=$(printf '%s\n' "$changed" | awk '
	{ d = "."; if (match($0, /\/[^\/]*$/) > 0) d = substr($0, 1, RSTART - 1); set[d] = 1 }
	END {
		if ("." in set) { print "."; exit }
		for (d in set) {
			keep = 1
			for (e in set) if (e != d && index(d, e "/") == 1) keep = 0
			if (keep) print d
		}
	}' | sort | while IFS= read -r folder; do
	if [ -d "$folder" ]; then printf '%s\n' "$folder"; fi
done)
if [ -z "$folders" ]; then
	echo "mutation-pr: no candidate: the folder of each changed file is gone: $(printf '%s' "$changed" | tr '\n' ' ')"
	echo "$none_lived"
	exit 0
fi

command -v gremlins >/dev/null 2>&1 || { echo "mutation-pr: gremlins is not on the path"; exit 1; }
command -v jq >/dev/null 2>&1 || { echo "mutation-pr: jq is not on the path"; exit 1; }
command -v go >/dev/null 2>&1 || { echo "mutation-pr: go is not on the path"; exit 1; }
command -v gofmt >/dev/null 2>&1 || { echo "mutation-pr: gofmt is not on the path"; exit 1; }
if command -v timeout >/dev/null 2>&1; then
	limiter=timeout
elif command -v gtimeout >/dev/null 2>&1; then
	limiter=gtimeout
else
	echo "mutation-pr: neither timeout nor gtimeout is on the path"
	exit 1
fi

report=$(mktemp "${TMPDIR:-/tmp}/mutation-pr.XXXXXX")
exitdir=$(mktemp -d "${TMPDIR:-/tmp}/mutation-pr-exits.XXXXXX")
exits=$exitdir/exits
trap 'rm -f "$report"; rm -rf "$exitdir"' EXIT INT TERM

# Gremlins keeps only a status for each mutant. Go reads GOFLAGS in every "go test" that gremlins starts, so the flag
# -exec runs each test binary through scripts/mutation-exec.sh, which records the exit code of the mutant it runs in.
# The flag -count=1 makes every mutant run its tests, so that a cached pass leaves no record. GOFLAGS splits on
# spaces and has no quoting, so a path with a space cannot name the script.
case $root in
*[[:space:]]*)
	echo "mutation-pr: $root has a space in its path, and GOFLAGS cannot name a script there"
	exit 1
	;;
esac
[ -x "$root/scripts/mutation-exec.sh" ] || { echo "mutation-pr: $root/scripts/mutation-exec.sh is not an executable file"; exit 1; }
export MUTATION_ROOT="$root" MUTATION_EXIT_LOG="$exits"
# "go env GOFLAGS" gives the value that go would read: the variable, or else the file that "go env -w" writes.
# The variable replaces the file in go, so the script merges the value into the variable it sets.
goflags=$(go env GOFLAGS) || { echo "mutation-pr: go env GOFLAGS failed"; exit 1; }
export GOFLAGS="${goflags:+$goflags }-count=1 -exec=$root/scripts/mutation-exec.sh -timeout=${limit}s"
: >"$exits"
: >"$exitdir/verdicts"

# Print each recorded test run: the evidence that a run which ended with no report still leaves.
print_exits() {
	if [ ! -s "$exits" ]; then
		echo "mutation-pr: no mutant test run left an exit code record"
		return 0
	fi
	if [ "${1:-}" != verdicts ]; then
		head -n 200 "$exits" | awk -F '\t' '{ printf "mutation-pr: test run of %s:%s:%s exited with %s%s\n", $1, $2, $3, $5, ($6 == "" ? "" : ": " $6) }'
		return 0
	fi
	# A run that passed its time limit: each record gets the verdict lines that gremlins wrote for its place, found by the
	# same rule as the report: the column of the record, or the column "after" for a boundary mutant.
	head -n 200 "$exits" | awk -F '\t' -v verdicts="$exitdir/verdicts" '
		FILENAME == verdicts { key = $1 ":" $2; n[key]++; col[key, n[key]] = $3; kind[key, n[key]] = $4; state[key, n[key]] = $5; next }
		{
			key = $1 ":" $2; said = ""
			for (i = 1; i <= n[key]; i++) {
				if ((kind[key, i] == "CONDITIONALS_BOUNDARY" ? $4 : $3) == col[key, i]) said = said (said == "" ? "" : ", ") state[key, i] " " kind[key, i]
			}
			printf "mutation-pr: test run of %s:%s:%s exited with %s%s. Gremlins wrote %s for this place.\n", $1, $2, $3, $5, ($6 == "" ? "" : ": " $6), (said == "" ? "no verdict" : said)
		}' "$exitdir/verdicts" -
}

# gremlins runs its own "git diff" and drops the first part of each path, which it expects to be "a/" or "b/".
# With diff.noprefix that part is a folder of the path, and gremlins skips the mutants of each file in a subfolder.
# So give its git the two settings that keep "a/" and "b/", the prefixes that the diff of this script also has.
# They go after the settings that the environment already holds, and git lets the last one win.
git_config_count=${GIT_CONFIG_COUNT:-0}
export "GIT_CONFIG_KEY_$git_config_count=diff.noprefix" "GIT_CONFIG_VALUE_$git_config_count=false"
export "GIT_CONFIG_KEY_$((git_config_count + 1))=diff.mnemonicPrefix" "GIT_CONFIG_VALUE_$((git_config_count + 1))=false"
export GIT_CONFIG_COUNT=$((git_config_count + 2))

# Start gremlins on one folder, for the time that is left. A folder other than "." gets diff.relative=true, so that the
# paths of the diff of gremlins are relative to the folder, like its mutants.
start_gremlins() {
	folder=$1 left=$2 out=$3
	path=.
	if [ "$folder" != . ]; then
		path=$root/$folder
		export "GIT_CONFIG_KEY_$GIT_CONFIG_COUNT=diff.relative" "GIT_CONFIG_VALUE_$GIT_CONFIG_COUNT=true"
		export GIT_CONFIG_COUNT=$((GIT_CONFIG_COUNT + 1))
	fi
	cd "$folder" && "$limiter" -k 30 "$left" gremlins unleash --diff "$base" --output "$out" "$path" </dev/null
}

# One start for each folder. The deadline of the run starts at the first start. The report of a start names files relative
# to its folder, so each report gets the folder as a prefix, and the reports merge into one. A start with no report found
# no mutant in its folder.
printf '%s\n' "$folders" >"$exitdir/folders"
started=
reports=0
noreport=
while IFS= read -r folder; do
	now=$(date +%s)
	[ -n "$started" ] || started=$now
	left=$((limit - (now - started)))
	if [ "$left" -le 0 ]; then
		echo "mutation-pr: the run exceeded MUTATION_TIMEOUT=${limit}s before the start on $folder"
		print_exits verdicts
		exit 1
	fi
	# The output of gremlins goes to the log and to a file: its verdict lines, "STATUS TYPE at path:line:column", name each
	# mutant that it finished, and they are all that a run which ends with no report leaves of its verdicts.
	{ code=0; (start_gremlins "$folder" "$left" "$exitdir/report") || code=$?; echo "$code" >"$exitdir/status"; } | tee "$exitdir/out"
	status=$(cat "$exitdir/status")
	LC_ALL=C awk -v folder="$folder" '
		{ gsub(/\033\[[0-9;]*m/, "") }
		match($0, / at [^ ]+:[0-9]+:[0-9]+$/) {
			head = substr($0, 1, RSTART - 1)
			sub(/^ +/, "", head)
			if (head !~ / /) next
			n = split(substr($0, RSTART + 4), f, ":")
			file = f[1]
			for (i = 2; i < n - 1; i++) file = file ":" f[i]
			if (folder != ".") file = folder "/" file
			type = head; sub(/^.* /, "", type)
			state = head; sub(/ [^ ]*$/, "", state)
			printf "%s\t%s\t%s\t%s\t%s\n", file, f[n - 1], f[n], type, state
		}' "$exitdir/out" >>"$exitdir/verdicts"
	if [ "$status" -eq 124 ] || [ "$status" -eq 137 ]; then
		echo "mutation-pr: the run exceeded MUTATION_TIMEOUT=${limit}s"
		print_exits verdicts
		exit 1
	fi
	if [ "$status" -ne 0 ]; then
		echo "mutation-pr: gremlins exited with status $status"
		print_exits
		exit "$status"
	fi
	if [ -s "$exitdir/report" ]; then
		reports=$((reports + 1))
		jq --arg folder "$folder" '.files = ((.files // []) | map(.file_name |= (if $folder == "." then . else $folder + "/" + . end)))' "$exitdir/report" >"$exitdir/prefixed.$reports" || { echo "mutation-pr: the gremlins report is not readable"; exit 1; }
		: >"$exitdir/report"
	else
		noreport="${noreport:+$noreport, }$folder"
	fi
done <"$exitdir/folders"

# gremlins writes no report when it finds no mutant in the folder, on a changed line or not.
[ "$reports" -gt 0 ] || { echo "mutation-pr: gremlins wrote no report: it found no mutant in $noreport"; exit 1; }
jq -s '{files: (map(.files) | add)}' "$exitdir"/prefixed.* >"$report"

# The merged report lists every mutant of the changed folders, and marks "SKIPPED" each one that is not on a changed line.
# A path mismatch also marks every mutant skipped, so the report must prove its paths: each file that it names
# must be a file under the module root. Only then does "all skipped" mean "no candidate".
names=$(jq -r '.files[]?.file_name' "$report") || { echo "mutation-pr: the gremlins report is not readable"; exit 1; }
unknown=$(printf '%s\n' "$names" | while IFS= read -r name; do [ -z "$name" ] || [ -f "$name" ] || printf '%s ' "$name"; done)
if [ -n "$unknown" ]; then
	echo "mutation-pr: the gremlins report names files that are not under $root: ${unknown% }. Its paths do not match the diff."
	exit 1
fi

# The changed lines of each file, one "path start count" line for each hunk, built the way gremlins builds them
# from its own diff ("git diff --merge-base BASE", with context lines). For each hunk, the range starts at the
# first line after the leading context lines, and it holds one line for each added line. Each header gives the
# number of old and new lines that follow it, so the loop reads those lines as the hunk and never as a header.
# A "\ No newline" line is not counted in a header. Like the diff of gremlins, this diff finds renames and covers
# every file. The fixed prefixes keep the "+++ b/" line when diff.noprefix or diff.mnemonicPrefix is set.
diff=$(git diff --merge-base "$base" --no-color --no-ext-diff --src-prefix=a/ --dst-prefix=b/) || { echo "mutation-pr: git diff against $base failed"; exit 1; }
ranges=$(printf '%s\n' "$diff" | {
	file=
	oldleft=0
	newleft=0
	while IFS= read -r line; do
		# Each case pattern starts with "(": a command substitution ends at a bare ")" in the sh of macOS.
		case $line in ('\'*) continue ;; esac
		if [ "$oldleft" -gt 0 ] || [ "$newleft" -gt 0 ]; then
			case $line in
			('+'*) newleft=$((newleft - 1)) added=$((added + 1)) seen=1 ;;
			('-'*) oldleft=$((oldleft - 1)) seen=1 ;;
			(*)
				oldleft=$((oldleft - 1)) newleft=$((newleft - 1))
				[ "$seen" -eq 1 ] || lead=$((lead + 1))
				;;
			esac
			if [ "$oldleft" -le 0 ] && [ "$newleft" -le 0 ] && [ -n "$file" ] && [ "$added" -gt 0 ]; then
				printf '%s %s %s\n' "$file" $((start + lead)) "$added"
			fi
			continue
		fi
		case $line in
		('+++ b/'*) file=${line#'+++ b/'} ;;
		('+++ '*) file= ;;
		('@@ '*)
			old=${line#'@@ -'}
			old=${old%% *}
			new=${line#'@@ -'*' +'}
			new=${new%% *}
			case $old in (*,*) oldleft=${old#*,} ;; (*) oldleft=1 ;; esac
			case $new in (*,*) newleft=${new#*,} start=${new%,*} ;; (*) newleft=1 start=$new ;; esac
			lead=0 added=0 seen=0
			;;
		esac
	done
})

# gremlins skips each mutant that is not on a changed line of its own diff. A skipped mutant on a changed line
# of this diff, or a mutant off those lines that gremlins did not skip, means that the report does not match the diff.
mismatch=$(jq -r --arg ranges "$ranges" '
	($ranges | split("\n") | map(select(. != "") | split(" ") | {file: .[0], start: (.[1] | tonumber), count: (.[2] | tonumber)})) as $hunks
	| [.files[]? | .file_name as $file | .mutations[]? | (.line as $line | any($hunks[]; .file == $file and $line >= .start and $line < .start + .count)) as $changed
		| select(($changed and .status == "SKIPPED") or ((($changed) | not) and .status != "SKIPPED")) | "\($file):\(.line)"]
	| unique | join(" ")' "$report") || { echo "mutation-pr: the gremlins report is not readable"; exit 1; }
if [ -n "$mismatch" ]; then
	echo "mutation-pr: the gremlins report does not match the diff against $base: it skips a mutant on a changed line, or keeps one off the changed lines, at $mismatch"
	exit 1
fi

counts=$(jq -r '[.files[]?.mutations[]?.status] | [length, (map(select(. != "SKIPPED")) | length), (map(select(. == "KILLED")) | length), (map(select(. == "LIVED")) | length), (map(select(. == "TIMED OUT")) | length)] | @tsv' "$report") || { echo "mutation-pr: the gremlins report is not readable"; exit 1; }
total=$(echo "$counts" | cut -f1)
candidates=$(echo "$counts" | cut -f2)
killed=$(echo "$counts" | cut -f3)
lived=$(echo "$counts" | cut -f4)
timed=$(echo "$counts" | cut -f5)
applied=$((killed + lived + timed))

# "No candidate" is the only result line of its run. A reader of the job log takes a counts line with applied=0 as a
# failed run, so this path prints no counts line.
if [ "$total" -gt 0 ] && [ "$candidates" -eq 0 ]; then
	echo "mutation-pr: no candidate: no changed line of $(printf '%s' "$changed" | tr '\n' ' ') holds an operator that gremlins mutates; the report marks all $total mutant(s) of the changed folders skipped"
	echo "$none_lived"
	exit 0
fi
echo "mutation-pr: applied=$applied killed=$killed lived=$lived timed_out=$timed"

if [ "$total" -eq 0 ]; then
	echo "mutation-pr: the gremlins report holds no mutant"
	echo "$none_lived"
	exit 1
fi

if [ "$applied" -eq 0 ]; then
	echo "mutation-pr: no mutant was applied. Check that the diff against $base touches covered Go code."
	echo "$none_lived"
	exit 1
fi
if [ "$lived" -eq 0 ]; then
	echo "$none_lived"
	exit 0
fi

# Each lived mutant, at most 50, gives two lines. The remediation workflow keeps the lines with the prefix "finding: ",
# as "finding: path:line: text". The engine step reads the marker lines, as "mutation-pr: lived path:line TYPE".
# The check above proved that every mutant that gremlins did not skip is on a changed line, so each lived mutant is in the diff.
#
# Gremlins reads LIVED for exit 0, for a kill by a signal and for every exit code other than 1 and 2. A lived mutant is a
# weak test only when its test run exited 0. The records of scripts/mutation-exec.sh hold the exit code of each run, by
# the file, the line and the column of the first changed byte that is not white space, and the column of the byte before it.
# The report of gremlins holds the column of the mutated token. The two agree, except for CONDITIONALS_BOUNDARY, which adds
# or removes the "=" after the token: that mutant pairs by the byte before the change. Mutators that share one place, such as
# "+=" with its two mutators, share their records. Each KILLED mutant at the place takes one record with exit code 1 or 2.
# A lived mutant takes the records that are left. It is weak when each of them exited 0. It is an error of the run when each
# of them did not exit 0, or when none is left. When some exited 0 and some did not, the error says that it is ambiguous: a
# late record of a cancelled mutant, or a second lived mutant at the place, can hold the other code, and the records do not
# say which run is which. A mutant that gremlins reports as TIMED OUT keeps running after gremlins stops it, and its late
# record is not read for any other mutant at its place.
classified=$exitdir/lived.json
jq -c --rawfile log "$exits" '
	def boundary: .type == "CONDITIONALS_BOUNDARY";
	def at: "\(.file):\(.line)";
	def mine($m): select(.file == $m.file and .line == $m.line and (if $m | boundary then .after == $m.column else .column == $m.column end));
	def sharing($m): select(.file == $m.file and .line == $m.line and .column == $m.column and boundary == ($m | boundary));
	[.files[]? | .file_name as $file | .mutations[]? | {file: $file, line, column, type, status}] as $mutants
	| ($log | split("\n") | map(select(. != "") | split("\t")
		| {file: .[0], line: (.[1] | tonumber), column: (.[2] | tonumber), after: (.[3] | tonumber), rc: (.[4] | tonumber), text: (.[5] // "")})) as $records
	| [$mutants[] | select(.status == "LIVED") | . as $m
		| ($mutants | map(sharing($m))) as $group
		| ($group | any(.status == "TIMED OUT")) as $late
		| ($group | map(select(.status == "KILLED")) | length) as $killed
		| ($records | map(mine($m)) | to_entries) as $own
		| ([$own[] | select(.value.rc == 1 or .value.rc == 2) | .key] | .[:$killed]) as $taken
		| ($own | map(select(.key as $i | any($taken[]; . == $i) | not) | .value)) as $left
		| (if $late then [] else $left end) as $runs
		| ($runs | map(select(.rc != 0))) as $failed
		| ($runs | map(.rc) | unique) as $codes
		| {at: at, type, weak: (($runs | length) > 0 and ($failed | length) == 0),
			ambiguous: (($runs | length) > 0 and ($failed | length) > 0 and ($failed | length) < ($runs | length)), codes: $codes} + ($failed[0] // {})]' "$report" >"$classified" || { echo "mutation-pr: the gremlins report is not readable"; exit 1; }
weak=$(jq 'map(select(.weak)) | length' "$classified")
errors=$((lived - weak))

# Each weak mutant, at most 50, gives two lines. The mutants with a failed test run give one line each, at most 50.
jq -r 'map(select(.weak)) | .[:50] | (.[] | "finding: \(.at): a mutant lived: \(.type)"), (.[] | "mutation-pr: lived \(.at) \(.type)")' "$classified"
jq -r 'map(select(.weak | not)) | .[:50] | .[]
	| "mutation-pr: run error \(.at) \(.type): " + (if .ambiguous then "ambiguous: the test runs recorded at this place exited with codes \(.codes | map(tostring) | join(" and ")), and the records do not tell which run belongs to this mutant"
		elif .rc == null then "no exit code was recorded for the test run of this mutant"
		elif .rc > 128 and .rc <= 192 then "the test run ended with exit code \(.rc), a kill by signal \(.rc - 128)"
		else "the test run exited with code \(.rc): \(.text)" end)' "$classified"
printed=$weak
[ "$printed" -le 50 ] || printed=50
if [ "$weak" -gt 0 ]; then
	echo "mutation-pr: $weak mutant(s) lived, $printed finding line(s) printed. Kill each one with a test."
fi
if [ "$errors" -gt 0 ]; then
	echo "mutation-pr: $errors mutant(s) lived with a test run that did not exit 0. That is an error of the run, not a weak test: read the run error lines."
fi
echo "mutation-pr: end count=$weak printed=$printed"
exit 1
