package slurm

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/host/hosttest"
	"github.com/tobocop2/beehest/internal/host/slurmtest"
	"github.com/tobocop2/beehest/internal/model"
)

func TestSlurm_ALostJobIsProvedByAFinalStateAfterTheCancel(t *testing.T) {
	t.Parallel()

	h, f, _ := newClockedHost(t)
	id, job := submitted(t, h, f)

	proof, err := h.LostProof(context.Background(), id)

	require.NoError(t, err)
	assert.Equal(t, model.LostEnded, proof.Kind, "a canceled job has a final state")
	assert.Equal(t, 1, f.Scancels, "the proof runs scancel first")
	assert.True(t, job.Canceled)
	assert.NotEmpty(t, proof.Note)

	again, err := h.LostProof(context.Background(), id)

	require.NoError(t, err, "the proof is safe to read again")
	assert.Equal(t, model.LostEnded, again.Kind)

	// An empty record (died) is no proof: the accounting record can come late.
	h, f, _ = newClockedHost(t)
	id, job = submitted(t, h, f)
	job.Vanished = true

	died, err := h.LostProof(context.Background(), id)

	require.NoError(t, err)
	assert.Empty(t, died.Kind, "an empty record is no proof")
}

func TestSlurm_ALostJobThatTheCancelDoesNotStopYetHasNoProof(t *testing.T) {
	t.Parallel()

	h, f, _ := newClockedHost(t)
	id, job := submitted(t, h, f)
	job.Lingers = 1

	proof, err := h.LostProof(context.Background(), id)

	require.NoError(t, err)
	assert.Empty(t, proof.Kind, "the scheduler still reads the job as running")
	assert.NotEmpty(t, proof.Note)

	proof, err = h.LostProof(context.Background(), id)

	require.NoError(t, err)
	assert.Equal(t, model.LostEnded, proof.Kind, "the next read proves it")
}

func TestSlurm_AnEmptyRecordIsNoProofBecauseTheAccountingRecordCanComeLate(t *testing.T) {
	t.Parallel()

	h, f, _ := newClockedHost(t)
	id, job := submitted(t, h, f)
	job.Vanished = true

	proof, err := h.LostProof(context.Background(), id)

	require.NoError(t, err)
	assert.Empty(t, proof.Kind)
	assert.Contains(t, proof.Note, "no record")
}

func TestSlurm_AJobThatEndedOnItsOwnIsProvedByItsFinalStateWhateverScancelSays(t *testing.T) {
	t.Parallel()

	for name, command := range map[string]string{"a job that passed": hosttest.PassingCommand, "a job that failed": hosttest.FailingCommand} {
		h, f, _ := newClockedHost(t)
		id, job := submitted(t, h, f)
		job.Command = command
		job.Polls = 10 // the controller has forgotten the job, so sacct answers
		f.NoStop = true

		proof, err := h.LostProof(context.Background(), id)

		require.NoError(t, err, name)
		assert.Equal(t, model.LostEnded, proof.Kind, name)
		assert.Zero(t, f.Scancels, "%s: scancel was refused, and the exit status is not the proof", name)
	}
}

func TestSlurm_ALostJobHasNoProofWhileTheLinkIsDownOrTheReadFails(t *testing.T) {
	t.Parallel()

	for name, plan := range map[string]func(*slurmtest.Cluster, *slurmtest.Job){
		"the link is down":  func(f *slurmtest.Cluster, _ *slurmtest.Job) { f.Drops = -1 },
		"the read fails":    func(f *slurmtest.Cluster, _ *slurmtest.Job) { f.Broken = true },
		"the state is odd":  func(_ *slurmtest.Cluster, job *slurmtest.Job) { job.Command = hosttest.UnknownCommand },
		"the scancel drops": func(f *slurmtest.Cluster, _ *slurmtest.Job) { f.DropAll = "scancel" },
	} {
		h, f, _ := newClockedHost(t)
		id, job := submitted(t, h, f)

		plan(f, job)

		proof, err := h.LostProof(context.Background(), id)

		require.NoError(t, err, name)
		assert.Empty(t, proof.Kind, name)
		assert.NotEmpty(t, proof.Note, name)

		if name == "the scancel drops" {
			assert.NotContains(t, proof.Note, "cannot be read", "the state read answered running, so nothing failed")
		} else {
			assert.Contains(t, proof.Note, "cannot be read", "%s: the note holds the failed read", name)
		}
	}

	h, _, _ := newClockedHost(t)

	_, err := h.LostProof(context.Background(), "not a job")

	require.Error(t, err, "an id that Submit did not return is a fault")
}
