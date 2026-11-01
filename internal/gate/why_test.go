package gate_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/gate"
	"github.com/tobocop2/obee/internal/model"
)

// logRemote is a fakeRemote that also gives the log of a job, as a host that can read its log does.
type logRemote struct {
	*fakeRemote

	log    string
	logErr error
}

func (l logRemote) Log(context.Context, model.JobID) (string, error) { return l.log, l.logErr }

func runFailedHostJob(t *testing.T, remote gate.Remote) gate.Result {
	t.Helper()

	g := gate.New((&fakeShell{}).run, (&fakeTree{states: []gate.State{clean(headA)}}).read, func(model.Project) (gate.Remote, error) { return remote, nil })

	result, err := g.Run(context.Background(), hostRequest(withReport(project("cluster"))))
	require.NoError(t, err)
	assert.False(t, result.Passed)

	return result
}

func TestVerdict_ACommandThatPassedWithNoTestCaseReadsAsPassedCommandAndFailedTests(t *testing.T) {
	t.Parallel()

	remote := logRemote{fakeRemote: &fakeRemote{state: model.JobFailed}, log: "ok\nobee-gate-no-tests\nobee-gate-done\n"}

	result := runFailedHostJob(t, remote)

	assert.Equal(t, []gate.Step{step(gate.StepCommand, gate.StatusPass), step(gate.StepTests, gate.StatusFail)}, result.Steps)
	assert.Contains(t, result.Report, "no test case")
	assert.Contains(t, result.Report, "reports/*.xml")
}

func TestVerdict_AFailedCommandKeepsItsFailureWhenTheLogHasNoMarker(t *testing.T) {
	t.Parallel()

	remote := logRemote{fakeRemote: &fakeRemote{state: model.JobFailed}, log: "make: *** Error 2\nobee-gate-done\n"}

	result := runFailedHostJob(t, remote)

	assert.Equal(t, []gate.Step{step(gate.StepCommand, gate.StatusFail), step(gate.StepTests, gate.StatusUnknown)}, result.Steps)
}

func TestVerdict_AHostThatCannotReadItsLogKeepsTheOldReadingAndNamesBothCauses(t *testing.T) {
	t.Parallel()

	for _, remote := range []gate.Remote{
		&fakeRemote{state: model.JobFailed},
		logRemote{fakeRemote: &fakeRemote{state: model.JobFailed}, logErr: errors.New("link down")},
	} {
		result := runFailedHostJob(t, remote)

		assert.Equal(t, []gate.Step{step(gate.StepCommand, gate.StatusFail), step(gate.StepTests, gate.StatusUnknown)}, result.Steps)
		assert.Contains(t, result.Report, "or the job found no test case")
	}
}

func TestVerdict_ATrackedReportFileIsRefusedAndNamed(t *testing.T) {
	t.Parallel()

	remote := logRemote{fakeRemote: &fakeRemote{state: model.JobFailed}, log: "obee-gate-tracked-report reports/old.xml\nobee-gate-done\n"}

	result := runFailedHostJob(t, remote)

	assert.Equal(t, []gate.Step{step(gate.StepCommand, gate.StatusUnknown), step(gate.StepTests, gate.StatusFail)}, result.Steps)
	assert.Contains(t, result.Report, "reports/old.xml")
	assert.Contains(t, result.Report, "tracks")
}

func TestHostCommand_RefusesATrackedReportFileBeforeTheGateRuns(t *testing.T) {
	t.Parallel()

	remote := &fakeRemote{state: model.JobSucceeded}
	g := build(&fakeShell{}, &fakeTree{states: []gate.State{clean(headA)}}, remote)

	_, err := g.Run(context.Background(), hostRequest(withReport(hostProject())))
	require.NoError(t, err)

	command := remote.submitted[0].Command
	assert.Contains(t, command, `git ls-files -- 'reports/*.xml'`)
	assert.Less(t, strings.Index(command, "git ls-files"), strings.Index(command, "sh -c 'make check'"))
}
