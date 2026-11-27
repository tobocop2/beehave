package ssh_test

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/host/hosttest"
	"github.com/tobocop2/beehest/internal/host/link"
	"github.com/tobocop2/beehest/internal/host/ssh"
	"github.com/tobocop2/beehest/internal/host/sshtest"
	"github.com/tobocop2/beehest/internal/model"
)

func declaredNone() model.HostConfig {
	cfg := config()
	cfg.Credentials = model.HostCredentialsNone

	return cfg
}

func TestUp_AHostDeclaredCredentialsNoneThatGetsTheAgentIsRefusedBeforeAnyCallReachesIt(t *testing.T) {
	t.Parallel()

	m := sshtest.New(hosttest.Working)
	runner := &hosttest.SSHConfig{Answer: "forwardagent yes\nforwardx11 no\n", Next: m.SSH()}

	h, err := ssh.New("gates", declaredNone(), runner)
	require.NoError(t, err)

	err = h.Up(t.Context())

	require.ErrorIs(t, err, link.ErrConfig)
	require.ErrorContains(t, err, "forwardagent yes")
	assert.Equal(t, 1, runner.Asked)
	assert.Empty(t, m.Calls, "no call reaches the machine")
}

func TestUp_AHostDeclaredCredentialsNoneThatPassesGetsEveryCredentialOptionOnEveryCall(t *testing.T) {
	t.Parallel()

	m := sshtest.New(hosttest.Working)
	runner := &hosttest.SSHConfig{Answer: hosttest.CleanSSHConfig, Next: m.SSH()}

	h, err := ssh.New("gates", declaredNone(), runner)
	require.NoError(t, err)

	require.NoError(t, h.Up(t.Context()))
	assert.Equal(t, 1, runner.Asked)
	require.NotEmpty(t, m.Options)

	for _, options := range m.Options {
		for _, forced := range []string{"ForwardAgent=no", "ClearAllForwardings=yes", "ForwardX11=no", "ForwardX11Trusted=no", "ControlMaster=no", "ControlPath=none", "GSSAPIDelegateCredentials=no"} {
			assert.True(t, slices.Contains(options, forced), "%s in %v", forced, options)
		}
	}
}

func TestUp_AHostWithoutTheDeclarationRunsNoSshG(t *testing.T) {
	t.Parallel()

	m := sshtest.New(hosttest.Working)
	runner := &hosttest.SSHConfig{Answer: "forwardagent yes\n", Next: m.SSH()}

	h, err := ssh.New("gates", config(), runner)
	require.NoError(t, err)

	require.NoError(t, h.Up(t.Context()))
	assert.Zero(t, runner.Asked)
	assert.False(t, slices.Contains(m.Options[0], "ForwardAgent=no"))
}
