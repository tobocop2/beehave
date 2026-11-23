package skypilot

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/host/link"
	"github.com/tobocop2/beehave/internal/model"
)

func TestUp_RefusesAClusterWhoseHeadNodeHasNoFlock(t *testing.T) {
	t.Parallel()

	h, _, sky, ssh := newHost(t, hostConfig(nil))
	sky.Plan("", "launch")
	ssh.PlanResult(model.CommandResult{ExitCode: 127, Stderr: "flock: command not found"}, "flock")

	err := h.Up(context.Background())

	require.ErrorIs(t, err, link.ErrNoFlock)
	assert.ErrorContains(t, err, "Install util-linux (flock)")
}

func TestUp_ChecksFlockOnTheHeadNodeAfterTheClusterLaunches(t *testing.T) {
	t.Parallel()

	h, _, sky, ssh := newHost(t, hostConfig(nil))
	sky.Plan("", "launch")

	require.NoError(t, h.Up(context.Background()))
	assert.True(t, ssh.Called("flock", "--version"))
}
