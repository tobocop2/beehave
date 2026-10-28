package cli_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/cli"
	"github.com/tobocop2/obee/internal/model"
)

func writeConfig(t *testing.T, dir, name, content string) {
	t.Helper()

	path := filepath.Join(dir, name)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

func runCheckConfig(t *testing.T, env map[string]string, args ...string) (int, string, string) {
	t.Helper()

	var stdout, stderr bytes.Buffer

	code := cli.Execute(context.Background(), append([]string{"check", "config"}, args...), cli.BuildInfo{Version: "x"}, env, &stdout, &stderr)

	return code, stdout.String(), stderr.String()
}

func TestCheckConfig_ExitsZeroForAValidFolder(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeConfig(t, dir, "runners.hcl", "runner {\nname = \"a\"\nagent = \"claude\"\n}\n")
	writeConfig(t, dir, "projects/p.hcl", "repo = \"o/n\"\ntracker = \"github\"\nforge = \"github\"\nclaims = \"beads\"\nbeads_prefix = \"p\"\nbase_branch = \"main\"\ngate = \"make check\"\n")

	code, stdout, stderr := runCheckConfig(t, map[string]string{}, "--config", dir)

	require.Equal(t, 0, code)
	require.Empty(t, stdout)
	require.Empty(t, stderr)
}

func runWithAdapters(t *testing.T, adapters func(context.Context, model.Project) error, dir string) (int, string) {
	t.Helper()

	var stdout, stderr bytes.Buffer

	code := cli.Execute(context.Background(), []string{"check", "config", "--config", dir}, cli.BuildInfo{Version: "x", Adapters: adapters},
		map[string]string{}, &stdout, &stderr)

	return code, stderr.String()
}

func TestCheckConfig_ReportsAProjectWhoseAdaptersCannotBeBuilt(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	text := "repo = \"o/n\"\ntracker = \"github\"\nforge = \"github\"\nclaims = \"beads\"\nbeads_prefix = \"p\"\nbase_branch = \"main\"\ngate = \"g\"\n"
	writeConfig(t, dir, "projects/a.hcl", text)
	writeConfig(t, dir, "projects/b.hcl", text)

	var built []string

	code, stderr := runWithAdapters(t, func(_ context.Context, project model.Project) error {
		built = append(built, project.Name)
		if project.Name == "b" {
			return errors.New("no gh on the path")
		}

		return nil
	}, dir)

	require.Equal(t, 2, code)
	require.Equal(t, []string{"a", "b"}, built)
	require.Equal(t, filepath.Join(dir, "projects", "b.hcl")+": no gh on the path\n", stderr)
}

func TestCheckConfig_RefusesABeadsClaimsProjectWithoutABeadsPrefix(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeConfig(t, dir, "projects/p.hcl", "repo = \"o/n\"\ntracker = \"github\"\nforge = \"github\"\nclaims = \"beads\"\nbase_branch = \"main\"\ngate = \"g\"\n")

	code, _, stderr := runCheckConfig(t, map[string]string{}, "--config", dir)

	require.Equal(t, 2, code)
	require.Contains(t, stderr, "`beads_prefix` must be a non-empty string")
}

func TestCheckConfig_PrintsOneLineForEachProblemAndExitsTwo(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeConfig(t, dir, "runners.hcl", "bogus = 1\n")
	writeConfig(t, dir, "projects/a.hcl", "repo = \"justname\"\ntracker = \"github\"\nforge = \"github\"\nclaims = \"beads\"\nbeads_prefix = \"p\"\nbase_branch = \"main\"\ngate = \"g\"\n")
	writeConfig(t, dir, "projects/b.hcl", "repo = \"o/n\"\ntracker = \"github\"\nforge = \"github\"\nclaims = \"beads\"\nbeads_prefix = \"p\"\nbase_branch = \"main\"\ngate = \"g\"\n")

	code, stdout, stderr := runCheckConfig(t, map[string]string{}, "--config", dir)

	require.Equal(t, 2, code)
	require.Empty(t, stdout)
	require.Equal(t,
		filepath.Join(dir, "runners.hcl")+":1:1: unknown key `bogus`. Remove it.\n"+
			filepath.Join(dir, "projects", "a.hcl")+":1:1: `repo` must be written owner/name. Fix the value.\n",
		stderr)
}

func TestCheckConfig_ReportsAnOverlayProblem(t *testing.T) {
	t.Parallel()

	code, _, stderr := runCheckConfig(t, map[string]string{"OBEE_AGENTS_MAX_CONCURRENT": "0"}, "--config", t.TempDir())

	require.Equal(t, 2, code)
	require.Contains(t, stderr, "agents.max_concurrent must be 1 or more")
}

func TestCheckConfig_ReportsAMissingFolder(t *testing.T) {
	t.Parallel()

	code, _, stderr := runCheckConfig(t, map[string]string{}, "--config", filepath.Join(t.TempDir(), "none"))

	require.Equal(t, 2, code)
	require.Contains(t, stderr, "does not exist")
}

func TestCheckConfig_TakesTheFolderFromTheEnvironment(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeConfig(t, dir, "projects/p.hcl", "shade = 1\n")

	code, _, stderr := runCheckConfig(t, map[string]string{"OBEE_CONFIG": dir})

	require.Equal(t, 2, code)
	require.Contains(t, stderr, filepath.Join(dir, "projects", "p.hcl"))
}

func TestCheckConfig_TheFlagBeatsTheEnvironment(t *testing.T) {
	t.Parallel()

	bad := t.TempDir()
	writeConfig(t, bad, "projects/p.hcl", "shade = 1\n")

	code, _, _ := runCheckConfig(t, map[string]string{"OBEE_CONFIG": bad}, "--config", t.TempDir())

	require.Equal(t, 0, code)
}

func TestCheckConfig_UsesTheHomeFolderByDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeConfig(t, home, ".config/obee/projects/p.hcl", "shade = 1\n")

	code, _, stderr := runCheckConfig(t, map[string]string{})

	require.Equal(t, 2, code)
	require.Contains(t, stderr, filepath.Join(home, ".config", "obee", "projects", "p.hcl"))
}

func TestCheckConfig_FailsWithExitOneWhenThereIsNoHomeFolder(t *testing.T) {
	t.Setenv("HOME", "")

	code, _, stderr := runCheckConfig(t, map[string]string{})

	require.Equal(t, 1, code)
	require.Contains(t, stderr, "obee: cannot find the home folder")
}

func TestCheckConfig_RejectsAnArgument(t *testing.T) {
	t.Parallel()

	code, _, stderr := runCheckConfig(t, map[string]string{}, "extra")

	require.Equal(t, 1, code)
	require.Contains(t, stderr, "unknown command")
}

func TestExitError_Message(t *testing.T) {
	t.Parallel()

	require.EqualError(t, &cli.ExitError{Code: 2}, "exit code 2")
}
