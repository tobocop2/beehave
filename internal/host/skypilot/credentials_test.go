package skypilot

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/host/hosttest"
	"github.com/tobocop2/beehave/internal/host/link"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc/proctest"
)

func declaredNone() model.HostConfig {
	cfg := hostConfig(nil)
	cfg.Credentials = model.HostCredentialsNone

	return cfg
}

func TestUp_AClusterDeclaredCredentialsNoneIsCheckedAfterTheLaunchAndBeforeAnyCallReachesIt(t *testing.T) {
	t.Parallel()

	h, _, sky, ssh := newHost(t, declaredNone())
	sky.Plan("", "launch")
	ssh.Plan("forwardagent yes\nforwardx11 no\n", "-G")

	err := h.Up(t.Context())

	require.ErrorIs(t, err, link.ErrConfig)
	require.ErrorContains(t, err, "forwardagent yes")
	assert.True(t, sky.Called("launch"))
	assert.Equal(t, [][]string{{"-G", "-o", "BatchMode=yes", clusterName}}, ssh.Calls, "the check is the only ssh call")
}

func TestUp_AClusterThatFailsToLaunchRunsNoCheck(t *testing.T) {
	t.Parallel()

	h, _, sky, ssh := newHost(t, declaredNone())
	sky.PlanFail("launch")

	require.Error(t, h.Up(t.Context()))
	assert.Empty(t, ssh.Calls, "the ssh config entry of the cluster exists only after the launch")
}

func TestUp_AClusterDeclaredCredentialsNoneThatPassesGetsEveryCredentialOptionOnTheQueueLink(t *testing.T) {
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

	forced := []string{
		"-o", "BatchMode=yes", "-o", "ForwardAgent=no", "-o", "ClearAllForwardings=yes", "-o", "ForwardX11=no", "-o", "ForwardX11Trusted=no",
		"-o", "ControlMaster=no", "-o", "ControlPath=none", "-o", "GSSAPIDelegateCredentials=no", clusterName,
	}
	assert.Equal(t, [][]string{
		{"-G", "-o", "BatchMode=yes", clusterName},
		append(append([]string{}, forced...), model.ShellQuote("flock"), model.ShellQuote("--version")),
		append(append([]string{}, forced...), model.ShellQuote("true")),
	}, ssh.Calls)
}
