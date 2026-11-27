package link_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/host/link"
	"github.com/tobocop2/beehest/internal/model"
	"github.com/tobocop2/beehest/internal/proc/proctest"
)

func TestCheckFlock_PassesWhenTheHostAnswersTheVersion(t *testing.T) {
	t.Parallel()

	ssh := proctest.Always(model.CommandResult{Stdout: "flock from util-linux 2.39\n"})

	require.NoError(t, link.New("head", ssh).CheckFlock(t.Context()))
	assert.True(t, ssh.Called("flock", "--version"))
}

func TestCheckFlock_RefusesAHostWithNoFlockAndNamesTheHostAndThePackage(t *testing.T) {
	t.Parallel()

	err := link.New("head", proctest.Always(model.CommandResult{ExitCode: 127})).CheckFlock(t.Context())

	require.ErrorIs(t, err, link.ErrNoFlock)
	require.ErrorContains(t, err, "head")
	require.ErrorContains(t, err, "Install util-linux (flock)")
}

func TestCheckFlock_ReportsADroppedLinkAsADroppedLinkAndNotAsAMissingFlock(t *testing.T) {
	t.Parallel()

	err := link.New("head", proctest.Always(model.CommandResult{ExitCode: 255, Stderr: "no route"})).CheckFlock(t.Context())

	require.ErrorIs(t, err, link.ErrDropped)
	assert.NotErrorIs(t, err, link.ErrNoFlock)
}
