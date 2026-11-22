package gate

import (
	"slices"
	"strings"

	"github.com/tobocop2/beehave/internal/model"
)

const (
	// programMarker starts the line that the job writes when it refuses a head or fails the final check of the gate program. The reason follows.
	programMarker = "beehave-gate-program"

	// programCheck is the word that compares the gate paths with the base commit after the last head command.
	programCheck = "gpcompare"
)

// programFirst is the word that resolves git and the tools of the final check to absolute paths, before any head command runs.
// The words of the gate program use these paths and never a tool through the PATH, which head code can write.
const programFirst = `gpabs() { for t in "$@"; do case $t in /*) ;; *) return 1 ;; esac; done; }; ` +
	`gpbin=$(command -v git) && gpfind=$(command -v find) && gpsort=$(command -v sort) && gpreadlink=$(command -v readlink) && gpcat=$(command -v cat) && ` +
	`gpshasum=$(command -v sha256sum || command -v shasum) && gpabs "$gpbin" "$gpfind" "$gpsort" "$gpreadlink" "$gpcat" "$gpshasum" && ` +
	`case $gpshasum in */shasum) gpshaarg='-a 256' ;; *) gpshaarg= ;; esac`

// programShell defines the shell functions of the gate program. Git reads the private repository of the snapshot with no replace refs
// and no config of the machine. The snapshot folder only feeds the overlay: the final compare never reads it. It reads the values that the job kept
// in shell variables (a path, a mode and a digest or a link target for each listed file). The final compare uses no git word and no tool through
// the PATH: it reads the digest, the execute bit and the kind of each file.
const programShell = `{
gpfail() { echo "` + programMarker + ` $*"; return 1; }
gpgit() { GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null GIT_NO_REPLACE_OBJECTS=1 "$gpbin" "$@"; }
gphash() { set -- $("$gpshasum" $gpshaarg "$1"); printf %s "$1"; }
gpsnapshot() {
	mkdir -p "$gpsnap/files" && : > "$gpsnap/manifest" || return 1
	gpgit init -q --bare "$gpsnap/repo.git" || return 1
	gpgit -C "$gpsnap/repo.git" fetch -q --no-tags -- "$PWD" "$gpsha" || "$gpbin" -C "$gpsnap/repo.git" fetch -q --no-tags -- "$gpurl" "$gpsha" || return 1
	[ "$(gpgit -C "$gpsnap/repo.git" rev-parse --verify -q "$gpsha^{commit}")" = "$gpsha" ] || return 1
	gpgit -C "$gpsnap/repo.git" ls-tree -r "$gpsha" -- $gppaths > "$gpsnap/tree.txt" || return 1
	gpwant=
	while IFS= read -r line; do
		meta=${line%%"$gptab"*}; path=${line#*"$gptab"}
		case $path in \"*) return 1 ;; esac
		set -- $meta
		[ "$2" = blob ] || return 1
		mkdir -p "$(dirname -- "$gpsnap/files/$path")" && gpgit -C "$gpsnap/repo.git" cat-file blob "$3" > "$gpsnap/files/$path" || return 1
		case $1 in 120000) sum=$(cat -- "$gpsnap/files/$path") ;; *) sum=$(gphash "$gpsnap/files/$path") ;; esac
		gpwant="${gpwant}${gpwant:+$gpnl}$1$gptab$path$gptab$sum"
		printf '%s\t%s\n' "$1" "$path" >> "$gpsnap/manifest"
	done < "$gpsnap/tree.txt"
	gpwantlist=$(printf '%s\n' "$gpwant" | while IFS="$gptab" read -r m p s; do printf '%s\n' "$p"; done | LC_ALL=C "$gpsort")
}
gprefuse() {
	gpgit cat-file -e "$gpsha^{commit}" || "$gpbin" fetch -q --no-tags -- "$gpurl" "$gpsha"
	mb=$(gpgit merge-base HEAD "$gpsha") && [ -n "$mb" ] || { gpfail "the head has no merge base with the base commit $gpsha, so the gate program cannot be compared"; return 1; }
	changed=$(gpgit diff-tree -r --no-renames --name-only "$mb" HEAD -- $gppaths) || { gpfail "the gate program of the head cannot be compared with the base commit $gpsha"; return 1; }
	[ -z "$changed" ] || { gpfail "the head changed the gate program, paths $(printf %s "$changed" | paste -s -d ' ' -). The gate runs the program of the base commit. The owner lands a change of these paths, then the gate runs again"; return 1; }
}
gprestore() {
	while IFS="$gptab" read -r mode path; do
		mkdir -p -- "$(dirname -- "$path")" || return 1
		case $mode in
		120000) ln -s -- "$(cat -- "$gpsnap/files/$path")" "$path" ;;
		100755) cp -- "$gpsnap/files/$path" "$path" && chmod 755 "./$path" ;;
		*) cp -- "$gpsnap/files/$path" "$path" && chmod 644 "./$path" ;;
		esac || return 1
	done < "$gpsnap/manifest"
}
gpmode() {
	has=0; [ -n "$("$gpfind" -P "$2" -maxdepth 0 -perm -100)" ] && has=1
	want=0; [ "$1" = 100755 ] && want=1
	[ "$has" = "$want" ]
}
gpentry() {
	case $1 in
	120000) [ -L "$2" ] && [ "$("$gpreadlink" -- "$2")" = "$3" ] ;;
	*) [ -f "$2" ] && [ ! -L "$2" ] && [ "$(gphash "$2")" = "$3" ] && gpmode "$1" "$2" ;;
	esac
}
gplist() {
	gpdisk=
	for p in $gppaths; do
		q=${p%/}
		case $q in */*) d=${q%/*}; b=${q##*/} ;; *) d=.; b=$q ;; esac
		[ -n "$("$gpfind" -P "$d" -maxdepth 1 -name "$b")" ] || continue
		[ -z "$("$gpfind" -P "$q" ! -type f ! -type l ! -type d)" ] || { gpfail "$p holds a file that is not a regular file, a link or a folder"; return 1; }
		found=$("$gpfind" -P "$q" ! -type d)
		[ -z "$found" ] || gpdisk="${gpdisk}${gpdisk:+$gpnl}$found"
	done
	gpdisk=$(printf '%s\n' "$gpdisk" | LC_ALL=C "$gpsort")
}
gpdiff() {
	printf '%s\n' "$1" | while IFS= read -r l; do case "$gpnl$2$gpnl" in *"$gpnl$l$gpnl"*) ;; *) printf '%s ' "$l" ;; esac; done
}
gpcompare() {
	gplist || return 1
	[ "$gpdisk" = "$gpwantlist" ] || { gpfail "the gate program changed while the gate ran: $(gpdiff "$gpdisk" "$gpwantlist")$(gpdiff "$gpwantlist" "$gpdisk")"; return 1; }
	bad=$(printf '%s\n' "$gpwant" | while IFS="$gptab" read -r mode path sum; do gpentry "$mode" "$path" "$sum" || { printf %s "$path"; break; }; done)
	[ -z "$bad" ] || { gpfail "the gate program changed while the gate ran: $bad"; return 1; }
}
}`

