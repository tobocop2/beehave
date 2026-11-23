package gitexec_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehave/internal/proc/gitexec"
)

func TestOwnerEnv_KeepsTheNetworkNamesAndSwitchesEveryConfigFileOffForACallToAnyRemote(t *testing.T) {
	t.Parallel()

	base := map[string]string{"PATH": "/bin", "GIT_SSL_CAINFO": "/ca.pem", "HTTPS_PROXY": "http://p:1", "HOME": "/home/o", "SSH_AUTH_SOCK": "/s", "GH_TOKEN": "t", "GIT_SSH_COMMAND": "evil"}

	env := gitexec.OwnerEnv(base, false, "GIT_DIR=/x")

	assert.ElementsMatch(t, []string{
		"PATH=/bin", "GIT_SSL_CAINFO=/ca.pem", "HTTPS_PROXY=http://p:1", "GIT_DIR=/x",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_NO_REPLACE_OBJECTS=1", "GIT_TERMINAL_PROMPT=0", "XDG_CONFIG_HOME=/dev/null",
	}, env, "no home folder, no agent socket, no token and no git variable of the process")
}

func TestOwnerEnv_KeepsTheHomeFolderAndTheAgentSocketForAnSshRemote(t *testing.T) {
	t.Parallel()

	env := gitexec.OwnerEnv(map[string]string{"HOME": "/home/o", "SSH_AUTH_SOCK": "/s"}, true)

	assert.Contains(t, env, "HOME=/home/o")
	assert.Contains(t, env, "SSH_AUTH_SOCK=/s")
	assert.Contains(t, env, "GIT_CONFIG_GLOBAL=/dev/null")
}

func TestOwnerEnv_AnSshRemoteWithNoSocketInTheBaseGetsNone(t *testing.T) {
	t.Parallel()

	assert.NotContains(t, gitexec.OwnerEnv(map[string]string{"PATH": "/bin"}, true), "SSH_AUTH_SOCK=")
}
