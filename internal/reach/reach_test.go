package reach_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/exectest"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc"
	"github.com/tobocop2/beehave/internal/reach"
)

const writerToken = "writer-token"

// fakeGit puts a fake git first on PATH. A call appends one line to the file that the function returns: the arguments, the token of the owner, the token
// of the writer and the transport allow list as the call sees them.
func fakeGit(t *testing.T) (string, string) {
	t.Helper()

	bin := t.TempDir()
	log := filepath.Join(bin, "calls")
	script := "#!/bin/sh\necho \"$* owner=${GH_TOKEN-unset} writer=${BEEHAVE_FORGE_TOKEN-unset} allow=${GIT_ALLOW_PROTOCOL-unset} dir=${GIT_DIR-unset} prompt=${GIT_TERMINAL_PROMPT-unset}\" >> " + log + "\n"
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

func TestRunner_AProjectWithNoCredentialsRunsAsTheOwnerWithItsEnvironmentAndAnyTransport(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	log, _ := fakeGit(t)

	out := reach.Runner(reach.Call{Project: "p", Dir: t.TempDir(), Remote: "ssh://git@example.test/o/r.git", OwnerEnv: []string{"GIT_TERMINAL_PROMPT=0", "GIT_DIR=/x"}}).
		Run(t.Context(), []string{"fetch"})

	require.Equal(t, 0, out.ExitCode)
	assert.Equal(t, "-c core.hooksPath=/dev/null fetch owner=owner-token writer=unset allow=unset dir=/x prompt=0", calls(t, log))
}

func TestRunner_AProjectWithLoadedCredentialsRunsAsTheWriterAndNeverAsTheOwner(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	log, path := fakeGit(t)

	out := reach.Runner(reach.Call{
		Project: "p", Dir: t.TempDir(), Remote: "https://example.test/o/r.git", Declared: true, Loaded: true, Token: writerToken,
		Base: map[string]string{"PATH": path}, Pairs: []string{"GIT_DIR=/y"},
		Owner: func(string) proc.Runner {
			t.Error("the writer call used the runner of the owner")

			return nil
		},
	}).Run(t.Context(), []string{"push"})

	require.Equal(t, 0, out.ExitCode)
	assert.Equal(t, "-c core.hooksPath=/dev/null push owner=unset writer="+writerToken+" allow=unset dir=/y prompt=0", calls(t, log))
}

func TestRunner_AProjectWithCredentialsThatTheProcessLacksRefusesTheCallAndRunsNoGit(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	log, _ := fakeGit(t)

	out := reach.Runner(reach.Call{Project: "p", Dir: t.TempDir(), Remote: "https://example.test/o/r.git", Declared: true}).Run(t.Context(), []string{"fetch"})

	assert.Equal(t, 1, out.ExitCode)
	require.ErrorIs(t, out.SetupErr, reach.ErrNotLoaded)
	assert.NoFileExists(t, log, "a refused call runs no git")
}

func TestRunner_AProjectWithLoadedCredentialsRefusesARemoteThatIsNotHTTPSAndRunsNoGit(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	log, _ := fakeGit(t)

	out := reach.Runner(reach.Call{Project: "p", Dir: t.TempDir(), Remote: "ssh://git@example.test/o/r.git", Declared: true, Loaded: true, Token: writerToken}).
		Run(t.Context(), []string{"fetch"})

	assert.Equal(t, 1, out.ExitCode)
	require.Error(t, out.SetupErr)
	assert.NoFileExists(t, log, "a refused call runs no git")
}

func TestRunner_AProjectWithNoCredentialsTakesTheRunnerThatTheCallerSupplies(t *testing.T) {
	t.Parallel()

	var got string

	want := stubRunner{}
	supplied := reach.Runner(reach.Call{Project: "p", Dir: "/d", Owner: func(dir string) proc.Runner {
		got = dir

		return want
	}})

	assert.Equal(t, want, supplied)
	assert.Equal(t, "/d", got)
}

// stubRunner is a runner that runs nothing.
type stubRunner struct{}

func (stubRunner) Run(context.Context, []string) model.CommandResult { return model.CommandResult{} }
