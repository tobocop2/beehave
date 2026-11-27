package flow

import (
	"bytes"
	"errors"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	gatepkg "github.com/tobocop2/beehest/internal/gate"
	"github.com/tobocop2/beehest/internal/host/hosttest"
	"github.com/tobocop2/beehest/internal/host/ssh"
	"github.com/tobocop2/beehest/internal/host/sshtest"
	"github.com/tobocop2/beehest/internal/model"
	"github.com/tobocop2/beehest/internal/proc"
)

// tailBytes is the size of the log tail that the ssh host reads.
const tailBytes = 16384

// revertClean is the end marker of a revert run in which every new test failed on the base code.
const revertClean = "beehest-revert-end new=2 passed=0 cases=3"

// reviewTree is an item folder with a base commit, an unrelated commit on another branch, and a head one commit above the base.
type reviewTree struct {
	dir, base, other, head string
}

// reviewTreeOf makes the folder. The head commit adds the file with the message.
func reviewTreeOf(t *testing.T, file, message string) reviewTree {
	t.Helper()

	dir, base := gitTree(t)
	gitIn(t, dir, "checkout", "-q", "-b", "elsewhere")
	putUnder(t, dir, "elsewhere.txt", "x")
	gitIn(t, dir, "add", "elsewhere.txt")
	gitIn(t, dir, "commit", "-q", "-m", "elsewhere")

	other := gitIn(t, dir, "rev-parse", "HEAD")

	gitIn(t, dir, "checkout", "-q", "-b", "work", base)
	putUnder(t, dir, file, "x")
	gitIn(t, dir, "add", file)
	gitIn(t, dir, "commit", "-q", "-m", message)

	return reviewTree{dir: dir, base: base, other: other, head: gitIn(t, dir, "rev-parse", "HEAD")}
}

// cleanChecks are the checks of a gate job that found one new test and a clean secret scan.
func cleanChecks() *GateChecks {
	zero := 0

	return &GateChecks{NewTests: []string{"pkg TestA"}, New: 1, Complete: true, Secrets: &zero, Revert: model.RevertPass, RevertCases: 1}
}

// reviewHarness gates the head of the tree with the checks, and reads the item folder from the tree.
func reviewHarness(t *testing.T, tree reviewTree, checks *GateChecks) *harness {
	t.Helper()

	h := reuseHarness(t, []GateResult{{Passed: true, Head: tree.head, Tree: "tree-1", Steps: stepsPass, Checks: checks}}, nil)
	h.trees = &fakeTrees{dir: tree.dir}
	h.facts = func(string) model.Project { return model.Project{Source: tree.dir} }

	return h
}

// jobLog is the log of a candidate job: a head gate that printed a lot, the two step markers, the name lines and marker lines of the check, and the end marker.
func jobLog(noise int, secretsRC string, lines ...string) string {
	return strings.Repeat("go test output line of the head gate\n", noise) +
		"beehest-gate-step head-gate rc=0\n" + "beehest-gate-step secret-scan rc=" + secretsRC + "\n" + strings.Join(lines, "\n") + "\n"
}

// logChecks gives the log to the fake machine as the log of a submitted job, reads it back through the ssh host, and returns the length that the host read
// and the checks that the engine reads from it.
func logChecks(t *testing.T, log string) (read int, checks GateChecks) {
	t.Helper()

	m := sshtest.New(hosttest.Working)
	n := 0

	h, err := ssh.New("gates", model.HostConfig{
		Provider: ssh.Name, MaxRun: 2 * time.Hour, StaleMinutes: 20, PrivateRemote: "private",
		Block: model.NewBlock("test", []model.BlockEntry{
			model.TextEntry(ssh.BlockUser, "lane"), model.TextEntry(ssh.BlockWorkRoot, sshtest.Root+"/work"), model.TextEntry(ssh.BlockKnownHosts, model.KnownHostsStrict),
		}),
	}, m.SSH(), ssh.WithNonce(func() string { n++; return "n" + strconv.Itoa(n) }))
	require.NoError(t, err)

	id, err := h.Submit(t.Context(), model.JobSpec{Name: "gate", Command: "x"})
	require.NoError(t, err)

	m.Units[string(id)].Log = log

	tail, err := h.Log(t.Context(), id)
	require.NoError(t, err)

	got := gatepkg.ReadChecks(tail)
	checks = GateChecks{
		NewTests: got.NewTests, Failed: got.Failed, New: got.New, FailedCount: got.FailedCount, Secrets: got.Secrets, Complete: got.Complete, Problem: got.Problem,
		Revert: got.Revert, RevertPassed: got.RevertPassed, RevertPassedCount: got.RevertPassedCount, RevertCases: got.RevertCases, RevertProblem: got.RevertProblem,
	}

	return len(tail), checks
}

