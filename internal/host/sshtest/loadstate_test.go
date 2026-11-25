package sshtest_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehave/internal/host/hosttest"
	"github.com/tobocop2/beehave/internal/host/sshtest"
)

func TestMachine_ShowsTheLoadStateOfAUnitThatSystemdHoldsAndNotFoundForAnyOther(t *testing.T) {
	t.Parallel()

	m := sshtest.New(hosttest.Working)
	show := func(unit string) string {
		return call(m, "systemctl", "--user", "show", "-p", "LoadState", "--value", unit).Stdout
	}

	assert.Equal(t, "not-found\n", show("beehave-a"), "no unit was started")

	call(m, "systemd-run", "--user", "--unit=beehave-a", "--", "sh", "-c", "true")
	assert.Equal(t, "loaded\n", show("beehave-a"))

	m.Units["beehave-a"].Vanished = true
	assert.Equal(t, "not-found\n", show("beehave-a"), "systemd dropped the unit")
}
