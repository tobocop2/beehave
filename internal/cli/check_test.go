package cli_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent/claude"
	"github.com/tobocop2/beehave/internal/agent/opencode"
	"github.com/tobocop2/beehave/internal/cli"
	"github.com/tobocop2/beehave/internal/config"
	forgegithub "github.com/tobocop2/beehave/internal/forge/github"
	"github.com/tobocop2/beehave/internal/host/skypilot"
	"github.com/tobocop2/beehave/internal/host/slurm"
	"github.com/tobocop2/beehave/internal/host/ssh"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/store/beads"
	storegithub "github.com/tobocop2/beehave/internal/store/github"
	"github.com/tobocop2/beehave/internal/store/local"
)

// registry is the adapter registry of the cli tests: the names that the adapters declare, with the keys and tools that the real adapters register.
func registry() config.Adapters {
	beadsEntry := func() *config.Entry {
		block := beads.Block()

		return &config.Entry{Block: &block, Tools: []string{"bd"}}
	}
	githubEntry := func() *config.Entry { return &config.Entry{Keys: []string{"repo"}, Tools: []string{"gh"}} }

	return config.Adapters{
		Trackers: map[model.TrackerName]*config.Entry{local.Tracker: {}, beads.Tracker: beadsEntry(), storegithub.Tracker: githubEntry()},
		Claims:   map[model.ClaimsName]*config.Entry{local.Claims: {}, beads.Claims: beadsEntry()},
		Forges:   map[model.ForgeName]*config.Entry{forgegithub.Name: githubEntry()},
		Hosts: map[model.HostProvider]*config.Entry{
			skypilot.Name: {Idle: true, HostBlock: "skypilot"},
			ssh.Name:      {HostBlock: "ssh"},
			slurm.Name:    {HostBlock: "ssh"},
		},
		Agents: map[model.Agent]*config.Entry{claude.Name: {}, opencode.Name: {}},
	}
}