// gitReply answers each command of git with the real git in the folder, and any other command with an empty output.
func gitReply(t *testing.T, dir string) func(CommandRequest) (CommandOutput, error) {
	t.Helper()

	return func(request CommandRequest) (CommandOutput, error) {
		if request.Run[0] != "git" {
			return CommandOutput{}, nil
		}

		cmd := proc.GroupCommand(t.Context(), "git", request.Run[1:], dir)
		cmd.Env = append(cmd.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")

		var stdout, stderr bytes.Buffer

		cmd.Stdout, cmd.Stderr = &stdout, &stderr

		err := cmd.Run()

		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return CommandOutput{Stdout: stdout.String(), Stderr: stderr.String(), RC: exit.ExitCode()}, nil
		}

		return CommandOutput{Stdout: stdout.String(), Stderr: stderr.String()}, err
	}
}

// libReviewFlow gates the head, judges it with the function of the library and shows ok and the findings.
func libReviewFlow(base, extra string) string {
	return "load(\"@beehest//review_fix.star\", \"before_review\")\n\n" + bodyFlow(
		"g = gate(name = \"g\", base = \"main\")\n"+
			"r = before_review(g, base = \""+base+"\", "+extra+"name = \"r\")\n"+
			"command(run = [\"show\", str(r.ok), \"|\".join(r.findings)])\n")
}

// reviewShown runs the flow with real git in the folder of the tree and returns the line that the flow showed.
func reviewShown(t *testing.T, tree reviewTree, checks *GateChecks, base, extra string) (string, error) {
	t.Helper()

	h := reviewHarness(t, tree, checks)
	h.commands.reply = gitReply(t, tree.dir)

	_, err := walkText(t, h, libReviewFlow(base, extra))

	for _, line := range h.commands.ran() {
		if strings.HasPrefix(line, "show ") {
			return line, err
		}
	}

	return "", err
}

func TestReviewFixBeforeReview_ACleanHeadIsOk(t *testing.T) {
	t.Parallel()

	tree := reviewTreeOf(t, "src/a.go", "add a")

	shown, err := reviewShown(t, tree, cleanChecks(), tree.base, `allow = ["src/**"], `)

	require.NoError(t, err)
	assert.Equal(t, "show True ", shown)
}

func TestReviewFixBeforeReview_AHeadThatIsNotADescendantOfTheBaseIsRefused(t *testing.T) {
	t.Parallel()

	tree := reviewTreeOf(t, "src/a.go", "add a")

	shown, err := reviewShown(t, tree, cleanChecks(), tree.other, "")

	require.NoError(t, err)
	assert.Contains(t, shown, "show False")
	assert.Contains(t, shown, "is not a descendant of the base "+tree.other)
}

func TestReviewFixBeforeReview_AChangedPathOutsideTheAllowedPathsIsARefusal(t *testing.T) {
	t.Parallel()

	tree := reviewTreeOf(t, "docs/a.md", "add a")

	shown, err := reviewShown(t, tree, cleanChecks(), tree.base, `allow = ["src/**", "./pkg/*.go"], `)

	require.NoError(t, err)
	assert.Contains(t, shown, "show False")
	assert.Contains(t, shown, "1 path(s) outside allow [src/**, ./pkg/*.go]: docs/a.md")
}

