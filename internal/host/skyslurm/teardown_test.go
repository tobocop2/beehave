package skyslurm

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/host"
	"github.com/tobocop2/beehave/internal/host/skypilot"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc/proctest"
)

func newTeardown(t *testing.T, private string) (*skypilot.Host, *proctest.Scripted, *proctest.Scripted) {
	t.Helper()

	h, sky, ssh := newBankingWithSky(t, private)

	ssh.Plan("/home/lane/repo/.git\x00", "find")
	ssh.Plan("", "for-each-ref")
	ssh.Plan("", "worktree", "list")
	ssh.PlanResult(model.CommandResult{ExitCode: 1}, "config", "--get-regexp")
	sky.Plan("", "down")
	sky.Plan(`[{"name": "other", "status": "UP"}]`, "status")

	return h, sky, ssh
}

func TestTeardown_AClusterThatSkyStillListsAfterTheDeleteIsNotConfirmedGone(t *testing.T) {
	t.Parallel()

	h, sky, _ := newTeardown(t, "")
	sky.Plan(`[{"name": "lane-host", "status": "UP"}]`, "status")

	err := h.Teardown(deadline(t))

	require.ErrorIs(t, err, host.ErrNotDeleted)
}

func TestTeardown_BanksOnTheHeadNodeThenDeletesTheCluster(t *testing.T) {
	t.Parallel()

	h, sky, ssh := newTeardown(t, "")

	require.NoError(t, h.Teardown(deadline(t)))
	assert.True(t, ssh.Called("lane-host", "git", "-C", "/home/lane/repo", "worktree", "list", "--porcelain"), "git runs on the head node")
	assert.Equal(t, [][]string{{"down", "-y", "lane-host"}, {"status", "-r", "-o", "json"}}, sky.Calls, "the delete is followed by a read of the status")
}

func TestTeardown_KeepsTheClusterWhileTheHeadNodeHoldsUnbankedWork(t *testing.T) {
	t.Parallel()

	h, sky, ssh := newTeardown(t, "")
	ssh.PlanContaining("commit\t1111111111111111111111111111111111111111\t\t\trefs/heads/main\n", "%(objecttype)")

	err := h.Teardown(deadline(t))

	require.ErrorIs(t, err, host.ErrUnbanked)
	assert.Empty(t, sky.Calls, "sky down does not run")
}
