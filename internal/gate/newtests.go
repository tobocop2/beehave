package gate

import (
	"strconv"
	"strings"

	"github.com/tobocop2/beehest/internal/model"
)

const (
	// markerCap is the most name lines and the most marker lines that the job of a candidate prints. The end marker carries the full counts.
	markerCap = 50

	// nameRoom is the most bytes that each kind of name line takes in the log, so the end markers stay in the 16384 bytes of the log that a host reads.
	nameRoom = 4096

	// baseCache is the folder, below the work root of the host, that holds one parsed base report for each base commit.
	baseCache = "beehest-base-reports"

	// newTestMarker starts the line that names one new test: the class and the name follow.
	newTestMarker = "beehest-new-test "

	// failedTestMarker starts the line of a new test that failed or was skipped on the head: the kind, the class and the name follow.
	failedTestMarker = "beehest-new-test-failed "

	// newTestsEnd starts the end marker of the new-tests check: "new=<count> failed=<count>".
	newTestsEnd = "beehest-new-tests-end "

	// newTestsError starts the line that says why the new-tests check could not compare.
	newTestsError = "beehest-new-tests-error "

	// badNameExit is the exit code of junitAwk for a report with a test name or class name that holds a tab or a newline. The tab splits the fields
	// of the parsed report and the newline splits its lines, so such a name cannot be compared. The job then writes an error line and no end marker, and a cache keeps no report that holds such a name.
	badNameExit = "3"

	// revertPassedMarker starts the line of a new test that passed or was skipped on the base code: the class and the name follow.
	revertPassedMarker = "beehest-revert-passed "

	// revertEnd starts the end marker of the revert run: "new=<count> passed=<count> cases=<count>".
	revertEnd = "beehest-revert-end "

	// revertError starts the line that says why the revert run could not run.
	revertError = "beehest-revert-error "

	// The names of the steps that the job of a candidate runs apart. Each writes a stepMarker line.
	stepHeadGate  = model.StepHeadGate
	stepSecrets   = model.StepSecrets
	stepBaseSuite = model.StepBaseSuite
	stepRevertRun = model.StepRevertRun
)

// junitAwk reads the JUnit files that its arguments name and prints one line for each test case: the class, the name and the result, split by tabs.
// The result is pass, fail or skipped. A test case is keyed by class and name, so a subtest is its own test. The reader is awk only: each
// record is one tag, which the record separator "<" makes, and a skipped or failure tag belongs to the test case that is open.
const junitAwk = `BEGIN { RS = "<" }
function attr(s, k) {
	if (match(s, "[ \t\n]" k "=\"[^\"]*\"")) {
		return substr(s, RSTART + length(k) + 3, RLENGTH - length(k) - 4)
	}
	return ""
}
function emit() {
	if (index(cn nm, "\t") || index(cn nm, "\n")) bad = 1; else print cn "\t" nm "\t" st
	open = 0
}
/^testcase[ \t\n]/ {
	if (open) emit()
	cn = attr($0, "classname"); nm = attr($0, "name"); st = "pass"; open = 1
	if ($0 ~ /\/>[ \t\r\n]*$/) emit()
	next
}
/^skipped[ \t\n\/>]/ { if (open) st = "skipped"; next }
/^(failure|error)[ \t\n\/>]/ { if (open) st = "fail"; next }
/^\/testcase/ { if (open) emit() }
END { if (bad) exit ` + badNameExit + ` }`

// keepAwk is the awk function that prints a name line of one kind while the kind has fewer than cap lines and its lines fit in room bytes.
// The first line that does not fit stops the kind, so the lines of a kind are always the first ones.
const keepAwk = `function keep(kind, line) {
	if (shown[kind] < cap && used[kind] + length(line) + 1 <= room) { shown[kind]++; used[kind] += length(line) + 1; print line } else shown[kind] = cap
}
`

// compareAwk reads the parsed base report, then the parsed head report. It prints a name line for each test of the head that the base lacks,
// a marker line for each of those that did not pass, and the end marker. Both kinds of line stop as keepAwk says, and the end marker counts them all.
// It also writes every new test, class and name split by a tab, to the file out, which the revert run reads.
const compareAwk = keepAwk + `FNR == NR { seen[$1 "\t" $2] = 1; next }
{
	if (($1 "\t" $2) in seen) next
	n++
	print $1 "\t" $2 > out
	keep("new", "` + newTestMarker + `" $1 " " $2)
	if ($3 != "pass") { f++; keep("failed", "` + failedTestMarker + `" $3 " " $1 " " $2) }
}
END { print "` + newTestsEnd + `new=" n + 0 " failed=" f + 0 }`

