package ssh_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/host/hosttest"
	"github.com/tobocop2/beehest/internal/host/jobwait"
	"github.com/tobocop2/beehest/internal/host/sshtest"
	"github.com/tobocop2/beehest/internal/model"
)

const gateFolder = sshtest.Root + "/work/beehest-gate-one-abc"

func TestSubmit_ReturnsAnIdThatHoldsTheFolderOfTheJob(t *testing.T) {
	t.Parallel()

	m := sshtest.New(hosttest.Working)
	h, _, _ := newHost(t, m, config())

	id, err := h.Submit(t.Context(), model.JobSpec{Name: "gate", Command: "true", Folder: gateFolder})

	require.NoError(t, err)
	assert.Equal(t, jobwait.Job{ID: "beehest-n1", Folder: gateFolder}.String(), string(id))
	require.NotNil(t, m.Units["beehest-n1"], "the unit is named by the key, not by the folder")
}

func TestSubmit_RefusesAFolderThatIsNotAnAbsolutePath(t *testing.T) {
	t.Parallel()

	m := sshtest.New(hosttest.Working)
	h, _, _ := newHost(t, m, config())

	_, err := h.Submit(t.Context(), model.JobSpec{Name: "gate", Command: "true", Folder: "work/clone"})

	require.ErrorIs(t, err, jobwait.ErrBadFolder)
	assert.Zero(t, calledWith(m, "systemd-run"))
}

func TestWait_AJobWithAFolderBanksOnlyItsFolderAndNeverTheWorkRoot(t *testing.T) {
	t.Parallel()

	m := sshtest.New(hosttest.Working)
	h, bank, _ := newHost(t, m, config())

	id, err := h.Submit(t.Context(), model.JobSpec{Name: "gate", Command: hosttest.FailingCommand, Folder: gateFolder})
	require.NoError(t, err)

	state, err := h.Wait(t.Context(), id)

	require.ErrorContains(t, err, "scan "+gateFolder+" for repositories", "the fake machine has no find, so the scan of the folder of the job is what fails")
	assert.Equal(t, model.JobFailed, state)

	assert.Empty(t, bank.calls, "the bank of the work root does not run for a job with a folder")
	assert.Positive(t, calledWith(m, "test -d "+gateFolder), "the bank of the job looks at its own folder")
	assert.Zero(t, calledWith(m, "find -H "+sshtest.Root+"/work "), "no scan of the work root, which holds the clones of other jobs")
}

func TestWait_AJobWithNoFolderBanksTheWorkRoot(t *testing.T) {
	t.Parallel()

	m := sshtest.New(hosttest.Working)
	h, bank, _ := newHost(t, m, config())

	id, err := h.Submit(t.Context(), model.JobSpec{Name: "x", Command: hosttest.FailingCommand})
	require.NoError(t, err)

	_, err = h.Wait(t.Context(), id)

	require.NoError(t, err)
	assert.Len(t, bank.calls, 1)
}

func TestWait_RefusesAnIdWhoseFolderIsNotAnAbsolutePath(t *testing.T) {
	t.Parallel()

	m := sshtest.New(hosttest.Working)
	h, _, _ := newHost(t, m, config())

	_, err := h.Wait(t.Context(), "beehest-n1~!!!")

	require.ErrorIs(t, err, jobwait.ErrBadFolder)
	assert.Zero(t, calledWith(m, "sh"))
}
