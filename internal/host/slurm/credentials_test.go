package slurm

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/host/hosttest"
	"github.com/tobocop2/beehest/internal/host/link"
	"github.com/tobocop2/beehest/internal/host/slurmtest"
	"github.com/tobocop2/beehest/internal/model"
)

// headNodeDeclaredNone returns a head node declared credentials = "none" whose `ssh -G` gives the answer, and the spy of its calls.
func headNodeDeclaredNone(t *testing.T, answer string) (*Host, *spy) {
	t.Helper()

	f := slurmtest.New(hosttest.Working)
	f.Up = true
	s := &spy{inner: &hosttest.SSHConfig{Answer: answer, Next: f.SSH()}}
	cfg := hostConfig()
	cfg.Credentials = model.HostCredentialsNone

	h, err := NewHost(slurmtest.Target, cfg, s, fastClock())
	require.NoError(t, err)

	return h, s
}

func TestUp_AHeadNodeDeclaredCredentialsNoneThatGetsTheDisplayIsRefusedBeforeAnyCallReachesIt(t *testing.T) {
	t.Parallel()

	h, s := headNodeDeclaredNone(t, "forwardagent no\nforwardx11 yes\n")

	err := h.Up(t.Context())

	require.ErrorIs(t, err, link.ErrConfig)
	require.ErrorContains(t, err, "forwardx11 yes")
	require.Len(t, s.calls, 1, "the only call is ssh -G")
	assert.True(t, strings.HasPrefix(s.calls[0], "-G "), s.calls[0])
}

func TestUp_AHeadNodeDeclaredCredentialsNoneThatPassesGetsEveryCredentialOptionOnEveryCall(t *testing.T) {
	t.Parallel()

	h, s := headNodeDeclaredNone(t, hosttest.CleanSSHConfig)

	require.NoError(t, h.Up(t.Context()))
	require.Greater(t, len(s.calls), 1)
	assert.True(t, strings.HasPrefix(s.calls[0], "-G "), "the check comes first")
	assert.NotContains(t, s.calls[0], "ForwardAgent=no", "the check reads the config of the owner, without the forced options")

	forced := "-o ForwardAgent=no -o ClearAllForwardings=yes -o ForwardX11=no -o ForwardX11Trusted=no -o ControlMaster=no -o ControlPath=none -o GSSAPIDelegateCredentials=no --"
	for _, call := range s.calls[1:] {
		assert.Contains(t, call, forced, call)
	}
}
