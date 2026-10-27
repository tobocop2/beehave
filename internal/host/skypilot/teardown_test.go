package skypilot

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/host"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/proc/proctest"
)

// newTeardown returns a host whose scripted ssh holds one repository, and its scripted sky.
func newTeardown(t *testing.T, private string) (*Host, *proctest.Scripted, *proctest.Scripted) {
	t.Helper()

	sky, ssh := &proctest.Scripted{}, &proctest.Scripted{}
	cfg := config()
	cfg.PrivateRemote = private

	h, err := New("lane-host", cfg, sky, ssh)
	require.NoError(t, err)

	planHome(ssh)
	ssh.Plan("/home/lane/repo/.git\x00", "find")
	ssh.Plan("", "for-each-ref")
	ssh.Plan("", "worktree", "list")
	ssh.PlanResult(model.CommandResult{ExitCode: 1}, "config", "--get-regexp")

	return h, sky, ssh
}

func TestTeardown_BanksTheWorkOfTheHeadNodeThenDeletesTheCluster(t *testing.T) {
	t.Parallel()

	h, sky, ssh := newTeardown(t, "git@private.test:org/bank.git")
	sky.Plan("", "down")

	require.NoError(t, h.Teardown(deadline(t)))
	assert.True(t, ssh.Called("lane-host", "git", "-C", "/home/lane/repo", "worktree", "list", "--porcelain"), "git runs on the head node")
	assert.Equal(t, [][]string{{"down", "-y", "lane-host"}}, sky.Calls)
}

func TestTeardown_KeepsTheClusterWhileTheHostHoldsUnbankedWork(t *testing.T) {
	t.Parallel()

	h, sky, ssh := newTeardown(t, "")
	ssh.PlanContaining("1111111111111111111111111111111111111111  refs/heads/main\n", "%(*objectname)")

	err := h.Teardown(deadline(t))

	require.ErrorIs(t, err, host.ErrUnbanked)
	assert.Empty(t, sky.Calls, "a host with unbanked work stays up")
}
