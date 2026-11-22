package gitexec

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/exectest"
)

// sourceAt makes a repository with one commit on main and returns its folder and that commit.
func sourceAt(t *testing.T) (dir, commit string) {
	t.Helper()

	dir = t.TempDir()

	gitIn(t, dir, "init", "-q", "-b", "main")
	gitIn(t, dir, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "--allow-empty", "-m", "base")

	return dir, gitIn(t, dir, "rev-parse", "HEAD")
}

// gitIn runs git with args in dir, with no user or system config, and returns what it printed.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()

	path, err := exec.LookPath("git")
	require.NoError(t, err)

	cmd := exec.CommandContext(t.Context(), path, args...) //nolint:gosec // a test runs git on its own temporary folder
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + filepath.Dir(path) + ":/usr/bin:/bin", "HOME=" + dir, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"}
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))

	return strings.TrimSpace(string(out))
}

// fakeGit writes a git program that prints text and returns its path.
func fakeGit(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "git")
	require.NoError(t, exectest.Write(path, "#!/bin/sh\n"+body+"\n"))

	return path
}

func found(path string) func() (string, error) { return func() (string, error) { return path, nil } }

func TestTip_ReadsTheCommitOfTheBranchAtTheRemote(t *testing.T) {
	t.Parallel()

	dir, commit := sourceAt(t)

	tip, err := Tip(t.Context(), dir, "main", 0)

	require.NoError(t, err)
	assert.Equal(t, commit, tip)
}

func TestTip_NamesTheBranchWhenTheRemoteLacksIt(t *testing.T) {
	t.Parallel()

	dir, _ := sourceAt(t)

	_, err := Tip(t.Context(), dir, "release", 0)

	require.ErrorIs(t, err, ErrNoTip)
	assert.ErrorContains(t, err, "refs/heads/release")
}

func TestTip_FailsWhenTheRemoteCannotBeRead(t *testing.T) {
	t.Parallel()

	_, err := Tip(t.Context(), filepath.Join(t.TempDir(), "absent"), "main", 0)

	require.Error(t, err)
	require.NotErrorIs(t, err, ErrNoTip)
	assert.ErrorContains(t, err, "exited with code")
}

func TestTip_FailsWhenGitIsNotFound(t *testing.T) {
	t.Parallel()

	_, err := tipWith(t.Context(), func() (string, error) { return "", exec.ErrNotFound }, "/x", "main", 0)

	require.ErrorIs(t, err, exec.ErrNotFound)
}

func TestTip_IgnoresALineOfAnotherRefAndAnEmptyCommit(t *testing.T) {
	t.Parallel()

	other := fakeGit(t, "printf 'abc\\trefs/heads/main-2\\n\\trefs/heads/main\\n'")

	_, err := tipWith(t.Context(), found(other), "/x", "main", 0)

	require.ErrorIs(t, err, ErrNoTip)
}

func TestTip_RunsTheProgramThatTheLookupGivesWithNoGitConfigOfTheUser(t *testing.T) {
	t.Parallel()

	env := fakeGit(t, "[ \"$GIT_CONFIG_GLOBAL\" = "+os.DevNull+" ] && [ \"$GIT_CONFIG_NOSYSTEM\" = 1 ] && printf '"+fullID+"\\trefs/heads/main\\n'")

	tip, err := tipWith(t.Context(), found(env), "/x", "main", 0)

	require.NoError(t, err)
	assert.Equal(t, fullID, tip)
}

// fullID is a commit id of 40 hexadecimal digits.
const fullID = "0123456789abcdef0123456789abcdef01234567"

func TestTip_RefusesAnAnswerThatIsNotAFullCommitIdAndNamesTheBranch(t *testing.T) {
	t.Parallel()

	for name, answer := range map[string]string{
		"a short id":      fullID[:7],
		"one digit short": fullID[:39],
		"one digit long":  fullID + "0",
		"upper case":      strings.ToUpper(fullID),
		"not hexadecimal": strings.Repeat("g", 40),
		"a ref in the id": "refs/heads/main" + fullID[:25],
	} {
		fake := fakeGit(t, "printf '"+answer+"\\trefs/heads/main\\n'")

		_, err := tipWith(t.Context(), found(fake), "/x", "main", 0)

		require.ErrorIs(t, err, ErrNoTip, name)
		require.ErrorContains(t, err, "refs/heads/main", name)
		require.ErrorContains(t, err, "not a full commit id", name)
	}
}
