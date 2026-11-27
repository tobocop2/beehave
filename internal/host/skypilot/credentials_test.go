package skypilot

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/host/hosttest"
	"github.com/tobocop2/beehest/internal/host/link"
	"github.com/tobocop2/beehest/internal/model"
	"github.com/tobocop2/beehest/internal/proc/proctest"
)

// declaredNone is a config that the loader refuses for this provider. The tests below pass it to the host directly to prove that the host keeps no promise of its own.
func declaredNone() model.HostConfig {
	cfg := hostConfig(nil)
	cfg.Credentials = model.HostCredentialsNone

	return cfg
}

func TestUp_AClusterRunsNoSshGAfterTheLaunchWhateverItsCredentialsValueIs(t *testing.T) {
	t.Parallel()

	h, _, sky, ssh := newHost(t, declaredNone())
	sky.Plan("", "launch")
	ssh.Plan("forwardagent yes\nforwardx11 no\n", "-G")

	require.NoError(t, h.Up(t.Context()))

	assert.True(t, sky.Called("launch"))
	require.Len(t, ssh.Calls, 1, "the flock version read is the only ssh call")
	assert.NotContains(t, ssh.Calls[0], "-G")
}

func TestUp_ASetupCommandOfAClusterRunsInTheLaunchAndNoSshGFollowsIt(t *testing.T) {
	t.Parallel()

	cfg := declaredNone()
	cfg.SetupCommand = "make setup"
	sky := &taskReader{Scripted: &proctest.Scripted{}}
	sky.Plan("", "launch")

	ssh := &proctest.Scripted{}
	ssh.Plan("forwardagent yes\nforwardx11 no\n", "-G")
	ssh.Plan("flock from util-linux 2.39.3\n", "flock", "--version")

	h, err := New(clusterName, cfg, sky, ssh, func(*link.Link, model.HostConfig, Slot) Queue { return &fakeQueue{} },
		WithCheckout("beehest", "main", "https://example.test/o/n.git"))
	require.NoError(t, err)

	require.NoError(t, h.Up(t.Context()))
	require.Len(t, sky.Calls, 1)
	assert.Equal(t, sky.path, sky.Calls[0][len(sky.Calls[0])-1], "SkyPilot runs the set-up in the launch, so no ssh call of the link carries it")
	assert.Contains(t, sky.text, "make setup")

	for _, call := range ssh.Calls {
		assert.NotContains(t, call, "-G")
	}
}

func TestUp_AClusterThatFailsToLaunchRunsNoSshCall(t *testing.T) {
	t.Parallel()

	h, _, sky, ssh := newHost(t, hostConfig(nil))
	sky.PlanFail("launch")

	require.Error(t, h.Up(t.Context()))
	assert.Empty(t, ssh.Calls, "the ssh config entry of the cluster exists only after the launch")
}

func TestUp_TheQueueLinkOfAClusterForcesNoCredentialOptionOnAnyCall(t *testing.T) {
	t.Parallel()

	sky, ssh := &proctest.Scripted{}, &proctest.Scripted{}
	sky.Plan("", "launch")
	ssh.Plan(hosttest.CleanSSHConfig, "-G")
	ssh.Plan("flock from util-linux 2.39.3\n", "flock", "--version")

	var queueLink *link.Link

	h, err := New(clusterName, declaredNone(), sky, ssh, func(l *link.Link, _ model.HostConfig, _ Slot) Queue {
		queueLink = l

		return &fakeQueue{}
	})
	require.NoError(t, err)
	require.NoError(t, h.Up(t.Context()))

	queueLink.Exec(t.Context(), "true")

	assert.Equal(t, [][]string{
		{"-o", "BatchMode=yes", clusterName, model.ShellQuote("flock"), model.ShellQuote("--version")},
		{"-o", "BatchMode=yes", clusterName, model.ShellQuote("true")},
	}, ssh.Calls)
}
