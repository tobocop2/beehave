package gate

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/config"
	"github.com/tobocop2/beehest/internal/exectest"
	"github.com/tobocop2/beehest/internal/model"
)

// tailBytes is the size of the log tail that the ssh host reads.
const tailBytes = 16384

const reportScript = `#!/bin/sh
mkdir -p reports
{ echo '<testsuites><testsuite>'
  while read -r kind cls name; do
    case $kind in
      pass) echo "<testcase classname=\"$cls\" name=\"$name\" time=\"0\"></testcase>";;
      skip) echo "<testcase classname=\"$cls\" name=\"$name\"><skipped message=\"s\"/></testcase>";;
      fail) echo "<testcase classname=\"$cls\" name=\"$name\"><failure message=\"f\">x</failure></testcase>";;
    esac
  done < tests.txt
  echo '</testsuite></testsuites>'; } > reports/j.xml
`

// shellRepo makes a repository on main with the given tests, then a branch with the head tests. It returns the repository and the head commit.
func shellRepo(t *testing.T, baseTests, headTests string) (repo, head string) {
	t.Helper()

	repo = t.TempDir()
	git := func(args ...string) string {
		out, err := exec.CommandContext(t.Context(), "git", append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...).CombinedOutput() //nolint:gosec // test helper
		require.NoError(t, err, string(out))

		return strings.TrimSpace(string(out))
	}

	require.NoError(t, os.WriteFile(filepath.Join(repo, "mkreport.sh"), []byte(reportScript), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "tests.txt"), []byte(baseTests), 0o600))
	git("init", "-q", "-b", "main")
	git("add", "mkreport.sh", "tests.txt")
	git("commit", "-q", "-m", "base")
	git("checkout", "-q", "-b", "feature")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "tests.txt"), []byte(headTests), 0o600))
	git("commit", "-q", "-am", "head")

	return repo, git("rev-parse", "HEAD")
}

// runCandidate runs the job of a candidate head in a real shell, with a stub gitleaks that exits with scan. The folder is the work root of the host.
func runCandidate(t *testing.T, root, repo, head string, scan int) string {
	t.Helper()

	return runCandidateOf(t, model.Project{Name: "demo", Gate: "sh mkreport.sh", TestReport: "reports/*.xml", TestCommand: "sh mkreport.sh"}, root, repo, head, scan)
}

// runCandidateOf is runCandidate for a project that the test chooses.
func runCandidateOf(t *testing.T, project model.Project, root, repo, head string, scan int) string {
	t.Helper()

	bin := filepath.Join(root, "bin")
	require.NoError(t, os.MkdirAll(bin, 0o750))

	// A negative scan runs the real gitleaks, which the PATH finds in its own folder.
	if scan < 0 {
		bin = gitleaksFolder(t)
	} else {
		require.NoError(t, exectest.Write(filepath.Join(bin, "gitleaks"), fmt.Sprintf("#!/bin/sh\nexit %d\n", scan)))
	}

	baseTip, err := exec.CommandContext(t.Context(), "git", "-C", repo, "rev-parse", "main").Output() //nolint:gosec // test helper
	require.NoError(t, err)

	command := hostCommand(project, nil, repo, head, root+"/beehest-gate-demo-"+head, "main", strings.TrimSpace(string(baseTip)))
	job := exec.CommandContext(t.Context(), "sh", "-c", command) //nolint:gosec // the job text under test
	job.Env = []string{"PATH=" + bin + string(os.PathListSeparator) + "/usr/bin:/bin", "HOME=" + t.TempDir()}

	// The job exits with the exit of the head gate, and the tests read the log, so the exit status is not the result here.
	out, err := job.CombinedOutput()
	if err != nil {
		var exit *exec.ExitError

		require.ErrorAs(t, err, &exit, string(out))
	}

	return string(out)
}

