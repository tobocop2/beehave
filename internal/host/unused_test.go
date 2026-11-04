package host_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/host"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc/proctest"
)

// shellCall is what a shell on the host got: -c, the script, the shell name, then the words the script reads.
func shellCall(t *testing.T, sh *proctest.Scripted) (script string, words []string) {
	t.Helper()

	require.Len(t, sh.Calls, 1)

	call := sh.Calls[0]
	require.Equal(t, "-c", call[0])

	return call[1], call[3:]
}

func TestEnsureUnused_ResolvesTheDirectoryOnTheHostInTheSameCallAsTheScan(t *testing.T) {
	t.Parallel()

	sh := &proctest.Scripted{}
	sh.PlanContaining("", "/proc")

	require.NoError(t, host.EnsureUnused(t.Context(), sh, "/home/lane[1]"))

	script, words := shellCall(t, sh)

	assert.Contains(t, script, `readlink -f -- "$1"`)
	assert.Contains(t, script, "find /proc")
	assert.Equal(t, []string{"/home/lane[1]", `/home/lane\[1]`}, words, "the raw path and its glob-escaped form")
}

func TestEnsureUnused_MatchesTheGivenAndTheResolvedPathAndEverythingBelow(t *testing.T) {
	t.Parallel()

	sh := &proctest.Scripted{}
	sh.PlanContaining("", "/proc")

	require.NoError(t, host.EnsureUnused(t.Context(), sh, "/home/lane1"))

	script, _ := shellCall(t, sh)

	for _, want := range []string{`-lname "$2"`, `-lname "$2/*"`, `-lname "$resolved"`, `-lname "$resolved/*"`} {
		assert.Contains(t, script, want)
	}
}

func TestEnsureUnused_ScansEveryWayAProcessHoldsADirectoryToTheDepthOfAThread(t *testing.T) {
	t.Parallel()

	sh := &proctest.Scripted{}
	sh.PlanContaining("", "/proc")

	require.NoError(t, host.EnsureUnused(t.Context(), sh, "/home/lane1"))

	script, _ := shellCall(t, sh)

	for _, want := range []string{"/proc/[0-9]*/cwd", "/proc/[0-9]*/exe", "/proc/[0-9]*/fd/*", "/proc/[0-9]*/task/*/cwd", "/proc/[0-9]*/task/*/exe"} {
		assert.Contains(t, script, "'"+want+"'")
	}

	assert.Contains(t, script, "-maxdepth 4")
}

func TestEnsureUnused_CleansTheDirectoryAndNeverResolvesItOnThisMachine(t *testing.T) {
	t.Parallel()

	target, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	link := filepath.Join(t.TempDir(), "lane")
	require.NoError(t, os.Symlink(target, link))

	sh := &proctest.Scripted{}
	sh.PlanContaining("", "/proc")

	require.NoError(t, host.EnsureUnused(t.Context(), sh, link+"//./"))

	_, words := shellCall(t, sh)

	assert.Equal(t, []string{link, link}, words, "a path that resolves here is not resolved here")
}

func TestEnsureUnused_RefusesADirectoryInUseAndAScanThatFails(t *testing.T) {
	t.Parallel()

	used := &proctest.Scripted{}
	used.PlanContaining("/proc/412/cwd\n", "/proc")

	require.ErrorIs(t, host.EnsureUnused(t.Context(), used, "/home/lane1"), host.ErrInUse)

	failing := &proctest.Scripted{}
	failing.PlanResult(model.CommandResult{ExitCode: 2, Stderr: "readlink: no such"}, "-c")

	require.ErrorIs(t, host.EnsureUnused(t.Context(), failing, "/home/lane1"), host.ErrInUseRead)
}
