package gate

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/exectest"
	"github.com/tobocop2/beehest/internal/model"
)

// codeScript is the test suite of a revert repository. code.txt is the code: one line "<function> <value>" each. A test file under tests/ holds
// one line "<class> <name> <function> <value>" for each test, which passes when code.txt has that line and is skipped when the value there is "skip".
// A test that calls a function code.txt lacks does not build: the suite then writes the JUnit report of a build failure from buildfail.xml.
const codeScript = `#!/bin/sh
mkdir -p reports
cat tests/*.test > all.tsv
while read -r cls name fn want; do
  grep -q "^$fn " code.txt || { cp buildfail.xml reports/j.xml; exit 1; }
done < all.tsv
{ echo '<testsuites><testsuite>'
  while read -r cls name fn want; do
    got=$(grep "^$fn " code.txt | head -1 | cut -d' ' -f2)
    if [ "$got" = skip ]; then echo "<testcase classname=\"$cls\" name=\"$name\"><skipped message=\"s\"/></testcase>"
    elif [ "$got" = "$want" ]; then echo "<testcase classname=\"$cls\" name=\"$name\"></testcase>"
    else echo "<testcase classname=\"$cls\" name=\"$name\"><failure message=\"f\">x</failure></testcase>"; fi
  done < all.tsv
  echo '</testsuite></testsuites>'; } > reports/j.xml
! grep -q '<failure' reports/j.xml
`

// revertProject is the project of a revert repository: the gate and the test command are its suite, and the test files are under tests/.
func revertProject() model.Project {
	return model.Project{Name: "demo", Gate: "sh suite.sh", TestReport: "reports/*.xml", TestCommand: "sh suite.sh", TestPaths: []string{"tests/**"}}
}

// revertRepo makes a repository on main with the base code and the base tests, then a branch whose commit has the head code and the head tests.
func revertRepo(t *testing.T, baseCode, baseTests, headCode, headTests string) (repo, head string) {
	t.Helper()

	repo = t.TempDir()
	fixture, err := os.ReadFile(filepath.Join("testdata", "junit-buildfail.xml"))
	require.NoError(t, err)

	write := func(name, text string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(repo, name)), 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(repo, name), []byte(text), 0o600)) //nolint:gosec // a fixed name in a test folder
	}

	write("suite.sh", codeScript)
	write("buildfail.xml", string(fixture))
	write(".gitignore", "reports/\nall.tsv\n")
	write("code.txt", baseCode)
	write("tests/calc.test", baseTests)
	gitRun(t, repo, "init", "-q", "-b", "main")
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-q", "-m", "base")
	gitRun(t, repo, "checkout", "-q", "-b", "feature")
	write("code.txt", headCode)
	write("tests/calc.test", headTests)
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-q", "-m", "head")

	return repo, gitRun(t, repo, "rev-parse", "HEAD")
}

// gitRun runs git in the repository and returns its trimmed output.
func gitRun(t *testing.T, repo string, args ...string) string {
	t.Helper()

	out, err := exec.CommandContext(t.Context(), "git", append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...).CombinedOutput() //nolint:gosec // test helper
	require.NoError(t, err, string(out))

	return strings.TrimSpace(string(out))
}

const baseCode = "add 3\ndouble 5\nenv skip\n"

const baseTests = "calc TestAdd add 3\n"

func TestBeforeReview_TheRevertArmRefusesANewTestThatPassesOnTheBase(t *testing.T) {
	t.Parallel()

	repo, head := revertRepo(t, baseCode, baseTests, baseCode, baseTests+"calc TestAddAgain add 3\n")

	out := runCandidateOf(t, revertProject(), t.TempDir(), repo, head, 0)
	checks := ReadChecks(out)

	require.Equal(t, 1, checks.New, out)
	require.Equal(t, model.RevertFail, checks.Revert, out)
	require.Equal(t, []string{"calc TestAddAgain"}, checks.RevertPassed)
	require.Equal(t, 1, checks.RevertPassedCount)
}