func TestReviewFixBeforeReview_APathInsideAnAllowedGlobIsNotAFinding(t *testing.T) {
	t.Parallel()

	tree := reviewTreeOf(t, "pkg/a.go", "add a")

	shown, err := reviewShown(t, tree, cleanChecks(), tree.base, `allow = ["./pkg/*.go"], `)

	require.NoError(t, err)
	assert.Equal(t, "show True ", shown, "the leading ./ of a glob is dropped, as for allow_paths")
}

func TestReviewFixBeforeReview_NoAllowAllowsEveryPath(t *testing.T) {
	t.Parallel()

	tree := reviewTreeOf(t, "docs/a.md", "add a")
	h := reviewHarness(t, tree, cleanChecks())
	h.commands.reply = gitReply(t, tree.dir)

	_, err := walkText(t, h, libReviewFlow(tree.base, ""))

	require.NoError(t, err)

	for _, line := range h.commands.ran() {
		assert.NotContains(t, line, "git diff", "a call with no allow lists no path")
	}
}

func TestReviewFixBeforeReview_AClosingKeywordInACommitIsARefusalThatNamesTheCommit(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		message string
		issue   string
	}{
		"fixes_number":            {"add a\n\nFixes #12", "#12"},
		"closes_cross_repo":       {"add a\n\ncloses owner/repo#3", "owner/repo#3"},
		"resolved_colon":          {"add a\n\nResolved: #7", "#7"},
		"issue_url":               {"add a\n\nfix https://github.com/owner/repo/issues/9", "owner/repo#9"},
		"keyword_in_header":       {"fix #5", "#5"},
		"number_on_the_next_line": {"add a\n\nCloses\n#12", "#12"},
		"plain_number":            {"add a\n\nsee #12", ""},
		"word_ends_in_fix":        {"add a\n\nsuffix #12", ""},
		"keyword_no_issue":        {"add a\n\nfixes the build", ""},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			tree := reviewTreeOf(t, "src/a.go", c.message)

			shown, err := reviewShown(t, tree, cleanChecks(), tree.base, "")

			require.NoError(t, err)

			if c.issue != "" {
				assert.Contains(t, shown, "show False")
				assert.Contains(t, shown, "the commit "+tree.head[:7]+" (")
				assert.Contains(t, shown, "closes "+c.issue+" with a closing keyword. Only the integration pull request may close an issue")
			} else {
				assert.Equal(t, "show True ", shown)
			}
		})
	}
}

func TestReviewFixBeforeReview_ACandidateWithNoNewTestOrASkippedNewTestOrACutLogOrADirtySecretScanIsRefused(t *testing.T) {
	t.Parallel()

	one, none := 1, 0
	cases := map[string]struct {
		checks *GateChecks
		finds  string
	}{
		"zero new tests": {&GateChecks{Complete: true, Secrets: &none}, "adds no new test"},
		"skipped new test": {
			&GateChecks{NewTests: []string{"pkg TestA"}, New: 1, Failed: []string{"skipped pkg TestA"}, FailedCount: 1, Complete: true, Secrets: &none},
			"a new test failed a check: skipped pkg TestA",
		},
		"cut log":           {&GateChecks{Secrets: &none, Problem: "no base report"}, "the new-tests check is cut and proves nothing: no base report"},
		"gate with no base": {nil, "no end marker"},
		"secret found":      {&GateChecks{NewTests: []string{"pkg TestA"}, New: 1, Complete: true, Secrets: &one}, "secret scan of the head exited with code 1"},
		"no scan code":      {&GateChecks{NewTests: []string{"pkg TestA"}, New: 1, Complete: true}, "no exit code of the secret scan"},
		"more failed than named": {
			&GateChecks{NewTests: []string{"pkg TestA"}, New: 1, Failed: []string{"skipped pkg TestA"}, FailedCount: 3, Complete: true, Secrets: &none, Revert: model.RevertPass, RevertCases: 1},
			"3 new tests failed a check in all, and the log names the first 1",
		},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			tree := reviewTreeOf(t, "src/a.go", "add a")

			shown, err := reviewShown(t, tree, c.checks, tree.base, "")

			require.NoError(t, err)
			assert.Contains(t, shown, "show False")
			assert.Contains(t, shown, c.finds, name)
		})
	}
}

