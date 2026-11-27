package reach_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/exectest"
	"github.com/tobocop2/beehest/internal/model"
	"github.com/tobocop2/beehest/internal/proc"
	"github.com/tobocop2/beehest/internal/proc/gitexec/reach"
)

const writerToken = "writer-token"

// fakeGit puts a fake git first on PATH. A call appends one line to the file that the function returns: the arguments, the token of the owner, the token
// of the writer and the transport allow list as the call sees them.
func fakeGit(t *testing.T) (string, string) {
	t.Helper()

	bin := t.TempDir()
	log := filepath.Join(bin, "calls")
	script := "#!/bin/sh\necho \"$* owner=${GH_TOKEN-unset} writer=${BEEHEST_FORGE_TOKEN-unset} allow=${GIT_ALLOW_PROTOCOL-unset} dir=${GIT_DIR-unset} prompt=${GIT_TERMINAL_PROMPT-unset}\" >> " + log + "\n"
	require.NoError(t, exectest.Write(filepath.Join(bin, "git"), script))

	path := strings.Join([]string{bin, "/usr/bin", "/bin"}, string(os.PathListSeparator))
	t.Setenv("PATH", path)
	t.Setenv("GH_TOKEN", "owner-token")

	return log, path
}

func calls(t *testing.T, log string) string {
	t.Helper()

	data, err := os.ReadFile(log) //nolint:gosec // a file of the test
	require.NoError(t, err)

	return strings.TrimSpace(string(data))
}

func TestRunner_AProjectWithNoCredentialsRunsAsTheOwnerToAnSshRemoteWithNoTokenAndAnyTransport(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	log, _ := fakeGit(t)

	out := reach.Runner(reach.Call{Project: "p", Dir: t.TempDir(), Remote: "ssh://git@example.test/o/r.git", OwnerEnv: []string{"GIT_DIR=/x"}}, nil).
		Run(t.Context(), []string{"fetch"})

	require.Equal(t, 0, out.ExitCode)
	assert.Equal(t, "-c core.hooksPath=/dev/null fetch owner=unset writer=unset allow=unset dir=/x prompt=0", calls(t, log))
}

func TestRunner_AnHttpsRemoteOfAProjectWithNoCredentialsTakesTheTokenOfTheOwnerThroughTheHelperOfTheHostOnly(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	log, _ := fakeGit(t)

	var host string

	out := reach.Runner(reach.Call{
		Project: "p", Dir: t.TempDir(), Remote: "https://example.test:8443/o/r.git", OwnerEnv: []string{"GIT_DIR=/x"},
		OwnerToken: func(h string) (string, error) { host = h; return "gh-token", nil },
	}, nil).Run(t.Context(), []string{"fetch"})

	require.Equal(t, 0, out.ExitCode)
	assert.Equal(t, "example.test:8443", host, "the token is read for the host and port of the address")
	assert.Equal(t, "-c core.hooksPath=/dev/null fetch owner=unset writer=gh-token allow=unset dir=/x prompt=0", calls(t, log))
}

func TestRunner_AnHttpsRemoteOfAProjectWithNoCredentialsIsRefusedWhenTheOwnerHasNoToken(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	log, _ := fakeGit(t)
	failing := func(string) (string, error) { return "", reach.ErrNoOwnerToken }

	for name, token := range map[string]func(string) (string, error){"no function": nil, "a failed read": failing} {
		out := reach.Runner(reach.Call{Project: "p", Dir: t.TempDir(), Remote: "https://example.test/o/r.git", OwnerToken: token}, nil).Run(t.Context(), []string{"fetch"})

		assert.Equal(t, 1, out.ExitCode, name)
		require.ErrorIs(t, out.SetupErr, reach.ErrNoOwnerToken, name)
	}

	assert.NoFileExists(t, log, "a refused call runs no git")
}

func TestRunner_AnOwnerCallKeepsTheNetworkNamesAndTheSshSocketOnlyForAnSshAddress(t *testing.T) {
	bin := t.TempDir()
	log := filepath.Join(bin, "calls")
	script := "#!/bin/sh\necho \"ca=${GIT_SSL_CAINFO-unset} cert=${SSL_CERT_FILE-unset} proxy=${HTTPS_PROXY-unset} sock=${SSH_AUTH_SOCK-unset} gh=${GH_TOKEN-unset}\" >> " + log + "\n"
	require.NoError(t, exectest.Write(filepath.Join(bin, "git"), script))
	t.Setenv("PATH", strings.Join([]string{bin, "/usr/bin", "/bin"}, string(os.PathListSeparator)))
	process := map[string]string{
		"GIT_SSL_CAINFO": "/ca.pem", "SSL_CERT_FILE": "/cert.pem", "HTTPS_PROXY": "http://proxy.test:3128", "SSH_AUTH_SOCK": "/agent.sock", "GH_TOKEN": "owner-token",
	}

	for _, remote := range []string{"https://example.test/o/r.git", "git@example.test:o/r.git", "ssh://git@example.test/o/r.git", "http://example.test/o/r.git", "/srv/r.git"} {
		reach.Runner(reach.Call{Project: "p", Dir: t.TempDir(), Remote: remote, Base: process, OwnerToken: func(string) (string, error) { return "t", nil }}, nil).
			Run(t.Context(), []string{"fetch"})
	}

	sock := "/agent.sock"
	base := "ca=/ca.pem cert=/cert.pem proxy=http://proxy.test:3128 gh=unset"
	assert.Equal(t, strings.Join([]string{
		"ca=/ca.pem cert=/cert.pem proxy=http://proxy.test:3128 sock=unset gh=unset",
		"ca=/ca.pem cert=/cert.pem proxy=http://proxy.test:3128 sock=" + sock + " gh=unset",
		"ca=/ca.pem cert=/cert.pem proxy=http://proxy.test:3128 sock=" + sock + " gh=unset",
		"ca=/ca.pem cert=/cert.pem proxy=http://proxy.test:3128 sock=unset gh=unset",
		"ca=/ca.pem cert=/cert.pem proxy=http://proxy.test:3128 sock=unset gh=unset",
	}, "\n"), calls(t, log), base)
}

