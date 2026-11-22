package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/flow"
	"github.com/tobocop2/beehave/internal/host"
	"github.com/tobocop2/beehave/internal/model"
)

// loggedHost is a host whose job log holds the lines of a new-tests check.
type loggedHost struct {
	fakeHost

	log string
}

func (l *loggedHost) Log(context.Context, model.JobID) (string, error) { return l.log, nil }

// candidateChecks runs a candidate gate whose host job succeeded with log, and returns the checks that the engine gets.
func candidateChecks(t *testing.T, log string) flow.GateChecks {
	t.Helper()

	hosted := &loggedHost{fakeHost: fakeHost{state: model.JobSucceeded}, log: log}
	projects := clusterProject()
	repo := projects["p"]
	repo.Source = "/repo"
	projects["p"] = repo

	gate := projectGate{
		tip:      fixedTip(anchorOld, nil),
		projects: projects,
		programs: programs{
			trees: treeAt("/tree"),
			local: gateRunner(&gateCalls{}, model.CommandResult{Stdout: "abc123"}, model.CommandResult{}),
			hosts: func(string, string) (host.Host, error) { return hosted, nil },
		},
		forge: forgeAt("https://example.test/repo.git"),
	}

	job, err := gate.Submit(t.Context(), "p", "1", "", "main", "", "")
	require.NoError(t, err)

	got, err := gate.Run(t.Context(), "p", "1", "", job)

	require.NoError(t, err)
	require.NotNil(t, got.Checks)

	return *got.Checks
}

func TestProjectGate_ACandidateGateGivesTheChecksOfItsLogToTheEngine(t *testing.T) {
	t.Parallel()

	got := candidateChecks(t, "beehave-gate-step secret-scan rc=0\n"+
		"beehave-new-test pkg TestA\n"+
		"beehave-new-test-failed skipped pkg TestB\n"+
		"beehave-new-tests-end new=2 failed=1\n")

	zero := 0

	// The log has no revert section, so the revert run proves nothing and the verdict is fail.
	assert.Equal(t, flow.GateChecks{
		NewTests: []string{"pkg TestA"}, Failed: []string{"skipped pkg TestB"}, New: 2, FailedCount: 1, Secrets: &zero, Complete: true, Revert: model.RevertFail,
	}, got)
}

func TestProjectGate_ACandidateGateGivesTheRevertRunOfItsLogToTheEngine(t *testing.T) {
	t.Parallel()

	// The new tests pkg TestA and pkg TestC passed on the base code. the before_review function of the library names each test of RevertPassed in its refusal, so the
	// engine must get every revert field of the log.
	got := candidateChecks(t, "beehave-new-test pkg TestA\n"+
		"beehave-new-test pkg TestC\n"+
		"beehave-new-tests-end new=3 failed=0\n"+
		"beehave-revert-passed pkg TestA\n"+
		"beehave-revert-passed pkg TestC\n"+
		"beehave-revert-end new=3 passed=3 cases=7\n"+
		"beehave-gate-step secret-scan rc=0\n"+
		"beehave-gate-step revert-run rc=1\n")

	zero, one := 0, 1

	assert.Equal(t, flow.GateChecks{
		NewTests: []string{"pkg TestA", "pkg TestC"}, New: 3, Secrets: &zero, Complete: true,
		Revert: model.RevertFail, RevertPassed: []string{"pkg TestA", "pkg TestC"}, RevertPassedCount: 3, RevertCases: 7, RevertRC: &one,
	}, got)
}

func TestProjectGate_ACandidateGateGivesTheReasonOfARevertErrorToTheEngine(t *testing.T) {
	t.Parallel()

	// The before_review function of the library shows the reason of the error line in its refusal, in place of the generic reason of a log with no end marker.
	got := candidateChecks(t, "beehave-new-test pkg TestA\n"+
		"beehave-new-tests-end new=1 failed=0\n"+
		"beehave-revert-error the base code did not check out\n"+
		"beehave-gate-step secret-scan rc=0\n")

	zero := 0

	assert.Equal(t, flow.GateChecks{
		NewTests: []string{"pkg TestA"}, New: 1, Secrets: &zero, Complete: true, Revert: model.RevertFail, RevertProblem: "the base code did not check out",
	}, got)
}
