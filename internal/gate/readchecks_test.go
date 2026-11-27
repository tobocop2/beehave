package gate_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/gate"
	"github.com/tobocop2/beehest/internal/model"
)

// runPassedCandidate runs a candidate head on a host whose job succeeded, and returns the result.
func runPassedCandidate(t *testing.T, remote gate.Remote) gate.Result {
	t.Helper()

	g := gate.New((&fakeShell{}).run, (&fakeTree{states: []gate.State{clean(headA)}}).read, func(model.Project) (gate.Remote, error) { return remote, nil })
	request := hostRequest(withReport(project("cluster")))
	request.Base = "main"

	result, err := g.Run(context.Background(), request)
	require.NoError(t, err)

	return result
}

func TestChecks_APassedCandidateJobGivesTheChecksOfItsLog(t *testing.T) {
	t.Parallel()

	log := "beehest-new-test pkg TestA\nbeehest-new-tests-end new=1 failed=0\n"
	result := runPassedCandidate(t, logRemote{fakeRemote: &fakeRemote{state: model.JobSucceeded}, log: log})

	require.NotNil(t, result.Checks)
	assert.True(t, result.Checks.Complete)
	assert.Equal(t, []string{"pkg TestA"}, result.Checks.NewTests)
	assert.Empty(t, result.Checks.Problem)
}

func TestChecks_AResumedJobIsACandidateByItsPlanAndNotByTheBaseOfTheRequest(t *testing.T) {
	t.Parallel()

	log := "beehest-new-test pkg TestA\nbeehest-new-tests-end new=1 failed=0\n"
	remote := logRemote{fakeRemote: &fakeRemote{state: model.JobSucceeded}, log: log}
	g := gate.New((&fakeShell{}).run, (&fakeTree{states: []gate.State{clean(headA)}}).read, func(model.Project) (gate.Remote, error) { return remote, nil })

	plain := hostRequest(withReport(project("cluster")))
	plain.Resume, plain.ResumeHead, plain.Base = "job-3", headB, "main"

	got, err := g.Run(context.Background(), plain)
	require.NoError(t, err)
	assert.Nil(t, got.Checks, "the plan of the resumed job has no base, so the job ran no candidate steps")

	candidate := hostRequest(withReport(project("cluster")))
	candidate.Resume, candidate.ResumeHead, candidate.Plan.Base = "job-3", headB, "main"

	got, err = g.Run(context.Background(), candidate)
	require.NoError(t, err)
	require.NotNil(t, got.Checks)
	assert.True(t, got.Checks.Complete)
}

func TestReadChecks_ALogThatIsOnlyTheEndMarkerIsComplete(t *testing.T) {
	t.Parallel()

	checks := gate.ReadChecks("beehest-new-tests-end new=0 failed=0")

	assert.True(t, checks.Complete)
	assert.Zero(t, checks.New)
}

func TestChecks_APassedCandidateJobOnAHostWithoutALogReaderNamesThatAsItsProblem(t *testing.T) {
	t.Parallel()

	result := runPassedCandidate(t, &fakeRemote{state: model.JobSucceeded})

	require.NotNil(t, result.Checks)
	assert.False(t, result.Checks.Complete)
	assert.Equal(t, "the build host cannot give the log of the gate job", result.Checks.Problem)
}

func TestChecks_APassedCandidateJobWhoseLogCannotBeReadNamesTheReadError(t *testing.T) {
	t.Parallel()

	result := runPassedCandidate(t, logRemote{fakeRemote: &fakeRemote{state: model.JobSucceeded}, logErr: errors.New("link down")})

	require.NotNil(t, result.Checks)
	assert.False(t, result.Checks.Complete)
	assert.Equal(t, "the log of the gate job cannot be read: link down", result.Checks.Problem)
}