func writeConfig(t *testing.T, dir, name, content string) {
	t.Helper()

	path := filepath.Join(dir, name)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

func runCheckConfig(t *testing.T, env map[string]string, args ...string) (int, string, string) {
	t.Helper()

	var stdout, stderr bytes.Buffer

	code := cli.Execute(context.Background(), append([]string{"check", "config"}, args...), cli.BuildInfo{Registry: registry(), Version: "x"}, env, &stdout, &stderr)

	return code, stdout.String(), stderr.String()
}

func TestCheckConfig_ExitsZeroForAValidFolder(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeConfig(t, dir, "runners.star", validRunners)
	writeConfig(t, dir, "projects/p.star", validProject)

	code, stdout, stderr := runCheckConfig(t, map[string]string{}, "--config", dir)

	require.Equal(t, 0, code)
	require.Empty(t, stdout)
	require.Empty(t, stderr)
}

func TestCheckConfig_AProjectOnABuildHostGetsANoteThatItsGateRunsWithNoSandbox(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	remote := "project(repo = \"o/n\", tracker = \"github\", forge = \"github\", claims = \"beads\", beads = beads(prefix = \"p\"), base_branch = \"main\", gate = \"make check\", test_report = \"r.xml\", " +
		"sandbox = \"on\", build_host = \"gpu\", host = host(provider = \"skypilot\", disk_floor_gb = 50))\n"
	writeConfig(t, dir, "runners.star", validRunners)
	writeConfig(t, dir, "projects/far.star", remote)
	writeConfig(t, dir, "projects/near.star", validProject)

	code, _, stderr := runCheckConfig(t, map[string]string{}, "--config", dir)

	require.Equal(t, 0, code, "a note changes no exit code")
	require.Equal(t, "note: project far builds on the host gpu, so its gate runs there with no sandbox. The sandbox covers only a gate that runs on this machine.\n", stderr)
}

func TestCheckConfig_ALocalProjectWithTheSandboxOnGetsANoteThatNoSandboxCanEnforceItsGate(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	boxed := "project(repo = \"o/n\", tracker = \"github\", forge = \"github\", claims = \"beads\", beads = beads(prefix = \"p\"), base_branch = \"main\", gate = \"make check\", test_report = \"r.xml\", " +
		"sandbox = \"on\", gate_network = [\"proxy.golang.org\"])\n"
	writeConfig(t, dir, "runners.star", validRunners)
	writeConfig(t, dir, "projects/near.star", boxed)

	code, _, stderr := runCheckConfig(t, map[string]string{}, "--config", dir)

	require.Equal(t, 0, code, "a note changes no exit code")
	require.Equal(t, "note: project near has the sandbox on, but "+config.GateSandboxRefusal+"\n", stderr)
}

func runWithAdapters(t *testing.T, adapters func(context.Context, model.Project) error, dir string) (int, string) {
	t.Helper()

	var stdout, stderr bytes.Buffer

	code := cli.Execute(context.Background(), []string{"check", "config", "--config", dir}, cli.BuildInfo{Registry: registry(), Version: "x", Adapters: adapters},
		map[string]string{}, &stdout, &stderr)

	return code, stderr.String()
}

func TestCheckConfig_ReportsAProjectWhoseAdaptersCannotBeBuilt(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	text := "project(repo = \"o/n\", tracker = \"github\", forge = \"github\", claims = \"beads\", beads = beads(prefix = \"p\"), base_branch = \"main\", gate = \"g\", test_report = \"r.xml\")\n"
	writeConfig(t, dir, "projects/a.star", text)
	writeConfig(t, dir, "projects/b.star", text)

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
	require.Equal(t, filepath.Join(dir, "projects", "b.star")+": no gh on the path\n", stderr)
}

func TestCheckConfig_RefusesABeadsClaimsProjectWithoutABeadsBlock(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeConfig(t, dir, "projects/p.star", "project(repo = \"o/n\", tracker = \"github\", forge = \"github\", claims = \"beads\", base_branch = \"main\", gate = \"g\")\n")

	code, _, stderr := runCheckConfig(t, map[string]string{}, "--config", dir)

	require.Equal(t, 2, code)
	require.Contains(t, stderr, "the claims `beads` needs the `beads` block. Write beads(prefix = ...).")
}

func TestCheckConfig_RefusesAnUnknownIssueStoreAndNamesTheKnownOnes(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeConfig(t, dir, "projects/p.star", "project(repo = \"o/n\", tracker = \"jira\", forge = \"github\", base_branch = \"main\", gate = \"g\")\n")

	code, _, stderr := runCheckConfig(t, map[string]string{}, "--config", dir)

	require.Equal(t, 2, code)
	require.Contains(t, stderr, `unknown tracker "jira": use one of beads, github, local`)
}

func TestCheckConfig_PrintsOneLineForEachProblemAndExitsTwo(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeConfig(t, dir, "runners.star", "agents(bogus = 1)\n")
	writeConfig(t, dir, "projects/a.star", "project(repo = \"justname\", tracker = \"github\", forge = \"github\", claims = \"beads\", beads = beads(prefix = \"p\"), base_branch = \"main\", gate = \"g\")\n")
	writeConfig(t, dir, "projects/b.star", validProject)

	code, stdout, stderr := runCheckConfig(t, map[string]string{}, "--config", dir)

	require.Equal(t, 2, code)
	require.Empty(t, stdout)
	require.Equal(t,
		filepath.Join(dir, "runners.star")+":1:7: agents: unexpected keyword argument \"bogus\". Fix the call.\n"+
			filepath.Join(dir, "projects", "a.star")+":1:8: `repo` must be written owner/name. Fix the value.\n",
		stderr)
}

func TestCheckConfig_ReportsAnOverlayProblem(t *testing.T) {
	t.Parallel()

	code, _, stderr := runCheckConfig(t, map[string]string{"BEEHAVE_AGENTS_MAX_CONCURRENT": "0"}, "--config", t.TempDir())

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
	writeConfig(t, dir, "projects/p.star", "shade()\n")

	code, _, stderr := runCheckConfig(t, map[string]string{"BEEHAVE_CONFIG": dir})

	require.Equal(t, 2, code)
	require.Contains(t, stderr, filepath.Join(dir, "projects", "p.star"))
}

func TestCheckConfig_TheFlagBeatsTheEnvironment(t *testing.T) {
	t.Parallel()

	bad := t.TempDir()
	writeConfig(t, bad, "projects/p.star", "shade()\n")

	code, _, _ := runCheckConfig(t, map[string]string{"BEEHAVE_CONFIG": bad}, "--config", t.TempDir())

	require.Equal(t, 0, code)
}

func TestCheckConfig_UsesTheHomeFolderByDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeConfig(t, home, ".config/beehave/projects/p.star", "shade()\n")

	code, _, stderr := runCheckConfig(t, map[string]string{})

	require.Equal(t, 2, code)
	require.Contains(t, stderr, filepath.Join(home, ".config", "beehave", "projects", "p.star"))
}

func TestCheckConfig_FailsWithExitOneWhenThereIsNoHomeFolder(t *testing.T) {
	t.Setenv("HOME", "")

	code, _, stderr := runCheckConfig(t, map[string]string{})

	require.Equal(t, 1, code)
	require.Contains(t, stderr, "beehave: cannot find the home folder")
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
