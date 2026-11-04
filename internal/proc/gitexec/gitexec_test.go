package gitexec_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/exectest"
	"github.com/tobocop2/beehave/internal/proc/gitexec"
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

func TestArgs_PutsTheHooksSettingFirstAndKeepsTheArguments(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{"-c", "core.hooksPath=/dev/null", "status", "-s"}, gitexec.Args([]string{"status", "-s"}))
}

func TestReadConfig_DoesNotSetTheHooksPathItReads(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	log := logGit(t)

	gitexec.ReadConfig(t.Context(), t.TempDir(), "core.hooksPath")

	assert.Equal(t, "config --get core.hooksPath", calls(t, log))
}

// runnerPattern matches the code that makes a runner or a command for the git program.
var runnerPattern = regexp.MustCompile(`Binary:\s*"git"|NewExecRunner\("git"|GroupCommand\([^,]+,\s*"git"|exec\.Command(Context)?\([^"\n]*"git"`)

// TestTheGitProgramIsNamedOnlyHere fails when code outside this package makes its own runner for git, because that call would run the hooks of the tree.
func TestTheGitProgramIsNamedOnlyHere(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..", "..")
	own := filepath.Join(root, "internal", "proc", "gitexec")

	var found []string

	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if entry.IsDir() && (entry.Name() == ".git" || path == own) {
			return filepath.SkipDir
		}

		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		data, err := os.ReadFile(path) //nolint:gosec // a source file of this module
		if err != nil {
			return err
		}

		if runnerPattern.Match(data) {
			found = append(found, path)
		}

		return nil
	})
	require.NoError(t, err)

	assert.Empty(t, found, "make the runner with gitexec.New or the arguments with gitexec.Args, so the hooks stay off")
}