func TestReviewFixBeforeReview_ASubtestCountsAsOneNewTest(t *testing.T) {
	t.Parallel()

	tree := reviewTreeOf(t, "src/a.go", "add a")
	zero := 0

	shown, err := reviewShown(t, tree, &GateChecks{NewTests: []string{"pkg TestA/case"}, New: 1, Complete: true, Secrets: &zero, Revert: model.RevertPass, RevertCases: 1}, tree.base, "")

	require.NoError(t, err)
	assert.Equal(t, "show True ", shown)
}

func TestReviewFixBeforeReview_TheFunctionJudgesTheGateResultItGetsWhateverGateCameAfterIt(t *testing.T) {
	t.Parallel()

	tree := reviewTreeOf(t, "src/a.go", "add a")
	zero := 0
	h := reuseHarness(t, []GateResult{
		{Passed: true, Head: tree.head, Tree: "tree-1", Steps: stepsPass, Checks: cleanChecks()},
		{Passed: true, Head: tree.other, Tree: "tree-2", Steps: stepsPass, Checks: &GateChecks{Complete: true, Secrets: &zero}},
	}, nil)
	h.trees = &fakeTrees{dir: tree.dir}
	h.facts = func(string) model.Project { return model.Project{Source: tree.dir} }
	h.commands.reply = gitReply(t, tree.dir)

	_, err := walkText(t, h, "load(\"@beehest//review_fix.star\", \"before_review\")\n\n"+bodyFlow(
		"g1 = gate(name = \"g1\", base = \"main\")\n"+
			"g2 = gate(name = \"g2\", base = \"main\")\n"+
			"r = before_review(g1, base = \""+tree.base+"\", name = \"r\")\n"+
			"command(run = [\"show\", str(r.ok)])\n"))

	require.NoError(t, err)
	assert.Contains(t, h.commands.ran(), "show True", "the second gate has no new test, and the first one is judged")
}

func TestReviewFixBeforeReview_AGitCommandThatFailsIsAnErrorAndNotARefusal(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		config, base, extra, want string
	}{
		"base git cannot find": {"", "no-such-base", "", "could not run git merge-base --is-ancestor no-such-base"},
		// git diff reads diff.context and git merge-base does not, so the descent check passes and the path list fails.
		"path list": {"diff.context", "", `allow = ["src/**"], `, "could not run git diff"},
		// git log reads log.abbrevCommit and git merge-base does not, so the descent check passes and the message read fails.
		"commit log": {"log.abbrevCommit", "", "", "could not run git log"},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			tree := reviewTreeOf(t, "src/a.go", "add a")
			if c.config != "" {
				gitIn(t, tree.dir, "config", c.config, "not-valid")
			}

			base := c.base
			if base == "" {
				base = tree.base
			}

			shown, err := reviewShown(t, tree, cleanChecks(), base, c.extra)

			require.ErrorContains(t, err, c.want)
			assert.Empty(t, shown, "a failed git command refuses nothing: the item stops")
		})
	}
}

func TestReviewFixBeforeReview_ANewTestThatPassesOnTheBaseIsAFindingThatNamesIt(t *testing.T) {
	t.Parallel()

	tree := reviewTreeOf(t, "src/a.go", "add a")

	shown, err := reviewShown(t, tree, revertChecks(3, "pkg TestA"), tree.base, "")

	require.NoError(t, err)
	assert.Contains(t, shown, "show False")
	assert.Contains(t, shown, "the new test pkg TestA passes or is skipped on the base code. A test must fail without the change")
	assert.Contains(t, shown, "3 new tests pass or are skipped on the base code in all, and the log names the first 1")
}