// programBefore are the words after the checkout and before any head command: the values of the gate program, the functions, the snapshot of the
// listed paths at the base commit, the refusal of a head that changed them, the overlay that puts the base files in the copy, and the deletion of the snapshot.
func programBefore(project model.Project, cloneURL, clone, baseSHA string) []string {
	snap := model.ShellQuote(clone + "/snap")
	words := []string{
		"gpsnap=" + snap + "; gpsha=" + model.ShellQuote(baseSHA) + "; gpurl=" + model.ShellQuote(cloneURL) +
			"; gppaths=" + model.ShellQuote(strings.Join(project.GateProgram, " ")) + `; gptab=$(printf '\t'); gpnl=$(printf '\nx'); gpnl=${gpnl%x}`,
		programShell,
		`{ gpsnapshot || gpfail "the base commit ` + baseSHA + ` cannot be read, so the gate program cannot be copied from it"; }`,
		`gprefuse`,
	}

	for _, path := range project.GateProgram {
		quoted := model.ShellQuote(path)
		words = append(words, "{ gpgit rm -r -q -f --ignore-unmatch -- "+quoted+" && rm -rf -- "+quoted+"; }")
	}

	return append(words, `{ gprestore || gpfail "the gate program of the base commit cannot be written into the copy"; }`)
}

// programClosing is the end of the candidate word: the words that run after every head command, the word that prints the logs, and the exit test.
// A project with a gate program runs the final check there, and its failure fails the job.
func programClosing(project model.Project) (words []string, logs, exit string) {
	logs, exit = `cat ../checks.txt ../revert.txt`, `[ "$headrc" -eq 0 ]`
	if len(project.GateProgram) == 0 {
		return nil, logs, exit
	}

	return []string{`( ` + programCheck + ` ) > ../program.txt 2>&1; programrc=$?`}, `"$gpcat" ../checks.txt ../revert.txt ../program.txt`, exit + ` && [ "$programrc" -eq 0 ]`
}

// programResult is the result of a job that wrote a program marker line, or false when the log holds none.
func programResult(log string, project model.Project) (Result, bool) {
	for _, line := range strings.Split(log, "\n") {
		reason, found := strings.CutPrefix(line, programMarker+" ")
		if !found {
			continue
		}

		steps := slices.Concat(hostSteps(project, StatusUnknown, StatusUnknown), []Step{{Name: model.StepGateProgram, Status: StatusFail}})

		return Result{Report: tail("the gate refused the head: " + reason + "\n"), Steps: steps}, true
	}

	return Result{}, false
}
