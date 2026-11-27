package ssh_test

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/host/hosttest"
	"github.com/tobocop2/beehest/internal/host/link"
	"github.com/tobocop2/beehest/internal/host/sshtest"
	"github.com/tobocop2/beehest/internal/model"
)

const stateFolderPath = sshtest.Root + "/work/.beehest"

func TestSSH_TheStopMakesTheStateFolderOnAHostThatHasNone(t *testing.T) {
	t.Parallel()

	m := sshtest.New(hosttest.Working)
	m.StrictFolders = true

	h, bank, _ := newHost(t, m, config())

	proof, err := h.LostProof(t.Context(), "beehest-n1")

	require.NoError(t, err)
	assert.Equal(t, model.LostStopped, proof.Kind, "a unit that is not loaded is stopped")
	assert.True(t, m.Files[stateFolderPath+"/"], "the stop makes the state folder")
	assert.True(t, m.Files[stateFolderPath+"/beehest-n1.canceled"], "the marker is written in it")
	assert.Empty(t, bank.calls, "the proof never banks")
	assert.Zero(t, calledWith(m, "tail"), "the proof never reads the log")

	order := words(m)
	folder := slices.IndexFunc(order, func(call string) bool { return call == "mkdir -p "+stateFolderPath })
	marker := slices.Index(order, "touch "+stateFolderPath+"/beehest-n1.canceled")
	stop := slices.Index(order, "systemctl --user stop beehest-n1")

	require.GreaterOrEqual(t, folder, 0, "the folder is made")
	assert.Less(t, folder, marker, "the folder comes before the marker")
	assert.Less(t, marker, stop, "the marker comes before the stop")
}

func TestSSH_ALostJobHasNoProofWhileTheLinkIsDownAndAProofOnceTheStopAnswers(t *testing.T) {
	t.Parallel()

	m := machineWith(nil, nil)
	h, bank, _ := newHost(t, m, config())

	id, err := h.Submit(t.Context(), model.JobSpec{Name: "x", Command: "x"})
	require.NoError(t, err)

	m.Drops = -1

	proof, err := h.LostProof(t.Context(), id)

	require.NoError(t, err, "a dropped link is no proof and no error")
	assert.Empty(t, proof.Kind)
	assert.Contains(t, proof.Note, link.ErrDropped.Error(), "the note holds the dropped link")
	assert.False(t, m.Units[string(id)].Stopped, "no stop reached the machine")

	m.Drops = 0

	proof, err = h.LostProof(t.Context(), id)

	require.NoError(t, err)
	assert.Equal(t, model.LostStopped, proof.Kind)
	assert.True(t, m.Units[string(id)].Stopped, "the unit is stopped")
	assert.True(t, m.Units[string(id)].Reset, "the unit is released so no failed unit stays")
	assert.True(t, m.Files[stateFolderPath+"/"+string(id)+".canceled"], "the marker comes with the stop")

	again, err := h.LostProof(t.Context(), id)

	require.NoError(t, err, "the proof is safe to read again")
	assert.Equal(t, model.LostStopped, again.Kind)
	assert.Empty(t, bank.calls, "the proof never banks")
}

func TestSSH_ALostJobWhoseStopFailsHasNoProofAndAnIdThatIsNotAUnitIsAnError(t *testing.T) {
	t.Parallel()

	for name, fail := range map[string]map[string]int{
		"the folder": {"mkdir": failed},
		"the marker": {"touch": failed},
		"the stop":   {"systemctl --user stop": failed},
	} {
		m := machineWith(nil, nil)
		h, _, _ := newHost(t, m, config())

		id, err := h.Submit(t.Context(), model.JobSpec{Name: "x", Command: "x"})
		require.NoError(t, err, name)

		m.Fail = fail

		proof, err := h.LostProof(t.Context(), id)

		require.NoError(t, err, name)
		assert.Empty(t, proof.Kind, "%s: a step that fails is no proof", name)
		assert.Contains(t, proof.Note, "no stop answered", "%s: the note says the stop failed", name)
	}

	h, _, _ := newHost(t, machineWith(nil, nil), config())

	_, err := h.LostProof(t.Context(), "not a unit")

	require.Error(t, err)
}

func TestSSH_AUnitThatCannotBeReleasedAfterTheStopIsStillProvedStopped(t *testing.T) {
	t.Parallel()

	m := machineWith(nil, nil)
	h, _, _ := newHost(t, m, config())

	id, err := h.Submit(t.Context(), model.JobSpec{Name: "x", Command: "x"})
	require.NoError(t, err)

	m.Fail = map[string]int{"systemctl --user reset-failed": dropped}

	proof, err := h.LostProof(t.Context(), id)

	require.NoError(t, err)
	assert.Equal(t, model.LostStopped, proof.Kind, "the stop answered, so the unit does not run")
	assert.Contains(t, proof.Note, "not released")
	assert.Contains(t, proof.Note, link.ErrDropped.Error())
}