// revertAwk reads the new tests, then the parsed report of the revert run. It prints a marker line for each new test that passed or was skipped
// there, in the order of the new tests and as keepAwk says, then the end marker with the full counts. A new test that the report lacks failed.
const revertAwk = keepAwk + `FNR == NR { if (!(($1 "\t" $2) in want)) { want[$1 "\t" $2] = 1; order[++n] = $1 "\t" $2 }; next }
{ cases++; if ($3 != "fail") ran[$1 "\t" $2] = 1 }
END {
	for (i = 1; i <= n; i++) if (order[i] in ran) { p++; split(order[i], f, "\t"); keep("passed", "` + revertPassedMarker + `" f[1] " " f[2]) }
	print "` + revertEnd + `new=" n + 0 " passed=" p + 0 " cases=" cases + 0
}`

// candidateWord is the word of the job of a candidate head, after the checkout. It runs the head gate, the secret scan, the base suite and the
// revert run apart, each in its own subshell, and a failed step never stops a later one. Then it prints the new-tests check, the revert lines, and
// last the step marker lines, so all of them sit in the tail of the log. The job exits with the exit code of the head gate.
// A project with no test_command runs no base suite, so only a cached base report can serve.
func candidateWord(project model.Project, pins []Pin, base, baseSHA, clone string) string {
	cache := model.ShellQuote(filepathDir(clone) + "/" + baseCache)
	glob := globWord(project.TestReport)
	before, after := reportChecks(project.TestReport, len(project.GateProgram) > 0)
	headGate := strings.Join(append(append(before, gateWords(project, pins)...), after...), " && ")

	parts := []string{
		`mb=$(git merge-base HEAD ` + model.ShellQuote(baseSHA) + ` 2>/dev/null)`,
		stepWord("headrc", "( "+headGate+" )"),
		stepWord("secretrc", `( [ -n "$mb" ] && `+limited(project, `gitleaks git --no-banner --log-opts="$mb..HEAD" .`)+` )`),
		"cache=" + cache + `; mkdir -p "$cache"`,
		baseSuite(project, glob),
		compareWord(glob, base, baseSHA),
		revertRun(project, glob),
	}

	closing, logs, exit := programClosing(project)
	parts = append(parts, closing...)
	parts = append(parts, logs, closingMarkers(), exit)

	return "{ " + strings.Join(parts, "; ") + "; }"
}

// compareWord is the word of the new-tests check. It parses the head report, compares it with the cached base report, writes its lines to
// ../checks.txt and every new test to ../new.tsv.
// A head with no merge base with the commit gets a message that names the base branch and the commit.
func compareWord(glob, base, baseSHA string) string {
	noBase := model.ShellQuote(newTestsError + "the head has no merge base with the commit " + baseSHA + " of the base branch " + base)

	return "{ " + `awk ` + model.ShellQuote(junitAwk) + ` ` + glob + ` > ../head.tsv; jrc=$?; }; ` +
		`: > ../new.tsv; { if [ "$jrc" -eq ` + badNameExit + ` ]; then echo "` + newTestsError + `a test name in the head report holds a tab or a newline, so the new tests cannot be named"; ` +
		`elif [ -n "$mb" ] && [ -s "$cache/$mb" ]; then ` + cappedAwk() + `-v out=../new.tsv ` + model.ShellQuote(compareAwk) + ` "$cache/$mb" ../head.tsv; ` +
		`elif [ -z "$mb" ]; then echo ` + noBase + `; ` +
		`else echo "` + newTestsError + `no base report for $mb"; fi; } > ../checks.txt`
}

// cappedAwk is the start of the command that runs compareAwk or revertAwk: tab fields, the line cap and the byte room, and the C locale, so
// that length counts bytes.
func cappedAwk() string {
	return `LC_ALL=C awk -F '\t' -v cap=` + strconv.Itoa(markerCap) + ` -v room=` + strconv.Itoa(nameRoom) + ` `
}

// filepathDir is the folder that holds the clone folder: the work root of the host.
func filepathDir(clone string) string {
	return clone[:strings.LastIndex(clone, "/")]
}

// stepWord runs command and keeps its exit code in the shell variable rcVar. It writes no marker line: a step prints any amount of output, and
// the host reads only the tail of the log, so closingMarkers writes every marker after all other output.
func stepWord(rcVar, command string) string {
	return command + `; ` + rcVar + `=$?; echo`
}