func TestBeforeReview_TheJobComparesTheHeadReportWithTheBaseReportPerTestAndCachesTheBase(t *testing.T) {
	t.Parallel()

	repo, head := shellRepo(t, "pass a TestOld\n", "pass a TestOld\npass a TestNew\npass a TestNew/case\nskip a TestSkipped\n")
	root := t.TempDir()

	out := runCandidate(t, root, repo, head, 0)
	checks := ReadChecks(out)

	require.True(t, checks.Complete, out)
	require.Equal(t, 3, checks.New)
	require.Equal(t, []string{"a TestNew", "a TestNew/case", "a TestSkipped"}, checks.NewTests)
	require.Equal(t, []string{"skipped a TestSkipped"}, checks.Failed)
	require.NotNil(t, checks.Secrets)
	require.Equal(t, 0, *checks.Secrets)
	require.Contains(t, out, stepMarker+" "+stepBaseSuite+" rc=0")

	again := runCandidate(t, root, repo, head, 0)

	require.NotContains(t, again, stepMarker+" "+stepBaseSuite, "the second job reads the cache")
	require.Equal(t, 3, ReadChecks(again).New)
}

func TestBeforeReview_AFailedSecretScanLeavesTheLaterStepsRunning(t *testing.T) {
	t.Parallel()

	repo, head := shellRepo(t, "pass a TestOld\n", "pass a TestOld\npass a TestNew\n")
	root := t.TempDir()

	checks := ReadChecks(runCandidate(t, root, repo, head, 1))

	require.True(t, checks.Complete)
	require.Equal(t, 1, checks.New)
	require.NotNil(t, checks.Secrets)
	require.Equal(t, 1, *checks.Secrets)
}

func TestBeforeReview_AHeadWithNoNewTestGivesAZeroCount(t *testing.T) {
	t.Parallel()

	repo, head := shellRepo(t, "pass a TestOld\n", "pass a TestOld\n# no change\n")
	root := t.TempDir()

	checks := ReadChecks(runCandidate(t, root, repo, head, 0))

	require.True(t, checks.Complete)
	require.Zero(t, checks.New)
}

func TestBeforeReview_TheJobExitsWithTheExitOfTheHeadGate(t *testing.T) {
	t.Parallel()

	project := model.Project{Name: "demo", Gate: "false", TestReport: "r/*.xml"}
	command := hostCommand(project, nil, "/nowhere", "abc", "/tmp/beehest-gate-demo-abc", "main", "0123456789abcdef0123456789abcdef01234567")

	require.Contains(t, command, `[ "$headrc" -eq 0 ]`)
	require.Contains(t, command, stepMarker+" "+stepHeadGate+" rc=$headrc")
	require.Contains(t, command, stepMarker+" "+stepSecrets+" rc=$secretrc")
}

func TestBeforeReview_ALogOverSixteenKiBBeforeTheEndMarkerStillReadsItsNameLines(t *testing.T) {
	t.Parallel()

	var log strings.Builder

	log.WriteString(strings.Repeat("noise noise noise noise\n", 1000))
	log.WriteString(newTestMarker + "pkg TestA/case\n" + newTestMarker + "pkg TestB\n" + newTestsEnd + "new=2 failed=0\n" + cloneMarker + "\n")

	checks := ReadChecks(log.String())

	require.True(t, checks.Complete)
	require.Equal(t, []string{"pkg TestA/case", "pkg TestB"}, checks.NewTests)
	require.Equal(t, 2, checks.New)
}

func TestBeforeReview_ABaseSuiteThatPrintsOverSixteenKiBLeavesEveryStepMarkerInTheTail(t *testing.T) {
	t.Parallel()

	repo, head := shellRepo(t, "pass a TestOld\n", "pass a TestOld\npass a TestNew\n")
	project := model.Project{
		Name: "demo", Gate: "sh mkreport.sh", TestReport: "reports/*.xml",
		TestCommand: "sh mkreport.sh; yes 'base suite output line that is long enough to fill the tail' | head -n 1000",
	}

	out := runCandidateOf(t, project, t.TempDir(), repo, head, 0)
	require.Greater(t, len(out), tailBytes, "the log is longer than the tail that the host reads")

	tail := out[len(out)-tailBytes:]
	codes := markerCodes(tail)
	checks := ReadChecks(tail)

	require.NotNil(t, checks.Secrets, "the secret scan marker is in the tail")
	require.Zero(t, *checks.Secrets)
	require.Equal(t, []string{"0"}, codes[stepHeadGate])
	require.Equal(t, []string{"0"}, codes[stepBaseSuite])
	require.True(t, checks.Complete)
	require.Equal(t, []string{"a TestNew"}, checks.NewTests)
}

