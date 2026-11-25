package ssh_test

import (
	"context"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/host/hosttest"
	"github.com/tobocop2/beehave/internal/host/link"
	"github.com/tobocop2/beehave/internal/host/ssh"
	"github.com/tobocop2/beehave/internal/host/sshtest"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc"
)

// sshConfig answers `ssh -G` with the agent setting given and passes every other call to the machine.
type sshConfig struct {
	forwardAgent string
	machine      proc.Runner
	asked        int
}

func (c *sshConfig) Run(ctx context.Context, args []string) model.CommandResult {
	if args[0] == "-G" {
		c.asked++

		return model.CommandResult{Stdout: "forwardagent " + c.forwardAgent + "\n"}
	}

	return c.machine.Run(ctx, args)
}

func declaredNone() model.HostConfig {
	cfg := config()
	cfg.Credentials = model.HostCredentialsNone

	return cfg
}

func TestUp_AHostDeclaredCredentialsNoneThatGetsTheAgentIsRefusedBeforeAnyCallReachesIt(t *testing.T) {
	t.Parallel()

	m := sshtest.New(hosttest.Working)
	runner := &sshConfig{forwardAgent: "yes", machine: m.SSH()}

	h, err := ssh.New("gates", declaredNone(), runner)
	require.NoError(t, err)

	err = h.Up(t.Context())

	require.ErrorIs(t, err, link.ErrConfig)
	require.ErrorContains(t, err, "forwardagent yes")
	assert.Equal(t, 1, runner.asked)
	assert.Empty(t, m.Calls, "no call reaches the machine")
}

func TestUp_AHostDeclaredCredentialsNoneThatPassesGetsNoAgentAndNoForwardOnEveryCall(t *testing.T) {
	t.Parallel()

	m := sshtest.New(hosttest.Working)
	runner := &sshConfig{forwardAgent: "no", machine: m.SSH()}

	h, err := ssh.New("gates", declaredNone(), runner)
	require.NoError(t, err)

	require.NoError(t, h.Up(t.Context()))
	assert.Equal(t, 1, runner.asked)
	require.NotEmpty(t, m.Options)

	for _, options := range m.Options {
		assert.True(t, slices.Contains(options, "ForwardAgent=no"), options)
		assert.True(t, slices.Contains(options, "ClearAllForwardings=yes"), options)
	}
}

func TestUp_AHostWithoutTheDeclarationRunsNoSshG(t *testing.T) {
	t.Parallel()

	m := sshtest.New(hosttest.Working)
	runner := &sshConfig{forwardAgent: "yes", machine: m.SSH()}

	h, err := ssh.New("gates", config(), runner)
	require.NoError(t, err)

	require.NoError(t, h.Up(t.Context()))
	assert.Zero(t, runner.asked)
	assert.False(t, slices.Contains(m.Options[0], "ForwardAgent=no"))
}
