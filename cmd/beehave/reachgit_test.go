package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/exectest"
)

// envGit puts a fake git first on PATH. A call appends one line to the file that the function returns: the repository, the prompt setting and the transport
// allow list as the call sees them.
func envGit(t *testing.T) string {
	t.Helper()

	bin := t.TempDir()
	log := filepath.Join(bin, "calls")
	script := "#!/bin/sh\necho \"dir=${GIT_DIR-unset} prompt=${GIT_TERMINAL_PROMPT-unset} allow=${GIT_ALLOW_PROTOCOL-unset}\" >> " + log + "\n"
	require.NoError(t, exectest.Write(filepath.Join(bin, "git"), script))
	t.Setenv("PATH", strings.Join([]string{bin, "/usr/bin", "/bin"}, string(os.PathListSeparator)))

	return log
}

func envCalls(t *testing.T, log string) []string {
	t.Helper()

	data, err := os.ReadFile(log) //nolint:gosec // a file of this test
	require.NoError(t, err)

	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func TestFetchHead_EveryGitCallOfTheCopyHasTheFileTransportOnlyAndNoPrompt(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	log := envGit(t)
	root := filepath.Join(t.TempDir(), "push")
	require.NoError(t, os.Mkdir(root, 0o700))
	dir := filepath.Join(root, "p.git")

	require.NoError(t, fetchHead(t.Context(), root, dir, "/clone", "/program", "fix-7", "abc"))

	want := "dir=" + dir + " prompt=0 allow=file"
	assert.Equal(t, []string{want, want}, envCalls(t, log), "the init of the push repository and the fetch of the head")
}

func TestWorktrees_TheCallToTheRemoteOfAProjectWithNoCredentialsRunsAsTheOwnerWithNoPasswordPrompt(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	log := envGit(t)

	out := worktrees{}.reach("q", t.TempDir(), "https://example.test/o/r.git").Run(t.Context(), []string{"fetch"})

	require.Equal(t, 0, out.ExitCode)
	assert.Equal(t, []string{"dir=unset prompt=0 allow=unset"}, envCalls(t, log))
}

func TestPulls_ThePushOfAProjectWithNoCredentialsRunsAsTheOwnerInThePushRepositoryWithNoPasswordPrompt(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	log := envGit(t)
	dir := t.TempDir()

	out := pulls{}.pusher("q", dir, "https://example.test/o/r.git").Run(t.Context(), []string{"push"})

	require.Equal(t, 0, out.ExitCode)
	assert.Equal(t, []string{"dir=" + dir + " prompt=0 allow=unset"}, envCalls(t, log))
}
