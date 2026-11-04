package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/proc"
)

func TestDisposableTree_DoesNotRunThePostCheckoutHookOfTheRepository(t *testing.T) {
	t.Parallel()

	dir := repoWithCommit(t)
	marker := filepath.Join(t.TempDir(), "hook-ran")
	hook := filepath.Join(dir, ".git", "hooks", "post-checkout")
	require.NoError(t, os.MkdirAll(filepath.Dir(hook), 0o700))
	require.NoError(t, os.WriteFile(hook, []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0o700)) //nolint:gosec // a hook that the test must see skipped

	work, release, err := disposableTree(t.Context(), dir, t.TempDir())
	require.NoError(t, err)

	defer release()

	assert.DirExists(t, work)
	assert.NoFileExists(t, marker, "the worktree add ran the hook of the repository")
}

func fakeGit(t *testing.T, script string) {
	t.Helper()

	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o700)) //nolint:gosec // a fake git for the test
	t.Setenv("PATH", strings.Join([]string{bin, "/usr/bin", "/bin"}, string(os.PathListSeparator)))
}

func TestGitIn_AFailureKeepsTheTextOfGit(t *testing.T) {
	fakeGit(t, "#!/bin/sh\necho 'fatal: not a repository' >&2\nexit 128\n")

	_, err := gitIn(t.Context(), t.TempDir(), "status")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "exited with code 128")
	assert.Contains(t, err.Error(), "fatal: not a repository")
}

func TestGitIn_ATimeoutReadsAsATimeout(t *testing.T) {
	fakeGit(t, "#!/bin/sh\nsleep 5\n")

	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()

	_, err := gitIn(ctx, t.TempDir(), "status")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "timed out")
	assert.NotContains(t, err.Error(), "canceled")
}

func TestGitIn_ACancelReadsAsACancel(t *testing.T) {
	fakeGit(t, "#!/bin/sh\nsleep 5\n")

	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(200*time.Millisecond, cancel)

	_, err := gitIn(ctx, t.TempDir(), "status")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "was canceled")
}

func TestGitFailure_AToolThatCannotStartNamesTheSetupError(t *testing.T) {
	t.Parallel()

	reason := gitFailure(t.Context(), model.CommandResult{ExitCode: proc.ExitCapFailed, SetupErr: errors.New("no count")})

	assert.Contains(t, reason, "could not start")
	assert.Contains(t, reason, "no count")
}

func TestGitIn_ReturnsWhenAChildOfGitKeepsTheOutputOpen(t *testing.T) {
	bin := t.TempDir()
	script := "#!/bin/sh\nsleep 6 &\nexit 0\n"
	require.NoError(t, os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o700)) //nolint:gosec // a fake git that the test must see bounded
	t.Setenv("PATH", strings.Join([]string{bin, "/usr/bin", "/bin"}, string(os.PathListSeparator)))

	start := time.Now()
	_, err := gitIn(t.Context(), t.TempDir(), "status")

	require.Error(t, err, "the runner reports the pipe that the child kept open")
	assert.Less(t, time.Since(start), 4*time.Second, "the call waited for the child that holds the output pipe")
}