// closingMarkers is the word that writes the step marker lines, after the new-tests check and the end marker, so they are the last lines of the log
// whatever the steps printed. The base suite step writes a line only when it ran: a job that read the cache has no base-suite code.
func closingMarkers() string {
	line := func(name, rcVar string) string { return `echo "` + stepMarker + " " + name + ` rc=$` + rcVar + `"` }

	return `echo; ` + line(stepHeadGate, "headrc") + `; ` + line(stepSecrets, "secretrc") + `; ` +
		`[ -z "${baserc+x}" ] || ` + line(stepBaseSuite, "baserc") + `; [ -z "${revertrc+x}" ] || ` + line(stepRevertRun, "revertrc")
}

// revertRun is the word that runs the new tests on the base code, when the head has new tests. It checks out the merge base in its own folder,
// copies onto it the test files that the head added or changed (the test_paths globs pick them), runs test_command there and parses the reports
// of that folder only. It writes its lines to ../revert.txt. A head with no new test, or a project with no test_command or no test_paths, gets an
// error line and no run.
func revertRun(project model.Project, glob string) string {
	run := `echo "` + revertError + `the project sets no test_command or no test_paths, so the new tests cannot run on the base code" > ../revert.txt`
	if project.TestCommand != "" && len(project.TestPaths) > 0 {
		run = revertWord(project, glob)
	}

	return `if [ -s ../new.tsv ]; then ` + run + `; else echo "` + revertError + `the job found no new test, so nothing ran on the base code" > ../revert.txt; fi`
}

// revertWord is the word of the revert run of a project with test_command and test_paths.
func revertWord(project model.Project, glob string) string {
	specs := make([]string, 0, len(project.TestPaths))
	for _, path := range project.TestPaths {
		specs = append(specs, model.ShellQuote(":(glob)"+path))
	}

	copied := `git diff --name-only -z --no-renames --diff-filter=AM "$mb" HEAD -- ` + strings.Join(specs, " ") + ` > ../revert.paths && ` +
		`{ [ ! -s ../revert.paths ] || git -C ../revert --literal-pathspecs checkout "$(git rev-parse HEAD)" --pathspec-from-file=../revert.paths --pathspec-file-nul; }`
	run := `( git worktree add --detach ../revert "$mb" >/dev/null && ` + copied + ` && cd ../revert && ` + limited(project, "sh -c "+model.ShellQuote(project.TestCommand)) + ` )`
	read := `if [ -d ../revert ]; then ( cd ../revert && awk ` + model.ShellQuote(junitAwk) + ` ` + glob + ` ) > ../revert.tsv; ` +
		cappedAwk() + model.ShellQuote(revertAwk) + ` ../new.tsv ../revert.tsv > ../revert.txt; ` +
		`else echo "` + revertError + `the merge base could not be checked out for the revert run" > ../revert.txt; fi`

	return stepWord("revertrc", run) + `; ` + read
}

// baseSuite is the word that runs the test command on the merge base when the cache holds no report for it, parses the report that the run left,
// and stores it under a temporary name that it renames to the SHA name, so no reader sees a partial file. A red suite still leaves its report.
func baseSuite(project model.Project, glob string) string {
	if project.TestCommand == "" {
		return ":"
	}

	run := `( git worktree add --detach ../base "$mb" >/dev/null && cd ../base && ` + limited(project, "sh -c "+model.ShellQuote(project.TestCommand)) + ` )`
	store := `if ( cd ../base && awk ` + model.ShellQuote(junitAwk) + ` ` + glob + ` ) > "$cache/.tmp.$mb.$$" && [ -s "$cache/.tmp.$mb.$$" ]; then mv "$cache/.tmp.$mb.$$" "$cache/$mb"; fi`

	return `if [ -n "$mb" ] && [ ! -s "$cache/$mb" ]; then ` + stepWord("baserc", run) + `; ` + store + `; fi`
}

// Checks is what the engine reads from the log of a candidate job that passed: the new tests, the ones that failed a check, the exit code of
// the secret scan, and whether the end marker was there. NewTests and Failed are capped, and New and FailedCount are the full counts.
type Checks struct {
	NewTests    []string `json:"new_tests,omitempty"`
	Failed      []string `json:"failed,omitempty"`
	New         int      `json:"new"`
	FailedCount int      `json:"failed_count"`
	// Secrets is the exit code of the secret scan, or nil when the log has no single marker line for it.
	Secrets *int `json:"secrets_rc,omitempty"`
	// Complete is true when the log holds the end marker. A log with none was cut, or the job could not compare.
	Complete bool   `json:"complete"`
	Problem  string `json:"problem,omitempty"`
	// Revert is the verdict of the revert run. RevertPassed names the new tests that passed or were skipped on the base code, capped, and
	// RevertPassedCount counts them all. RevertCases is the number of test cases in the report of the run, RevertRC its exit code or nil.
	Revert            model.RevertVerdict `json:"revert"`
	RevertPassed      []string            `json:"revert_passed,omitempty"`
	RevertPassedCount int                 `json:"revert_passed_count"`
	RevertCases       int                 `json:"revert_cases"`
	RevertRC          *int                `json:"revert_rc,omitempty"`
	RevertProblem     string              `json:"revert_problem,omitempty"`
}