func TestBeforeReview_ATestNameWithATabIsRefusedWithAClearError(t *testing.T) {
	t.Parallel()

	repo, head := shellRepo(t, "pass a TestOld\n", "pass a TestOld\npass a Test\tparam id\n")

	out := runCandidate(t, t.TempDir(), repo, head, 0)
	checks := ReadChecks(out)

	require.False(t, checks.Complete, "a name that cannot be compared gives no end marker, so the check is cut")
	require.Contains(t, checks.Problem, "holds a tab or a newline")
	require.Empty(t, checks.NewTests)
}

func TestBeforeReview_AHeadWithNoMergeBaseWithTheBaseCommitNamesTheBaseBranchAndTheCommit(t *testing.T) {
	t.Parallel()

	repo, head := shellRepo(t, "pass a TestOld\n", "pass a TestOld\npass a TestNew\n")
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	absent := strings.Repeat("3", 40)

	require.NoError(t, os.MkdirAll(bin, 0o750))
	require.NoError(t, exectest.Write(filepath.Join(bin, "gitleaks"), "#!/bin/sh\nexit 0\n"))

	project := model.Project{Name: "demo", Gate: "sh mkreport.sh", TestReport: "reports/*.xml", TestCommand: "sh mkreport.sh"}
	command := hostCommand(project, nil, repo, head, root+"/beehest-gate-demo-"+head, "main", absent)
	job := exec.CommandContext(t.Context(), "sh", "-c", command) //nolint:gosec // the job text under test
	job.Env = []string{"PATH=" + bin + string(os.PathListSeparator) + "/usr/bin:/bin", "HOME=" + t.TempDir()}

	out, err := job.CombinedOutput()
	if err != nil {
		var exit *exec.ExitError

		require.ErrorAs(t, err, &exit, string(out))
	}

	checks := ReadChecks(string(out))

	require.False(t, checks.Complete)
	require.Contains(t, checks.Problem, "the head has no merge base with the commit "+absent+" of the base branch main")
}

func TestBeforeReview_ABaseReportWithATabNameIsNotCachedSoNoLaterRunReadsAPartialList(t *testing.T) {
	t.Parallel()

	repo, head := shellRepo(t, "pass a TestOne\npass a Test\tparam id\npass a TestTwo\n", "pass a TestOne\npass a TestTwo\npass a TestNew\n")
	root := t.TempDir()

	first := ReadChecks(runCandidate(t, root, repo, head, 0))

	require.False(t, first.Complete)
	require.Contains(t, first.Problem, "no base report")

	entries, err := os.ReadDir(filepath.Join(root, baseCache))
	require.NoError(t, err)

	for _, entry := range entries {
		require.True(t, strings.HasPrefix(entry.Name(), ".tmp."), "the cache holds no report for the base: %s", entry.Name())
	}

	second := ReadChecks(runCandidate(t, root, repo, head, 0))

	require.False(t, second.Complete, "the second run did not read a partial cache")
	require.Contains(t, second.Problem, "no base report")
}

func TestBeforeReview_AJUnitNameWithANewlineMakesTheReaderExitWithTheBadNameCode(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "j.xml"), []byte("<testsuite><testcase classname=\"a\" name=\"T\nx\"></testcase><testcase classname=\"a\" name=\"Ok\"></testcase></testsuite>\n"), 0o600))

	cmd := exec.CommandContext(t.Context(), "awk", junitAwk, filepath.Join(dir, "j.xml")) //nolint:gosec // the reader under test
	out, err := cmd.Output()

	var exit *exec.ExitError

	require.ErrorAs(t, err, &exit)
	require.Equal(t, 3, exit.ExitCode())
	require.Equal(t, "a\tOk\tpass\n", string(out), "the good test stays and the bad name is dropped")
}

