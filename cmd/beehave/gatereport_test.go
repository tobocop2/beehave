package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/gate"
	"github.com/tobocop2/beehave/internal/model"
)

const passingReport = `<testsuite tests="1" failures="0"><testcase name="a"/></testsuite>`

// reportGate returns a gate whose command runs under a fake enforcer that calls write in the copy of the work folder, and the project that it judges.
func reportGate(t *testing.T, pattern string, write func(dir string)) (gate.Gate, model.Project, *fakeEnforcer) {
	t.Helper()

	fake := &fakeEnforcer{during: write}
	box := sandboxFor(t, fake, nil)

	found := box.projects["p"]
	found.TestReport = pattern
	box.projects["p"] = found

	tree := func(context.Context, string) (gate.State, error) {
		return gate.State{Head: "h", Tree: "t", Clean: true}, nil
	}
	shell := projectGate{projects: box.projects, programs: programs{sandbox: box}}.shellFor("p")

	return gate.New(shell, tree, nil), found.Project, fake
}

func writeReport(t *testing.T, dir, name, text string) {
	t.Helper()

	require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600))
}

func TestGateSandbox_ASandboxedGateThatWritesItsTestReportPassesWhenTheTestsPass(t *testing.T) {
	t.Parallel()

	repo := repoWithCommit(t)
	g, project, _ := reportGate(t, "reports/*.xml", func(dir string) { writeReport(t, dir, "reports/junit.xml", passingReport) })

	result, err := g.Run(t.Context(), gate.Request{Project: project, Dir: repo})

	require.NoError(t, err)
	assert.True(t, result.Passed, result.Report)
	assert.FileExists(t, filepath.Join(repo, "reports", "junit.xml"), "the report is where the gate reads it")
}

func TestGateSandbox_ASandboxedGateStillFailsWhenItsReportHoldsAFailedTest(t *testing.T) {
	t.Parallel()

	repo := repoWithCommit(t)
	failing := `<testsuite tests="1" failures="1"><testcase name="a"><failure/></testcase></testsuite>`
	g, project, _ := reportGate(t, "reports/*.xml", func(dir string) { writeReport(t, dir, "reports/junit.xml", failing) })

	result, err := g.Run(t.Context(), gate.Request{Project: project, Dir: repo})

	require.NoError(t, err)
	assert.False(t, result.Passed)
	assert.Contains(t, result.Report, "1 failed of 1 tests")
}

func TestGateSandbox_ATestReportOutsideTheWorkFolderIsRefusedAndRunsNoCommand(t *testing.T) {
	t.Parallel()

	for _, pattern := range []string{"../reports/*.xml", "/tmp/*.xml", "reports/../../x.xml"} {
		t.Run(pattern, func(t *testing.T) {
			t.Parallel()

			repo := repoWithCommit(t)
			g, project, fake := reportGate(t, pattern, func(string) {})

			result, err := g.Run(t.Context(), gate.Request{Project: project, Dir: repo})

			require.NoError(t, err)
			assert.False(t, result.Passed)
			assert.Nil(t, fake.argv, "the command did not run")
			assert.Contains(t, result.Report, "is not inside the work folder")
		})
	}
}

func TestGateSandbox_ATestReportGlobThatIsNotValidIsRefusedAndRunsNoCommand(t *testing.T) {
	t.Parallel()

	repo := repoWithCommit(t)
	g, project, fake := reportGate(t, "reports/[", func(string) {})

	result, err := g.Run(t.Context(), gate.Request{Project: project, Dir: repo})

	require.NoError(t, err)
	assert.False(t, result.Passed)
	assert.Nil(t, fake.argv)
	assert.Contains(t, result.Report, "read the test_report")
}

func TestGateSandbox_AReportOfAFailedCommandIsNotCarriedToTheRealFolder(t *testing.T) {
	t.Parallel()

	repo := repoWithCommit(t)
	g, project, fake := reportGate(t, "reports/*.xml", func(dir string) { writeReport(t, dir, "reports/junit.xml", passingReport) })
	fake.code = 2

	result, err := g.Run(t.Context(), gate.Request{Project: project, Dir: repo})

	require.NoError(t, err)
	assert.False(t, result.Passed)
	assert.NoFileExists(t, filepath.Join(repo, "reports", "junit.xml"))
}

func TestGateSandbox_AReportThatTheCommandLeftUnchangedIsNotCarried(t *testing.T) {
	t.Parallel()

	repo := repoWithCommit(t)
	writeReport(t, repo, "reports/old.xml", passingReport)
	runGit(t, repo, "add", "reports/old.xml")
	runGit(t, repo, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "track a report")

	old := filepath.Join(repo, "reports", "old.xml")
	stale := evaluatedTime(t, old)
	g, project, _ := reportGate(t, "reports/*.xml", func(string) {})

	result, err := g.Run(t.Context(), gate.Request{Project: project, Dir: repo})

	require.NoError(t, err)
	assert.False(t, result.Passed, "a stale report is no result")
	assert.Equal(t, stale, evaluatedTime(t, old), "the real file is not rewritten")
}

func evaluatedTime(t *testing.T, file string) string {
	t.Helper()

	info, err := os.Stat(file)
	require.NoError(t, err)

	return info.ModTime().String()
}

