package sshtest_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehest/internal/host/hosttest"
	"github.com/tobocop2/beehest/internal/host/sshtest"
)

func TestMachine_FlockAnswersTheVersionUnlessThePlanTakesItAway(t *testing.T) {
	t.Parallel()

	m := sshtest.New(hosttest.Working)

	assert.Contains(t, call(m, "flock", "--version").Stdout, "util-linux")

	m.NoFlock = true

	assert.Equal(t, 127, call(m, "flock", "--version").ExitCode)
}

func TestMachine_ThePollPrintsTheWaitingLineOnlyWhileTheMarkerExists(t *testing.T) {
	t.Parallel()

	m := sshtest.New(hosttest.Working)
	submit(m, "u1", hosttest.PassingCommand)

	assert.NotContains(t, observe(m, "u1"), "beehest-waiting")

	m.Files["/b/u1.waiting"] = true

	assert.Contains(t, observe(m, "u1"), "beehest-waiting")
}
