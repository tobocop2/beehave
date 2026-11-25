package slurm

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/host/hosttest"
	"github.com/tobocop2/beehave/internal/host/link"
	"github.com/tobocop2/beehave/internal/host/slurmtest"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc"
)

// sshConfig answers `ssh -G` with the agent setting given and passes every other call to the cluster.
type sshConfig struct {
	forwardAgent string
	cluster      proc.Runner
}

func (c sshConfig) Run(ctx context.Context, args []string) model.CommandResult {
	if args[0] == "-G" {
		return model.CommandResult{Stdout: "forwardagent " + c.forwardAgent + "\n"}
	}

	return c.cluster.Run(ctx, args)
}

// headNodeDeclaredNone returns a head node declared credentials = "none" whose ssh config gives the agent setting, and the spy of its calls.
func headNodeDeclaredNone(t *testing.T, forwardAgent string) (*Host, *spy) {
	t.Helper()

	f := slurmtest.New(hosttest.Working)
	f.Up = true
	s := &spy{inner: sshConfig{forwardAgent: forwardAgent, cluster: f.SSH()}}
	cfg := hostConfig()
	cfg.Credentials = model.HostCredentialsNone

	h, err := NewHost(slurmtest.Target, cfg, s, fastClock())
	require.NoError(t, err)

	return h, s
}

func TestUp_AHeadNodeDeclaredCredentialsNoneThatGetsTheAgentIsRefusedBeforeAnyCallReachesIt(t *testing.T) {
	t.Parallel()

	h, s := headNodeDeclaredNone(t, "/tmp/agent.sock")

	err := h.Up(t.Context())

	require.ErrorIs(t, err, link.ErrConfig)
	require.ErrorContains(t, err, "forwardagent /tmp/agent.sock")
	require.Len(t, s.calls, 1, "the only call is ssh -G")
	assert.True(t, strings.HasPrefix(s.calls[0], "-G "), s.calls[0])
}

func TestUp_AHeadNodeDeclaredCredentialsNoneThatPassesGetsNoAgentAndNoForwardOnEveryCall(t *testing.T) {
	t.Parallel()

	h, s := headNodeDeclaredNone(t, "no")

	require.NoError(t, h.Up(t.Context()))
	require.Greater(t, len(s.calls), 1)
	assert.True(t, strings.HasPrefix(s.calls[0], "-G "), "the check comes first")
	assert.NotContains(t, s.calls[0], "ForwardAgent=no", "the check reads the config of the owner, without the forced options")

	for _, call := range s.calls[1:] {
		assert.Contains(t, call, "-o ForwardAgent=no -o ClearAllForwardings=yes --", call)
	}
}