func TestGateSandbox_ALinkInTheCopyThatPointsOutsideIsNotFollowed(t *testing.T) {
	t.Parallel()

	outside := t.TempDir()
	writeReport(t, outside, "secret.xml", passingReport)

	repo := repoWithCommit(t)
	g, project, _ := reportGate(t, "reports/*.xml", func(dir string) {
		require.NoError(t, os.MkdirAll(filepath.Join(dir, "reports"), 0o750))
		require.NoError(t, os.Symlink(filepath.Join(outside, "secret.xml"), filepath.Join(dir, "reports", "link.xml")))
	})

	result, err := g.Run(t.Context(), gate.Request{Project: project, Dir: repo})

	require.NoError(t, err)
	assert.False(t, result.Passed, "a link to a file outside the work folder is no report")
	assert.NoFileExists(t, filepath.Join(repo, "reports", "link.xml"))
}

func TestGateSandbox_AFolderLinkInTheCopyThatPointsOutsideIsNotFollowed(t *testing.T) {
	t.Parallel()

	outside := t.TempDir()
	writeReport(t, outside, "secret.xml", passingReport)

	repo := repoWithCommit(t)
	g, project, _ := reportGate(t, "reports/*.xml", func(dir string) {
		require.NoError(t, os.Symlink(outside, filepath.Join(dir, "reports")))
	})

	result, err := g.Run(t.Context(), gate.Request{Project: project, Dir: repo})

	require.NoError(t, err)
	assert.False(t, result.Passed)
	assert.NoDirExists(t, filepath.Join(repo, "reports"))
}

func TestGateSandbox_AReportThatCannotBeWrittenToTheRealFolderFailsTheGateAndSaysWhy(t *testing.T) {
	t.Parallel()

	repo := repoWithCommit(t)
	// The real folder holds a file where the report folder must go.
	writeReport(t, repo, "reports", "not a folder")
	g, project, _ := reportGate(t, "reports/*.xml", func(dir string) {
		writeReport(t, dir, "reports/junit.xml", passingReport)
	})

	result, err := g.Run(t.Context(), gate.Request{Project: project, Dir: repo})

	require.NoError(t, err)
	assert.False(t, result.Passed)
	assert.Contains(t, result.Report, "write the test report reports/junit.xml to the work folder")
}

func TestGateSandbox_ACopyThatTheCommandRemovedFailsTheGateAndSaysWhy(t *testing.T) {
	t.Parallel()

	repo := repoWithCommit(t)
	g, project, _ := reportGate(t, "reports/*.xml", func(dir string) { require.NoError(t, os.RemoveAll(dir)) })

	result, err := g.Run(t.Context(), gate.Request{Project: project, Dir: repo})

	require.NoError(t, err)
	assert.False(t, result.Passed)
	assert.Contains(t, result.Report, "open the work folder for the test report")
}

func TestGateSandbox_ARealFolderThatIsGoneFailsTheGateAndSaysWhy(t *testing.T) {
	t.Parallel()

	repo := repoWithCommit(t)
	g, project, _ := reportGate(t, "reports/*.xml", func(dir string) {
		writeReport(t, dir, "reports/junit.xml", passingReport)
		require.NoError(t, os.Rename(repo, repo+"-moved"))
	})

	result, err := g.Run(t.Context(), gate.Request{Project: project, Dir: repo})

	require.NoError(t, err)
	assert.False(t, result.Passed)
	assert.Contains(t, result.Report, "open the work folder for the test report")
}

func TestGateSandbox_AReportThatCannotBeReadInTheCopyFailsTheGateAndSaysWhy(t *testing.T) {
	t.Parallel()
	notRoot(t)

	repo := repoWithCommit(t)
	g, project, _ := reportGate(t, "reports/*.xml", func(dir string) {
		writeReport(t, dir, "reports/junit.xml", passingReport)
		require.NoError(t, os.Chmod(filepath.Join(dir, "reports", "junit.xml"), 0o000))
	})

	result, err := g.Run(t.Context(), gate.Request{Project: project, Dir: repo})

	require.NoError(t, err)
	assert.False(t, result.Passed)
	assert.Contains(t, result.Report, "write the test report reports/junit.xml to the work folder")
}

func TestGateSandbox_AGateInAFolderWithNoGitWritesItsReportInPlaceAndCarriesNothing(t *testing.T) {
	t.Parallel()

	plain := t.TempDir()
	g, project, _ := reportGate(t, "reports/*.xml", func(dir string) { writeReport(t, dir, "reports/junit.xml", passingReport) })

	result, err := g.Run(t.Context(), gate.Request{Project: project, Dir: plain})

	require.NoError(t, err)
	assert.True(t, result.Passed, result.Report)
}

func TestGateSandbox_ASandboxedGateThatWritesNoReportStillFailsForTheMissingReport(t *testing.T) {
	t.Parallel()

	repo := repoWithCommit(t)
	g, project, _ := reportGate(t, "reports/*.xml", func(string) {})

	result, err := g.Run(t.Context(), gate.Request{Project: project, Dir: repo})

	require.NoError(t, err)
	assert.False(t, result.Passed)
	assert.Contains(t, result.Report, "no file matches the test_report glob")
}