func TestBeforeReview_TheRevertArmRefusesANewTestThatIsSkippedOnTheBase(t *testing.T) {
	t.Parallel()

	repo, head := revertRepo(t, baseCode, baseTests, "add 3\ndouble 5\nenv 1\n", baseTests+"calc TestEnv env 1\n")

	out := runCandidateOf(t, revertProject(), t.TempDir(), repo, head, 0)
	checks := ReadChecks(out)

	require.Equal(t, model.RevertFail, checks.Revert, out)
	require.Equal(t, []string{"calc TestEnv"}, checks.RevertPassed, "a skip on the base proves nothing about the change")
}

func TestBeforeReview_TheRevertArmPassesANewTestThatFailsOnTheBase(t *testing.T) {
	t.Parallel()

	repo, head := revertRepo(t, baseCode, baseTests, "add 3\ndouble 4\nenv skip\n", baseTests+"calc TestDouble double 4\n")

	out := runCandidateOf(t, revertProject(), t.TempDir(), repo, head, 0)
	checks := ReadChecks(out)

	require.Equal(t, 1, checks.New, out)
	require.Equal(t, model.RevertPass, checks.Revert, out)
	require.Empty(t, checks.RevertPassed)
	require.Equal(t, 2, checks.RevertCases, "the base run reports the old test and the new one")
}

func TestBeforeReview_TheRevertArmPassesANewTestWhosePackageDoesNotBuildOnTheBase(t *testing.T) {
	t.Parallel()

	repo, head := revertRepo(t, baseCode, baseTests, baseCode+"triple 9\n", baseTests+"calc TestTriple triple 9\n")

	out := runCandidateOf(t, revertProject(), t.TempDir(), repo, head, 0)
	checks := ReadChecks(out)

	require.Equal(t, 1, checks.New, out)
	require.Equal(t, model.RevertPass, checks.Revert, out)
	require.Equal(t, 1, checks.RevertCases, "the report of a build failure holds one failed case and no new test name")
}

func TestBeforeReview_TheRevertArmPassesAHeadTestThatCallsAFunctionTheBaseLacksInARealShell(t *testing.T) {
	t.Parallel()

	repo, head := revertRepo(t, baseCode, baseTests, baseCode+"triple 9\n", baseTests+"calc TestTriple triple 9\ncalc TestTripleAgain triple 9\n")
	root := t.TempDir()

	out := runCandidateOf(t, revertProject(), root, repo, head, 0)
	checks := ReadChecks(out)

	// The head report holds three passing cases. A reader of the head clone would see them and refuse both new tests.
	require.Equal(t, 2, checks.New, out)
	require.Equal(t, model.RevertPass, checks.Revert, out)
	require.Equal(t, 1, checks.RevertCases, "the revert run reads the reports of the base folder only")
	require.Contains(t, out, stepMarker+" "+model.StepRevertRun+" rc=1")
}

func TestBeforeReview_ARevertRunThatFailsBesideAPassingHeadGateRecordsBothExitsAndPasses(t *testing.T) {
	t.Parallel()

	repo, head := revertRepo(t, baseCode, baseTests, "add 3\ndouble 4\nenv skip\n", baseTests+"calc TestDouble double 4\n")
	bin := t.TempDir()
	require.NoError(t, exectest.Write(filepath.Join(bin, "gitleaks"), "#!/bin/sh\nexit 0\n"))

	baseTip, err := exec.CommandContext(t.Context(), "git", "-C", repo, "rev-parse", "main").Output() //nolint:gosec // test helper
	require.NoError(t, err)

	command := hostCommand(revertProject(), nil, repo, head, t.TempDir()+"/beehest-gate-demo-"+head, "main", strings.TrimSpace(string(baseTip)))
	job := exec.CommandContext(t.Context(), "sh", "-c", command) //nolint:gosec // the job text under test
	job.Env = []string{"PATH=" + bin + string(os.PathListSeparator) + "/usr/bin:/bin", "HOME=" + t.TempDir()}
	out, err := job.CombinedOutput()

	require.NoError(t, err, "the job exits with the head gate, which passed: %s", out)
	require.Contains(t, string(out), stepMarker+" "+stepHeadGate+" rc=0")

	checks := ReadChecks(string(out))

	require.NotNil(t, checks.RevertRC, string(out))
	require.Equal(t, 1, *checks.RevertRC)
	require.Equal(t, model.RevertPass, checks.Revert)
}