// ReadChecks reads the lines of the new-tests check from the log of a candidate job. The end marker is the last line with its prefix, and the
// name lines and marker lines are the run of such lines just above it, so a test that prints the prefix earlier changes nothing.
func ReadChecks(log string) Checks {
	lines := strings.Split(log, "\n")
	checks := Checks{}

	codes := markerCodes(log)
	checks.Secrets = singleCode(codes[stepSecrets])
	checks.RevertRC = singleCode(codes[stepRevertRun])
	readRevert(lines, &checks)

	end := endMarker(lines, &checks)
	if end < 0 {
		return checks
	}

	checks.Complete = true
	counts := fieldCounts(strings.TrimPrefix(lines[end], newTestsEnd))
	checks.New, checks.FailedCount = counts["new"], counts["failed"]
	readRun(lines[:end], &checks)

	return checks
}

// singleCode reads the exit code of a step that has exactly one marker line, and is nil otherwise. A code that does not read is 1.
func singleCode(codes []string) *int {
	if len(codes) != 1 {
		return nil
	}

	code := atoiOr(codes[0], 1)

	return &code
}

// readRevert reads the lines of the revert run: the end marker or error line that ends it, and the run of passed lines just above the end marker.
// The verdict is pass only for an end marker with a test case in the report and no new test that passed on the base code.
func readRevert(lines []string, checks *Checks) {
	checks.Revert = model.RevertFail

	end := revertEndLine(lines, checks)
	if end < 0 {
		return
	}

	counts := fieldCounts(strings.TrimPrefix(lines[end], revertEnd))
	checks.RevertPassedCount, checks.RevertCases = counts["passed"], counts["cases"]
	checks.RevertPassed = passedRun(lines[:end])

	switch {
	case checks.RevertCases == 0:
		checks.RevertProblem = "the report of the revert run holds no test case, so the run proves nothing"
	case checks.RevertPassedCount == 0:
		checks.Revert = model.RevertPass
	}
}

// revertEndLine is the index of the last line of the revert run when it is an end line, or -1. The job ends the run with one end line or one
// error line, so an end line that a test printed above an error line counts for nothing. It records the reason of a last error line in checks.
func revertEndLine(lines []string, checks *Checks) int {
	end := -1

	for i, line := range lines {
		if strings.HasPrefix(line, revertEnd) {
			end, checks.RevertProblem = i, ""
		} else if reason, found := strings.CutPrefix(line, revertError); found {
			end, checks.RevertProblem = -1, reason
		}
	}

	return end
}

// passedRun is the names of the run of passed lines that ends the lines.
func passedRun(lines []string) []string {
	var names []string

	for i := len(lines) - 1; i >= 0; i-- {
		name, found := strings.CutPrefix(lines[i], revertPassedMarker)
		if !found {
			break
		}

		names = append([]string{name}, names...)
	}

	return names
}

// fieldCounts reads "key=<n>" fields. A value that does not read gives 0.
func fieldCounts(text string) map[string]int {
	counts := map[string]int{}

	for _, field := range strings.Fields(text) {
		key, value, _ := strings.Cut(field, "=")
		counts[key] = atoiOr(value, 0)
	}

	return counts
}

// readRun reads the name lines and marker lines in the run of such lines that ends the lines.
func readRun(lines []string, checks *Checks) {
	for i := len(lines) - 1; i >= 0; i-- {
		if name, found := strings.CutPrefix(lines[i], newTestMarker); found {
			checks.NewTests = append([]string{name}, checks.NewTests...)
		} else if reason, found := strings.CutPrefix(lines[i], failedTestMarker); found {
			checks.Failed = append([]string{reason}, checks.Failed...)
		} else {
			return
		}
	}
}

// endMarker is the index of the last end marker line, or -1. It also records the last error line of the job in checks.
func endMarker(lines []string, checks *Checks) int {
	end := -1

	for i, line := range lines {
		if strings.HasPrefix(line, newTestsEnd) {
			end = i
		}

		if reason, found := strings.CutPrefix(line, newTestsError); found {
			checks.Problem = reason
		}
	}

	return end
}
