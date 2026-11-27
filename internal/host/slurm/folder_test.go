package slurm

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/host"
	"github.com/tobocop2/beehest/internal/host/jobwait"
	"github.com/tobocop2/beehest/internal/model"
)

const gateFolder = "/home/lane/beehest-gate-one-abc"

func TestSubmit_ReturnsAnIdThatHoldsTheFolderOfTheJob(t *testing.T) {
	t.Parallel()

	h, ssh := newScripted(t)
	ssh.Plan("77;cluster1\n", "sbatch")

	id, err := h.Submit(t.Context(), model.JobSpec{Name: "gate", Command: "true", Folder: gateFolder})

	require.NoError(t, err)
	assert.Equal(t, jobwait.Job{ID: "77", Nonce: "n", Folder: gateFolder}.String(), string(id))
}

func TestSubmit_RefusesAFolderThatIsNotAnAbsolutePath(t *testing.T) {
	t.Parallel()

	h, ssh := newScripted(t)

	_, err := h.Submit(t.Context(), model.JobSpec{Name: "gate", Command: "true", Folder: "clone"})

	require.ErrorIs(t, err, jobwait.ErrBadFolder)
	assert.False(t, ssh.Called("sbatch"))
}

func TestWait_AJobWithAFolderBanksOnlyItsFolderAndNeverTheWorkRoot(t *testing.T) {
	t.Parallel()

	roots := 0
	h, ssh := newScripted(t, WithBank(func(context.Context) error { roots++; return nil }))
	ssh.Plan("COMPLETED\n", "squeue")
	ssh.PlanResult(model.CommandResult{ExitCode: 1}, "test")

	id := model.JobID(jobwait.Job{ID: "5", Nonce: "n", Folder: gateFolder}.String())

	state, err := h.Wait(t.Context(), id)

	require.NoError(t, err, "a folder that is gone holds no work")
	assert.Equal(t, model.JobSucceeded, state)
	assert.Zero(t, roots, "the bank of the work root does not run for a job with a folder")
	assert.True(t, ssh.Called("test", "-d", gateFolder), "the bank of the job looks at its own folder")
}

func TestWait_AFolderThatCannotBeCheckedKeepsTheWorkUnbanked(t *testing.T) {
	t.Parallel()

	h, ssh := newScripted(t)
	ssh.Plan("COMPLETED\n", "squeue")
	ssh.PlanResult(model.CommandResult{ExitCode: 255, Stderr: "no route"}, "test")

	id := model.JobID(jobwait.Job{ID: "5", Nonce: "n", Folder: gateFolder}.String())

	_, err := h.Wait(t.Context(), id)

	require.ErrorContains(t, err, "check the folder "+gateFolder)
	require.NotErrorIs(t, err, host.ErrNoRepos)
}

func TestWait_AJobWithNoFolderBanksTheWorkRoot(t *testing.T) {
	t.Parallel()

	roots := 0
	h, ssh := newScripted(t, WithBank(func(context.Context) error { roots++; return nil }))
	ssh.Plan("COMPLETED\n", "squeue")

	_, err := h.Wait(t.Context(), "5-n")

	require.NoError(t, err)
	assert.Equal(t, 1, roots)
}

func TestWait_RefusesAnIdWhoseFolderIsNotAnAbsolutePath(t *testing.T) {
	t.Parallel()

	h, ssh := newScripted(t)

	_, err := h.Wait(t.Context(), "5-n~!!!")

	require.ErrorIs(t, err, jobwait.ErrBadFolder)
	assert.False(t, ssh.Called("squeue"))
}