func TestBeforeReview_ARevertReportWithNoTestcaseIsAToolError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "new.tsv"), []byte("calc\tTestDouble\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "revert.tsv"), nil, 0o600))

	cmd := exec.CommandContext(t.Context(), "awk", "-F", "\t", "-v", fmt.Sprintf("cap=%d", markerCap), "-v", fmt.Sprintf("room=%d", nameRoom), revertAwk, filepath.Join(dir, "new.tsv"), filepath.Join(dir, "revert.tsv")) //nolint:gosec // the reader under test
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))

	checks := ReadChecks(string(out))

	require.Equal(t, model.RevertFail, checks.Revert, string(out))
	require.Zero(t, checks.RevertCases)
	require.Contains(t, checks.RevertProblem, "no test case")
}

func TestBeforeReview_AHeadWithNewTestsButNoTestPathsOrNoTestCommandHasNoRevertRunAndFails(t *testing.T) {
	t.Parallel()

	for name, unset := range map[string]func(*model.Project){
		"no_test_paths":   func(project *model.Project) { project.TestPaths = nil },
		"no_test_command": func(project *model.Project) { project.TestCommand = "" },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			repo, head := revertRepo(t, baseCode, baseTests, "add 3\ndouble 4\nenv skip\n", baseTests+"calc TestDouble double 4\n")
			root := t.TempDir()

			// A first job caches the report of the base. Without test_command the job reads that cache, so it still finds the new test.
			runCandidateOf(t, revertProject(), root, repo, head, 0)

			project := revertProject()
			unset(&project)

			out := runCandidateOf(t, project, root, repo, head, 0)
			checks := ReadChecks(out)

			require.Equal(t, 1, checks.New, out)
			require.Equal(t, model.RevertFail, checks.Revert)
			require.Contains(t, checks.RevertProblem, "the project sets no test_command or no test_paths")
			require.NotContains(t, out, stepMarker+" "+model.StepRevertRun)
		})
	}
}

func TestBeforeReview_ARevertEndLineThatTheHeadGatePrintsCannotPassARunThatDidNotRun(t *testing.T) {
	t.Parallel()

	forged := "echo '" + revertEnd + "new=1 passed=0 cases=9' && "

	for name, tc := range map[string]struct {
		paths     []string
		headCode  string
		headTests string
	}{
		"no_test_paths": {nil, "add 3\ndouble 4\nenv skip\n", baseTests + "calc TestDouble double 4\n"},
		"no_new_test":   {[]string{"tests/**"}, baseCode + "# no change\n", baseTests},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			repo, head := revertRepo(t, baseCode, baseTests, tc.headCode, tc.headTests)
			project := revertProject()
			project.TestPaths, project.Gate = tc.paths, forged+project.Gate

			out := runCandidateOf(t, project, t.TempDir(), repo, head, 0)
			checks := ReadChecks(out)

			require.Contains(t, out, revertEnd+"new=1 passed=0 cases=9", "the head gate printed the end line")
			require.Equal(t, model.RevertFail, checks.Revert, out)
			require.NotEmpty(t, checks.RevertProblem)
		})
	}
}

func TestBeforeReview_AHeadWithNoNewTestHasNoRevertRun(t *testing.T) {
	t.Parallel()

	repo, head := revertRepo(t, baseCode, baseTests, baseCode+"# no change\n", baseTests)

	out := runCandidateOf(t, revertProject(), t.TempDir(), repo, head, 0)
	checks := ReadChecks(out)

	require.True(t, checks.Complete, out)
	require.Zero(t, checks.New)
	require.Equal(t, model.RevertFail, checks.Revert)
	require.NotContains(t, out, stepMarker+" "+model.StepRevertRun)
}

