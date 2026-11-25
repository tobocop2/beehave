package link_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/host/link"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc/proctest"
)

// sshG is the part of an `ssh -G` answer that does not change between the cases: the locale variables of the system config of macOS.
const sshG = "user ci\nhostname 10.9.9.9\nbatchmode yes\nclearallforwardings no\nsendenv LANG\nsendenv LC_*\n"

func TestWithCredentials_NoneForcesNoAgentAndNoForwardOnEveryCallAndLeavesTheLinkItCopies(t *testing.T) {
	t.Parallel()

	ssh := proctest.Always(model.CommandResult{})

	machine, err := link.ForMachine("box", link.Options{Address: "10.9.9.9"}, ssh)
	require.NoError(t, err)

	_, err = machine.WithCredentials(model.HostCredentialsNone).Remote(t.Context(), "true", "true")
	require.NoError(t, err)
	_, err = machine.Remote(t.Context(), "true", "true")
	require.NoError(t, err)
	_, err = link.New("cluster", ssh).WithCredentials(model.HostCredentialsNone).Remote(t.Context(), "true", "true")
	require.NoError(t, err)

	options := []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=15", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=3", "-o", "HostName=10.9.9.9", "-o", "StrictHostKeyChecking=yes"}
	forced := []string{"-o", "ForwardAgent=no", "-o", "ClearAllForwardings=yes"}

	assert.Equal(t, [][]string{
		append(append(append([]string{}, options...), forced...), "--", "box", "true"),
		append(append([]string{}, options...), "--", "box", "true"),
		append(append([]string{"-o", "BatchMode=yes"}, forced...), "cluster", "true"),
	}, ssh.Calls)
}

func TestCheckCredentials_RunsNothingForAHostWithoutTheDeclaration(t *testing.T) {
	t.Parallel()

	ssh := proctest.Always(model.CommandResult{Stdout: "forwardagent yes\n"})

	require.NoError(t, link.New("box", ssh).CheckCredentials(t.Context()))
	assert.Empty(t, ssh.Calls)
}

func TestCheckCredentials_RunsSshGWithTheOptionsOfTheLinkWithoutTheForcedOnes(t *testing.T) {
	t.Parallel()

	ssh := proctest.Always(model.CommandResult{Stdout: sshG + "forwardagent no\n"})

	machine, err := link.ForMachine("box", link.Options{Address: "10.9.9.9", User: "ci"}, ssh)
	require.NoError(t, err)

	require.NoError(t, machine.WithCredentials(model.HostCredentialsNone).CheckCredentials(t.Context()))
	require.NoError(t, link.New("cluster", ssh).WithCredentials(model.HostCredentialsNone).CheckCredentials(t.Context()))

	assert.Equal(t, [][]string{
		{
			"-G", "-o", "BatchMode=yes", "-o", "ConnectTimeout=15", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=3",
			"-o", "HostName=10.9.9.9", "-o", "User=ci", "-o", "StrictHostKeyChecking=yes", "--", "box",
		},
		{"-G", "-o", "BatchMode=yes", "cluster"},
	}, ssh.Calls)
}

func TestCheckCredentials_AcceptsOnlyNoAgentAndLocaleVariablesAndNoSecretSetting(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		answer string
		refuse []string // each part of the refusal; empty means the host passes
	}{
		"agent off":                         {answer: "forwardagent no\n"},
		"agent off and a plain setenv":      {answer: "forwardagent no\nsetenv TERM=xterm\n"},
		"agent on":                          {answer: "forwardagent yes\n", refuse: []string{"forwardagent yes", "box", "credentials = \"none\""}},
		"agent socket path":                 {answer: "forwardagent /tmp/agent.sock\n", refuse: []string{"forwardagent /tmp/agent.sock", "box"}},
		"agent from the variable":           {answer: "forwardagent $SSH_AUTH_SOCK\n", refuse: []string{"forwardagent $SSH_AUTH_SOCK", "box"}},
		"no agent line":                     {answer: "", refuse: []string{"forwardagent", "box"}},
		"a sent variable that is a secret":  {answer: "forwardagent no\nsendenv GH_TOKEN\n", refuse: []string{"sendenv GH_TOKEN", "box", "only LANG and LC_*"}},
		"a sent pattern of every variable":  {answer: "forwardagent no\nsendenv *\n", refuse: []string{"sendenv *", "box"}},
		"a set variable with a secret name": {answer: "forwardagent no\nsetenv GH_TOKEN=ghp_hidden\n", refuse: []string{"setenv GH_TOKEN=...", "box"}},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			ssh := proctest.Always(model.CommandResult{Stdout: sshG + tt.answer})
			err := link.New("box", ssh).WithCredentials(model.HostCredentialsNone).CheckCredentials(t.Context())

			if len(tt.refuse) == 0 {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, link.ErrConfig)

			for _, part := range tt.refuse {
				require.ErrorContains(t, err, part)
			}

			assert.NotContains(t, err.Error(), "ghp_hidden", "a refusal never shows the value of a variable")
		})
	}
}

func TestCheckCredentials_RefusesWhenSshCannotReadItsConfigAndShowsNoTextOfIt(t *testing.T) {
	t.Parallel()

	ssh := proctest.Always(model.CommandResult{ExitCode: 255, Stderr: "line 3: Bad SetEnv GH_TOKEN=ghp_hidden"})
	err := link.New("box", ssh).WithCredentials(model.HostCredentialsNone).CheckCredentials(t.Context())

	require.ErrorIs(t, err, link.ErrConfig)
	require.ErrorContains(t, err, "`ssh -G box` exited 255")
	assert.NotContains(t, err.Error(), "ghp_hidden")
}