func TestReviewFixBeforeReview_ARevertRunThatProvesNothingIsAFindingWithItsReason(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		problem, finds string
	}{
		"no test case":  {"the report of the revert run holds no test case, so the run proves nothing", "holds no test case"},
		"no test_paths": {"the project sets no test_command or no test_paths, so the new tests cannot run on the base code", "no test_paths"},
		"no end marker": {"", "no end marker of the revert run"},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			tree := reviewTreeOf(t, "src/a.go", "add a")
			checks := revertChecks(0)
			checks.RevertProblem = c.problem

			shown, err := reviewShown(t, tree, checks, tree.base, "")

			require.NoError(t, err)
			assert.Contains(t, shown, "the revert run proves nothing: ")
			assert.Contains(t, shown, c.finds)
		})
	}
}

func TestReviewFixBeforeReview_AHeadWithNoNewTestGetsNoRevertFinding(t *testing.T) {
	t.Parallel()

	tree := reviewTreeOf(t, "src/a.go", "add a")
	zero := 0

	shown, err := reviewShown(t, tree, &GateChecks{Complete: true, Secrets: &zero, Revert: model.RevertFail}, tree.base, "")

	require.NoError(t, err)
	assert.Equal(t, "show False the head adds no new test. Add a test that fails without the change", shown)
}

func TestReviewFixBeforeReview_ALogOverSixteenKiBBeforeTheEndMarkerPassesWithItsNewTestNames(t *testing.T) {
	t.Parallel()

	log := jobLog(1000, "0", "beehest-new-test pkg TestA", "beehest-new-test pkg TestA/case", "beehest-new-tests-end new=2 failed=0", revertClean)
	require.Greater(t, len(log), tailBytes, "the log is longer than the tail that the host reads")

	read, checks := logChecks(t, log)
	tree := reviewTreeOf(t, "src/a.go", "add a")

	shown, err := reviewShown(t, tree, &checks, tree.base, "")

	require.NoError(t, err)
	assert.Equal(t, tailBytes, read)
	assert.Equal(t, []string{"pkg TestA", "pkg TestA/case"}, checks.NewTests, "a subtest is its own new test")
	assert.Equal(t, "show True ", shown)
}

func TestReviewFixBeforeReview_TheChecksOfARealLogAreFindings(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		log, finds string
	}{
		"skipped new test": {
			jobLog(1000, "0", "beehest-new-test pkg TestA", "beehest-new-test-failed skipped pkg TestA", "beehest-new-tests-end new=1 failed=1", revertClean),
			"a new test failed a check: skipped pkg TestA",
		},
		"no end marker": {jobLog(1000, "0", "beehest-new-test pkg TestA"), "no end marker"},
		"zero new":      {jobLog(1000, "0", "beehest-new-tests-end new=0 failed=0"), "adds no new test"},
		"secret scan":   {jobLog(1000, "1", "beehest-new-test pkg TestA", "beehest-new-tests-end new=1 failed=0", revertClean), "secret scan of the head exited with code 1"},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, checks := logChecks(t, c.log)
			tree := reviewTreeOf(t, "src/a.go", "add a")

			shown, err := reviewShown(t, tree, &checks, tree.base, "")

			require.NoError(t, err)
			assert.Contains(t, shown, "show False")
			assert.Contains(t, shown, c.finds)
		})
	}
}

func TestReviewFixBeforeReview_AProjectWithNoRepositoryStopsBeforeAnyCommandWithAMessageThatNamesTheKeys(t *testing.T) {
	t.Parallel()

	tree := reviewTreeOf(t, "src/a.go", "add a")
	h := reviewHarness(t, tree, cleanChecks())
	h.facts = func(string) model.Project { return model.Project{} }
	h.bare = true
	h.commands.reply = gitReply(t, tree.dir)

	_, err := walkText(t, h, libReviewFlow(tree.base, ""))

	require.ErrorContains(t, err, "before_review needs a repository, and this project has no repository. Set `source` or `forge` in the project file")

	for _, line := range h.commands.ran() {
		assert.NotContains(t, line, "git", "no git command runs in a project with no repository")
	}
}