func TestBeforeReview_ALogWithNoEndMarkerIsACutLog(t *testing.T) {
	t.Parallel()

	checks := ReadChecks(newTestMarker + "pkg TestA\nnoise\n")

	require.False(t, checks.Complete)
	require.Empty(t, checks.NewTests)
}

func TestBeforeReview_ATestThatPrintsTheEndMarkerEarlierChangesNothing(t *testing.T) {
	t.Parallel()

	log := newTestsEnd + "new=99 failed=0\n" + "noise\n" + newTestMarker + "pkg TestA\n" + newTestsEnd + "new=1 failed=0\n"

	checks := ReadChecks(log)

	require.Equal(t, 1, checks.New)
	require.Equal(t, []string{"pkg TestA"}, checks.NewTests)
}

func TestBeforeReview_TheEndMarkerCarriesTheFullCountPastTheNameLineCap(t *testing.T) {
	t.Parallel()

	repo, head := shellRepo(t, "pass a TestOld\n", "pass a TestOld\n"+manyTests(75))
	root := t.TempDir()

	checks := ReadChecks(runCandidate(t, root, repo, head, 0))

	require.Equal(t, 75, checks.New)
	require.Len(t, checks.NewTests, markerCap)
}

// gitleaksFolder is the folder of the real gitleaks. A machine that has none fails the test when CI is set, and skips it anywhere else.
func gitleaksFolder(t *testing.T) string {
	t.Helper()

	found, err := exec.LookPath("gitleaks")
	if err != nil {
		if config.ProcessEnv()["CI"] != "" {
			t.Fatal("gitleaks is not installed and CI is set. The planted-key test never skips in CI. Run make setup")
		}

		t.Skip("gitleaks is not installed. Run make setup")
	}

	return filepath.Dir(found)
}

// plantedKey is a key shape that gitleaks reads as a cloud access key. The parts join at run time, so no file of the repository holds the whole key.
func plantedKey() string {
	return "AKI" + "A" + "XQJZ5MNP" + "W3LK7RTV"
}

// commitFile commits a file with the text on the branch that is checked out, and returns the new head.
func commitFile(t *testing.T, repo, name, text string) string {
	t.Helper()

	require.NoError(t, os.WriteFile(filepath.Join(repo, name), []byte(text), 0o600))

	for _, args := range [][]string{{"add", name}, {"commit", "-q", "-m", "add " + name}} {
		out, err := exec.CommandContext(t.Context(), "git", append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...).CombinedOutput() //nolint:gosec // test helper
		require.NoError(t, err, string(out))
	}

	out, err := exec.CommandContext(t.Context(), "git", "-C", repo, "rev-parse", "HEAD").Output() //nolint:gosec // test helper
	require.NoError(t, err)

	return strings.TrimSpace(string(out))
}

func TestBeforeReview_ASecretScanFailsAPlantedKeyInTheCandidateDiffAndPassesACleanOne(t *testing.T) {
	t.Parallel()

	gitleaksFolder(t)

	cleanRepo, _ := shellRepo(t, "pass a TestOld\n", "pass a TestOld\npass a TestNew\n")
	cleanHead := commitFile(t, cleanRepo, "notes.env", "greeting = hello\n")
	clean := ReadChecks(runCandidate(t, t.TempDir(), cleanRepo, cleanHead, -1))

	plantedRepo, _ := shellRepo(t, "pass a TestOld\n", "pass a TestOld\npass a TestNew\n")
	plantedHead := commitFile(t, plantedRepo, "settings.env", "aws_access_key_id = "+plantedKey()+"\n")
	planted := ReadChecks(runCandidate(t, t.TempDir(), plantedRepo, plantedHead, -1))

	require.NotNil(t, clean.Secrets)
	require.Zero(t, *clean.Secrets, "the control: a diff with no key passes the scan")
	require.NotNil(t, planted.Secrets)
	require.NotZero(t, *planted.Secrets, "the planted key in the diff fails the scan")
	require.True(t, planted.Complete, "the failed scan leaves the later steps running")
	require.Equal(t, 1, planted.New)
}

func manyTests(n int) string {
	var out strings.Builder

	for i := range n {
		fmt.Fprintf(&out, "pass a TestN%d\n", i)
	}

	return out.String()
}