func TestRunner_ACallOfTheOwnerStartsWithNoConfigFileAndNoGitVariableOfTheProcess(t *testing.T) {
	bin := t.TempDir()
	log := filepath.Join(bin, "calls")
	script := "#!/bin/sh\necho \"global=${GIT_CONFIG_GLOBAL-unset} system=${GIT_CONFIG_NOSYSTEM-unset} ssh=${GIT_SSH_COMMAND-unset} count=${GIT_CONFIG_COUNT-unset} sock=${SSH_AUTH_SOCK-unset}\" >> " + log + "\n"
	require.NoError(t, exectest.Write(filepath.Join(bin, "git"), script))
	t.Setenv("PATH", strings.Join([]string{bin, "/usr/bin", "/bin"}, string(os.PathListSeparator)))
	process := map[string]string{"GIT_SSH_COMMAND": "evil", "GIT_CONFIG_COUNT": "1", "GIT_CONFIG_GLOBAL": "/home/owner/.gitconfig", "SSH_AUTH_SOCK": "/agent.sock"}

	reach.Runner(reach.Call{Project: "p", Dir: t.TempDir(), Remote: "ssh://git@example.test/o/r.git", Base: process}, nil).Run(t.Context(), []string{"fetch"})

	assert.Equal(t, "global=/dev/null system=1 ssh=unset count=unset sock=/agent.sock", calls(t, log))
}

func TestRunner_AProjectWithLoadedCredentialsRunsAsTheWriterAndNeverAsTheOwner(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	log, path := fakeGit(t)

	out := reach.Runner(reach.Call{
		Project: "p", Dir: t.TempDir(), Remote: "https://example.test/o/r.git", Declared: true, Loaded: true, Token: writerToken,
		Base: map[string]string{"PATH": path}, Pairs: []string{"GIT_DIR=/y"},
	}, func(string) proc.Runner {
		t.Error("the writer call used the runner of the owner")

		return nil
	}).Run(t.Context(), []string{"push"})

	require.Equal(t, 0, out.ExitCode)
	assert.Equal(t, "-c core.hooksPath=/dev/null push owner=unset writer="+writerToken+" allow=unset dir=/y prompt=0", calls(t, log))
}

func TestRunner_AProjectWithCredentialsThatTheProcessLacksRefusesTheCallAndRunsNoGit(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	log, _ := fakeGit(t)

	out := reach.Runner(reach.Call{Project: "p", Dir: t.TempDir(), Remote: "https://example.test/o/r.git", Declared: true}, nil).Run(t.Context(), []string{"fetch"})

	assert.Equal(t, 1, out.ExitCode)
	require.ErrorIs(t, out.SetupErr, reach.ErrNotLoaded)
	assert.NoFileExists(t, log, "a refused call runs no git")
}

func TestRunner_AProjectWithLoadedCredentialsRefusesARemoteThatIsNotHTTPSAndRunsNoGit(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	log, _ := fakeGit(t)

	out := reach.Runner(reach.Call{Project: "p", Dir: t.TempDir(), Remote: "ssh://git@example.test/o/r.git", Declared: true, Loaded: true, Token: writerToken}, nil).
		Run(t.Context(), []string{"fetch"})

	assert.Equal(t, 1, out.ExitCode)
	require.Error(t, out.SetupErr)
	assert.NoFileExists(t, log, "a refused call runs no git")
}

func TestRunner_AProjectWithNoCredentialsTakesTheRunnerThatTheCallerSupplies(t *testing.T) {
	t.Parallel()

	var got string

	want := stubRunner{}
	supplied := reach.Runner(reach.Call{Project: "p", Dir: "/d"}, func(dir string) proc.Runner {
		got = dir

		return want
	})

	assert.Equal(t, want, supplied)
	assert.Equal(t, "/d", got)
}

// stubRunner is a runner that runs nothing.
type stubRunner struct{}

func (stubRunner) Run(context.Context, []string) model.CommandResult { return model.CommandResult{} }