func TestReviewFixBeforeReview_TheAllowListHasTheDialectOfTheAgentScope(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		allow   string
		outside bool
	}{
		"braces name two trees":                       {`["{docs,src}/**"]`, false},
		"a bare directory name is the directory only": {`["docs"]`, true},
		"a glob that crosses the slash":               {`["docs/**"]`, false},
		"a star stops at the slash":                   {`["docs/*"]`, false},
		"a star does not cross a directory":           {`["*"]`, true},
		"the magic prefix of a pathspec is text":      {`[":(glob)docs/**"]`, true},
		"the dot slash prefix is dropped":             {`["./docs/**"]`, false},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			tree := reviewTreeOf(t, "docs/a.md", "add a")

			shown, err := reviewShown(t, tree, cleanChecks(), tree.base, "allow = "+c.allow+", ")

			require.NoError(t, err)

			if c.outside {
				assert.Contains(t, shown, "show False")
				assert.Contains(t, shown, "1 path(s) outside allow [")
				assert.Contains(t, shown, ": docs/a.md")
			} else {
				assert.Equal(t, "show True ", shown)
			}
		})
	}
}

func TestReviewFixBeforeReview_ABadAllowListStopsTheItemBeforeAnyStepAndNamesThePattern(t *testing.T) {
	t.Parallel()

	cases := map[string]struct{ allow, message string }{
		"an empty entry":           {`[""]`, `has the empty pattern ""`},
		"an absolute path":         {`["/docs/**"]`, `has the absolute pattern "/docs/**"`},
		"a path that leaves up":    {`["../docs/**"]`, `has the pattern "../docs/**", which leaves the item folder`},
		"a bare double dot":        {`[".."]`, `has the pattern "..", which leaves the item folder`},
		"an inner double dot":      {`["docs/../x"]`, `has the pattern "docs/../x", which leaves the item folder`},
		"a trailing double dot":    {`["docs/.."]`, `has the pattern "docs/..", which leaves the item folder`},
		"a brace choice of ..":     {`["{docs,..}/x"]`, `has the pattern "{docs,..}/x", which leaves the item folder`},
		"a bracket that opens":     {`["docs/["]`, `has the pattern "docs/[", which does not parse`},
		"a list with no entry":     {`[]`, "is an empty list, which would allow every path"},
		"an entry that is no text": {`[3]`, "must hold strings only"},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			tree := reviewTreeOf(t, "docs/a.md", "add a")
			h := reviewHarness(t, tree, cleanChecks())
			h.commands.reply = gitReply(t, tree.dir)

			_, err := walkText(t, h, libReviewFlow(tree.base, "allow = "+c.allow+", "))

			require.ErrorContains(t, err, "allow "+c.message, "the message names the keyword and the pattern")
			assert.Empty(t, slices.DeleteFunc(h.commands.ran(), func(line string) bool { return !strings.HasPrefix(line, "git") }), "no git command runs")
		})
	}
}

func TestReviewFixBeforeReview_AProjectWithAForgeAndNoSourceHasARepository(t *testing.T) {
	t.Parallel()

	tree := reviewTreeOf(t, "src/a.go", "add a")
	h := reviewHarness(t, tree, cleanChecks())
	h.bare = true
	h.facts = func(string) model.Project { return model.Project{Forge: "github"} }
	h.preflight = &fakePreflight{} // the owner wrote the item, so the run starts
	h.commands.reply = gitReply(t, tree.dir)

	_, err := walkText(t, h, libReviewFlow(tree.base, ""))

	require.NoError(t, err)
	assert.Contains(t, h.commands.ran(), "show True ", "the checks ran: a forge alone is a repository")
}
