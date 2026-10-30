package skypilot

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/host"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/proc/proctest"
)

func newTeardown(t *testing.T, private string) (*Host, *proctest.Scripted, *proctest.Scripted) {
	t.Helper()

	h, ssh := newBanking(t, private)
	sky := &proctest.Scripted{}
	h.sky = sky

	ssh.Plan("/home/lane/repo/.git\x00", "find")
	ssh.Plan("", "for-each-ref")
	ssh.Plan("", "worktree", "list")
	ssh.PlanResult(model.CommandResult{ExitCode: 1}, "config", "--get-regexp")
	sky.Plan("", "down")

	return h, sky, ssh
}

func TestTeardown_BanksOnTheHeadNodeThenDeletesTheCluster(t *testing.T) {
	t.Parallel()

	h, sky, ssh := newTeardown(t, "")

	require.NoError(t, h.Teardown(deadline(t)))
	assert.True(t, ssh.Called("lane-host", "git", "-C", "/home/lane/repo", "worktree", "list", "--porcelain"), "git runs on the head node")
	assert.Equal(t, [][]string{{"down", "-y", "lane-host"}}, sky.Calls)
}

func TestTeardown_KeepsTheClusterWhileTheHeadNodeHoldsUnbankedWork(t *testing.T) {
	t.Parallel()

	h, sky, ssh := newTeardown(t, "")
	ssh.PlanContaining("1111111111111111111111111111111111111111  refs/heads/main\n", "%(*objectname)")

	err := h.Teardown(deadline(t))

	require.ErrorIs(t, err, host.ErrUnbanked)
	assert.Empty(t, sky.Calls, "sky down does not run")
}