func TestBeforeReview_TheEndMarkersStayInTheLogTailWhenEveryNameIsLong(t *testing.T) {
	t.Parallel()

	// Each new test is skipped on the head and on the base, so it prints a name line, a failed line and a revert line, each of about 1 KiB.
	class := strings.Repeat("c", 200)

	var tests strings.Builder

	tests.WriteString(baseTests)

	for i := range 60 {
		fmt.Fprintf(&tests, "%s Test%s%02d env 1\n", class, strings.Repeat("n", 800), i)
	}

	repo, head := revertRepo(t, baseCode, baseTests, baseCode, tests.String())
	project := revertProject()
	project.Gate = "awk 'BEGIN { for (i = 0; i < 400; i++) print \"gate output line\", i }' && " + project.Gate
	out := runCandidateOf(t, project, t.TempDir(), repo, head, 0)
	require.Greater(t, len(out), tailBytes, "the log is longer than the tail that the host reads")

	tail := out[len(out)-tailBytes:]
	checks := ReadChecks(tail)

	require.True(t, checks.Complete, "the new-tests end marker is in the tail")
	require.Equal(t, []int{60, 60, 60}, []int{checks.New, checks.FailedCount, checks.RevertPassedCount}, "the end markers carry the full counts")
	require.Equal(t, model.RevertFail, checks.Revert)
	require.NotEmpty(t, checks.RevertPassed)
	require.NotNil(t, checks.Secrets, "the step markers are in the tail")
	require.NotNil(t, checks.RevertRC, "the step markers are in the tail")

	for _, name := range append(append([]string{}, checks.NewTests...), checks.RevertPassed...) {
		require.Len(t, name, len(class)+len(" Test")+800+2, "every name line in the tail is whole")
	}
}

// twoByteRune is a letter that UTF-8 writes in two bytes.
const twoByteRune = "é"

func TestBeforeReview_TheByteRoomOfTheNameLinesCountsBytesInAUTF8Locale(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	var wanted, report strings.Builder

	for i := range 60 {
		name := fmt.Sprintf("Test%s%02d", strings.Repeat(twoByteRune, 100), i)
		fmt.Fprintf(&wanted, "calc\t%s\n", name)
		fmt.Fprintf(&report, "calc\t%s\tpass\n", name)
	}

	require.NoError(t, os.WriteFile(filepath.Join(dir, "new.tsv"), []byte(wanted.String()), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "revert.tsv"), []byte(report.String()), 0o600))

	cmd := exec.CommandContext(t.Context(), "sh", "-c", cappedAwk()+model.ShellQuote(revertAwk)+" new.tsv revert.tsv") //nolint:gosec // the word under test
	cmd.Dir, cmd.Env = dir, []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8", "LC_ALL="}
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))

	names := strings.TrimSuffix(string(out), "\n")
	names = names[:strings.LastIndex(names, "\n")+1]

	require.NotEmpty(t, names)
	require.LessOrEqual(t, len(names), nameRoom, "the passed lines fit in the room in bytes, not in letters")
	require.Contains(t, string(out), revertEnd+"new=60 passed=60 cases=60")
}

func TestBeforeReview_TheRevertMarkersCapTheNamesAndTheEndMarkerCountsThemAll(t *testing.T) {
	t.Parallel()

	var log strings.Builder

	for range markerCap {
		log.WriteString(revertPassedMarker + "pkg TestX\n")
	}

	log.WriteString(revertEnd + "new=60 passed=60 cases=70\n")

	checks := ReadChecks(log.String())

	require.Len(t, checks.RevertPassed, markerCap)
	require.Equal(t, 60, checks.RevertPassedCount)
	require.Equal(t, 70, checks.RevertCases)
	require.Equal(t, model.RevertFail, checks.Revert)
}
