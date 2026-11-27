package ssh_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/host/hosttest"
	"github.com/tobocop2/beehest/internal/host/sshtest"
	"github.com/tobocop2/beehest/internal/model"
)

func TestSubmit_ARetryOfAFailedJobUnderTheKeyOfTheNextTryStartsANewUnit(t *testing.T) {
	t.Parallel()

	m := sshtest.New(hosttest.Working)
	h, _, _ := newHost(t, m, config())

	first, err := h.Submit(t.Context(), model.JobSpec{Name: "gate", Command: hosttest.FailingCommand, Key: "run-1:4:1"})
	require.NoError(t, err)

	state, err := h.Wait(t.Context(), first)
	require.NoError(t, err)
	require.Equal(t, model.JobFailed, state)

	second, err := h.Submit(t.Context(), model.JobSpec{Name: "gate", Command: hosttest.PassingCommand, Key: "run-1:4:2"})

	require.NoError(t, err)
	assert.NotEqual(t, first, second)
	assert.Equal(t, 2, calledWith(m, systemdRunCall))
}

func TestSubmit_ACrashBeforeTheWaitFindsTheFailedUnitOfTheTryAndStartsNothingAgain(t *testing.T) {
	t.Parallel()

	m := sshtest.New(hosttest.Working)
	h, _, _ := newHost(t, m, config())
	spec := model.JobSpec{Name: "gate", Command: hosttest.FailingCommand, Key: "run-1:4:1"}

	first, err := h.Submit(t.Context(), spec)
	require.NoError(t, err)

	again, err := h.Submit(t.Context(), spec)

	require.NoError(t, err)
	assert.Equal(t, first, again)
	assert.Equal(t, 1, calledWith(m, systemdRunCall))
}
