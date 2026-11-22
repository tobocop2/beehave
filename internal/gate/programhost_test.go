package gate_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/gate"
	"github.com/tobocop2/beehave/internal/model"
)

func programProject() model.Project {
	project := hostProject()
	project.GateProgram = []string{"Makefile", "scripts/"}

	return project
}

func TestRun_AHostGateWithAGateProgramPutsTheProgramWordsInTheJob(t *testing.T) {
	t.Parallel()

	remote := &fakeRemote{state: model.JobSucceeded}
	g := build(&fakeShell{}, &fakeTree{states: []gate.State{clean(headA)}}, remote)

	_, err := g.Run(context.Background(), hostRequest(programProject()))
	require.NoError(t, err)
	require.Len(t, remote.submitted, 1)

	command := remote.submitted[0].Command
	assert.Contains(t, command, "gppaths='Makefile scripts/'")
	assert.Contains(t, command, "gpsha="+tipSHA)
	assert.Contains(t, command, "gpgit rm -r -q -f --ignore-unmatch -- Makefile && rm -rf -- Makefile")
	assert.Contains(t, command, "gpgit rm -r -q -f --ignore-unmatch -- scripts/ && rm -rf -- scripts/")
	assert.True(t, len(command) > len("gpcompare") && command[len(command)-len("gpcompare"):] == "gpcompare", "the final check is the last word")
}

func TestRun_AHostGateWithNoGateProgramHasNoProgramWords(t *testing.T) {
	t.Parallel()

	remote := &fakeRemote{state: model.JobSucceeded}
	g := build(&fakeShell{}, &fakeTree{states: []gate.State{clean(headA)}}, remote)

	_, err := g.Run(context.Background(), hostRequest(hostProject()))
	require.NoError(t, err)
	require.Len(t, remote.submitted, 1)
	assert.NotContains(t, remote.submitted[0].Command, "gp")
}

func TestRun_AHostGateWithAGateProgramAndNoBaseCommitStartsNoJob(t *testing.T) {
	t.Parallel()

	remote := &fakeRemote{state: model.JobSucceeded}
	g := build(&fakeShell{}, &fakeTree{states: []gate.State{clean(headA)}}, remote)
	request := hostRequest(programProject())
	request.BaseSHA = ""

	_, err := g.Run(context.Background(), request)

	require.ErrorIs(t, err, gate.ErrNoBase)
	assert.Empty(t, remote.submitted)
}

func TestVerdict_ARefusedHeadReadsAsAFailedGateProgramStepWithTheChangedPaths(t *testing.T) {
	t.Parallel()

	log := "beehave-gate-program the head changed the gate program, paths Makefile scripts/check.sh. The gate runs the program of the base commit.\nbeehave-gate-done\n"
	remote := logRemote{fakeRemote: &fakeRemote{state: model.JobFailed}, log: log}

	result := runFailedHostJob(t, remote)

	assert.Equal(t, []gate.Step{step(gate.StepCommand, gate.StatusUnknown), step(gate.StepTests, gate.StatusUnknown), step(model.StepGateProgram, gate.StatusFail)}, result.Steps)
	assert.Contains(t, result.Report, "Makefile scripts/check.sh")
}

func TestKey_TheGateProgramListIsPartOfTheKey(t *testing.T) {
	t.Parallel()

	g := build(&fakeShell{}, &fakeTree{states: []gate.State{clean(headA), clean(headA)}}, &fakeRemote{})
	plain, err := g.Key(context.Background(), hostRequest(hostProject()))
	require.NoError(t, err)

	listed, err := g.Key(context.Background(), hostRequest(programProject()))
	require.NoError(t, err)

	assert.NotEqual(t, plain.ID, listed.ID)
}
