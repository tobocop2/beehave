package ssh_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/host/hosttest"
	"github.com/tobocop2/beehave/internal/host/sshtest"
	"github.com/tobocop2/beehave/internal/model"
)

const systemdRunCall = "systemd-run --user"

func TestSubmit_ASubmitRetriedAfterACrashFindsTheUnitTheHostAlreadyStarted(t *testing.T) {
	t.Parallel()

	m := sshtest.New(hosttest.Working)
	h, _, _ := newHost(t, m, config())
	spec := model.JobSpec{Name: "gate", Command: "make check", Key: "run-1:4"}

	first, err := h.Submit(t.Context(), spec)
	require.NoError(t, err)

	// The engine crashed before it recorded the id. The replay submits again with the same key.
	second, err := h.Submit(t.Context(), spec)

	require.NoError(t, err)
	assert.Equal(t, first, second, "the retry returns the job that the first submit started")
	assert.Equal(t, 1, calledWith(m, systemdRunCall), "the host started the job once")
	assert.Len(t, m.Units, 1)
}

func TestSubmit_TwoCallsWithTwoKeysStartTwoUnitsAndTwoCallsWithNoKeyStartTwoToo(t *testing.T) {
	t.Parallel()

	m := sshtest.New(hosttest.Working)
	h, _, _ := newHost(t, m, config())

	a, err := h.Submit(t.Context(), model.JobSpec{Name: "gate", Command: "x", Key: "run-1:4"})
	require.NoError(t, err)

	b, err := h.Submit(t.Context(), model.JobSpec{Name: "gate", Command: "x", Key: "run-1:5"})
	require.NoError(t, err)

	c, err := h.Submit(t.Context(), model.JobSpec{Name: "gate", Command: "x"})
	require.NoError(t, err)

	d, err := h.Submit(t.Context(), model.JobSpec{Name: "gate", Command: "x"})
	require.NoError(t, err)

	assert.Len(t, map[model.JobID]bool{a: true, b: true, c: true, d: true}, 4)
	assert.Equal(t, 4, calledWith(m, systemdRunCall))
}

func TestSubmit_AKeyedJobThatTheHostNoLongerHoldsStartsAgain(t *testing.T) {
	t.Parallel()

	m := sshtest.New(hosttest.Working)
	h, _, _ := newHost(t, m, config())
	spec := model.JobSpec{Name: "gate", Command: "x", Key: "run-1:4"}

	first, err := h.Submit(t.Context(), spec)
	require.NoError(t, err)

	m.Units[string(first)].Vanished = true

	second, err := h.Submit(t.Context(), spec)

	require.NoError(t, err)
	assert.Equal(t, first, second, "the unit name is a function of the key")
	assert.Equal(t, 2, calledWith(m, systemdRunCall), "a unit that is gone is started again")
}

func TestSubmit_AFailedLookupOfTheKeyedUnitStartsNothing(t *testing.T) {
	t.Parallel()

	m := sshtest.New(hosttest.Working)
	m.Fail = map[string]int{"systemctl --user show": 1}
	h, _, _ := newHost(t, m, config())

	_, err := h.Submit(t.Context(), model.JobSpec{Name: "gate", Command: "x", Key: "run-1:4"})

	require.Error(t, err)
	assert.Zero(t, calledWith(m, systemdRunCall), "a host that cannot say whether the job exists gets no second copy")
}
