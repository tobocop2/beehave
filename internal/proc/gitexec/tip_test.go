package gitexec

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestTipOf_ReadsTheCommitOfTheBranchInTheListingOfARealRemote(t *testing.T) {
	t.Parallel()

	dir, commit := sourceAt(t)

	tip, err := TipOf(gitIn(t, dir, "ls-remote", "--", dir, TipRef("main")), dir, "main")

	require.NoError(t, err)
	assert.Equal(t, commit, tip)
}

func TestTipOf_NamesTheBranchWhenTheListingLacksIt(t *testing.T) {
	t.Parallel()

	_, err := TipOf(fullID+"\trefs/heads/main\n", "/x", "release")

	require.ErrorIs(t, err, ErrNoTip)
	assert.ErrorContains(t, err, "refs/heads/release")
}

func TestTipOf_IgnoresALineOfAnotherRefAndAnEmptyCommit(t *testing.T) {
	t.Parallel()

	_, err := TipOf("abc\trefs/heads/main-2\n\trefs/heads/main\n", "/x", "main")

	require.ErrorIs(t, err, ErrNoTip)
}

// fullID is a commit id of 40 hexadecimal digits.
const fullID = "abcdabcdabcdabcdabcdabcdabcdabcdabcdabcd"

func TestTipOf_RefusesAnAnswerThatIsNotAFullCommitIdAndNamesTheBranch(t *testing.T) {
	t.Parallel()

	for name, answer := range map[string]string{
		"a short id":                    fullID[:7],
		"one digit short":               fullID[:39],
		"one digit long":                fullID + "0",
		"an upper case hex letter":      fullID[:39] + "A",
		"a letter past f":               fullID[:39] + "g",
		"a sign below the digits":       fullID[:39] + "-",
		"a sign between digits and a-f": fullID[:39] + ":",
		"a ref in the id":               "refs/heads/main" + fullID[:25],
	} {
		_, err := TipOf(answer+"\trefs/heads/main\n", "/x", "main")

		require.ErrorIs(t, err, ErrNoTip, name)
		require.ErrorContains(t, err, "refs/heads/main", name)
		require.ErrorContains(t, err, "not a full commit id", name)
	}
}
