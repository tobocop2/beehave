package gitexec_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/exectest"
	"github.com/tobocop2/beehave/internal/proc/gitexec"
	"github.com/tobocop2/beehave/internal/proc/gitexec/internal/gitbin"
)

// logGit puts a fake git first on PATH. The fake appends its arguments, one call per line, to the file that the function returns.
func logGit(t *testing.T) string {
	t.Helper()

	bin := t.TempDir()
	log := filepath.Join(bin, "calls")
	script := "#!/bin/sh\necho \"$@\" >> " + log + "\n"
	require.NoError(t, exectest.Write(filepath.Join(bin, "git"), script))
	t.Setenv("PATH", strings.Join([]string{bin, "/usr/bin", "/bin"}, string(os.PathListSeparator)))

	return log
}

func calls(t *testing.T, log string) string {
	t.Helper()

	data, err := os.ReadFile(log) //nolint:gosec // a file of the test
	require.NoError(t, err)

	return strings.TrimSpace(string(data))
}

func TestRunner_RunsGitWithTheHooksOff(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	log := logGit(t)

	gitexec.New(t.TempDir(), 0).Run(t.Context(), []string{"worktree", "add", "x"})

	assert.Equal(t, "-c core.hooksPath=/dev/null worktree add x", calls(t, log))
}

// envGit puts a fake git first on PATH that appends GIT_ALLOW_PROTOCOL and GIT_NO_LAZY_FETCH, one call per line, to the file that the function returns.
func envGit(t *testing.T) string {
	t.Helper()

	bin := t.TempDir()
	log := filepath.Join(bin, "calls")
	script := "#!/bin/sh\necho \"allow=${GIT_ALLOW_PROTOCOL-unset} lazy=${GIT_NO_LAZY_FETCH-unset}\" >> " + log + "\n"
	require.NoError(t, exectest.Write(filepath.Join(bin, "git"), script))
	t.Setenv("PATH", strings.Join([]string{bin, "/usr/bin", "/bin"}, string(os.PathListSeparator)))

	return log
}

func TestRunner_EveryLocalCallTakesTheFileTransportOnlyAndFetchesNoMissingObject(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	log := envGit(t)

	gitexec.New(t.TempDir(), 0).Run(t.Context(), []string{"status"})

	widened := gitexec.New(t.TempDir(), 0)
	widened.Env = []string{"GIT_ALLOW_PROTOCOL=ssh", "GIT_NO_LAZY_FETCH=0"}
	widened.Run(t.Context(), []string{"status"})

	cloneRunner(t).ReadConfig(t.Context(), "user.name")

	assert.Equal(t, strings.Repeat("allow=file lazy=1\n", 4), calls(t, log)+"\n", "no env of the caller widens a local call, the read of the settings included")
}

func TestIsGit_AcceptsTheNameOfTheGitProgramOnly(t *testing.T) {
	t.Parallel()

	assert.True(t, gitexec.IsGit("git"))
	assert.False(t, gitexec.IsGit("gh"))
	assert.False(t, gitexec.IsGit("/usr/bin/git"))
}

func TestArgs_PutsTheHooksSettingFirstAndKeepsTheArguments(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{"-c", "core.hooksPath=/dev/null", "status", "-s"}, gitbin.Args([]string{"status", "-s"}))
}

func TestReadConfig_DoesNotSetTheHooksPathItReads(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	log := logGit(t)

	cloneRunner(t).ReadConfig(t.Context(), "core.hooksPath")

	lines := strings.Split(calls(t, log), "\n")
	assert.Equal(t, "config --get core.hooksPath", lines[len(lines)-1])
}
