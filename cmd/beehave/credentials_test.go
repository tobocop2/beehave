package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/exectest"
	forgegithub "github.com/tobocop2/beehave/internal/forge/github"
	"github.com/tobocop2/beehave/internal/flow"
	"github.com/tobocop2/beehave/internal/model"
	storegithub "github.com/tobocop2/beehave/internal/store/github"
)

const writerToken = "writer-token-value"

// writerHome makes a home folder with the writer token file and returns the options of an engine that runs with it and the config of a project that names the file.
func writerHome(t *testing.T) (config.Config, flow.Options) {
	t.Helper()

	home := evaluated(t, t.TempDir())
	folder := filepath.Join(home, ".config", "beehave", "credentials")
	require.NoError(t, os.MkdirAll(folder, 0o700))
	require.NoError(t, os.Chmod(folder, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(folder, "writer.token"), []byte(writerToken+"\n"), 0o600))

	project := model.Project{Name: "p", Repo: "o/r", Credentials: map[model.Role]model.Credential{model.RoleWriter: {File: "~/.config/beehave/credentials/writer.token"}}}
	opts := flow.Options{StateDir: evaluated(t, t.TempDir()), Env: map[string]string{"HOME": home, "PATH": os.Getenv("PATH")}}

	return config.Config{Projects: map[string]config.Project{"p": {Project: project}}}, opts
}

func TestWritersStart_ReadsTheWriterTokenOnceAndMakesAnEmptyGhConfigFolder(t *testing.T) {
	t.Parallel()

	cfg, opts := writerHome(t)
	roles := &writers{}

	require.NoError(t, roles.start(t.Context(), cfg, opts))

	role, found := roles.of("p")
	require.True(t, found)
	assert.Equal(t, writerToken, role.token)
	assert.Equal(t, filepath.Join(opts.StateDir, "roles", "p", "gh"), role.configDir)
	assert.NoDirExists(t, filepath.Join(opts.StateDir, "roles", "q"))

	entries, err := os.ReadDir(role.configDir)
	require.NoError(t, err)
	assert.Empty(t, entries)

	require.NoError(t, os.WriteFile(filepath.Join(role.configDir, "hosts.yml"), []byte("x"), 0o600))
	require.NoError(t, roles.start(t.Context(), cfg, opts))
	entries, err = os.ReadDir(role.configDir)
	require.NoError(t, err)
	assert.Empty(t, entries, "a new start empties the folder that a call filled")
}

func TestWritersStart_AProjectWithNoCredentialsLoadsNothingAndNeedsNoProbe(t *testing.T) {
	t.Parallel()

	roles := &writers{}

	require.NoError(t, roles.start(t.Context(), config.Config{Projects: map[string]config.Project{"p": {}}}, flow.Options{Env: map[string]string{"GH_TOKEN": "x"}}))

	_, found := roles.of("p")
	assert.False(t, found)

	var none *writers

	_, found = none.of("p")
	assert.False(t, found, "a table that holds no writers holds no role")
}

func TestWritersStart_ARefusedCredentialStopsTheStartAndNamesTheProject(t *testing.T) {
	t.Parallel()

	cfg, opts := writerHome(t)
	require.NoError(t, os.Chmod(filepath.Join(opts.Env["HOME"], ".config", "beehave", "credentials", "writer.token"), 0o644))

	err := (&writers{}).start(t.Context(), cfg, opts)

	require.ErrorContains(t, err, "project p: the writer credential")
	require.ErrorContains(t, err, "want 600")
}

func TestWritersStart_AStateFolderThatCannotHoldTheConfigFolderStopsTheStart(t *testing.T) {
	t.Parallel()

	cfg, opts := writerHome(t)
	opts.StateDir = filepath.Join(opts.StateDir, "file")
	require.NoError(t, os.WriteFile(opts.StateDir, []byte("x"), 0o600))

	require.ErrorContains(t, (&writers{}).start(t.Context(), cfg, opts), "project p: make the gh config folder")
}

func TestWritersStart_TheProbeRefusesToStartWhenAnAllowedNameHoldsTheWriterToken(t *testing.T) {
	t.Parallel()

	cfg, opts := writerHome(t)
	opts.Env["TMPDIR"] = "/tmp/" + writerToken

	err := (&writers{}).start(t.Context(), cfg, opts)

	require.ErrorContains(t, err, "holds a credential in TMPDIR")
}

func TestProbe_AChildThatTheEngineStartsDoesNotGetATokenThatThePlantedEnvironmentHolds(t *testing.T) {
	t.Parallel()

	planted := map[string]string{
		"PATH": os.Getenv("PATH"), "GH_TOKEN": "planted-gh-token", "GITHUB_TOKEN": "planted-github-token", "GITLAB_TOKEN": "planted-gitlab-token",
		"GH_ENTERPRISE_TOKEN": "planted-enterprise", "CI_JOB_TOKEN": "planted-ci", "SSH_AUTH_SOCK": "/planted/agent.sock",
	}

	child := exec.CommandContext(t.Context(), "env")
	child.Env = agent.LoginEnv(planted, agent.Profile{}, "")

	out, err := child.Output()

	require.NoError(t, err)
	assert.Contains(t, string(out), "PATH=", "the child runs and prints its environment")
	assert.NotContains(t, string(out), "planted")
	assert.NotContains(t, string(out), "TOKEN")
	assert.NotContains(t, string(out), "SSH_AUTH_SOCK")
}

func TestWriters_AProjectWithNoCredentialsRunsGhWithTheLoginOfTheOwnerAndOneWithUnloadedCredentialsFails(t *testing.T) {
	t.Parallel()

	roles := &writers{}

	runner, err := roles.runner(model.Project{Name: "p"})
	require.NoError(t, err)
	assert.Nil(t, runner.Only, "the owner login keeps the environment of the process")

	_, err = roles.runner(model.Project{Name: "p", Credentials: map[model.Role]model.Credential{model.RoleWriter: {File: "f"}}})
	require.ErrorIs(t, err, errCredentialsNotLoaded)
	require.ErrorContains(t, err, "project p")
}

func TestWriters_TheGhRunnerOfAProjectWithCredentialsGetsOnlyItsRoleEnvironment(t *testing.T) {
	t.Setenv("GH_TOKEN", "owner-token")

	cfg, opts := writerHome(t)
	roles := &writers{}
	require.NoError(t, roles.start(t.Context(), cfg, opts))

	runner, err := roles.runner(cfg.Projects["p"].Project)
	require.NoError(t, err)

	runner.Binary = "env"
	done := runner.Run(t.Context(), nil)

	assert.Contains(t, done.Stdout, "GH_TOKEN="+writerToken+"\n")
	assert.Contains(t, done.Stdout, "GH_CONFIG_DIR="+filepath.Join(opts.StateDir, "roles", "p", "gh")+"\n")
	assert.Contains(t, done.Stdout, "GH_PROMPT_DISABLED=1\n")
	assert.NotContains(t, done.Stdout, "owner-token")
}

func TestAdapters_TheForgeAndTheTrackerOfAProjectWithUnloadedCredentialsFailClosed(t *testing.T) {
	t.Parallel()

	project := model.Project{Name: "p", Repo: "o/r", Forge: forgegithub.Name, Tracker: storegithub.Tracker, Credentials: map[model.Role]model.Credential{model.RoleWriter: {File: "f"}}}
	table := newAdapters(nil, nil, nil)

	_, err := table.forge(project)
	require.ErrorIs(t, err, errCredentialsNotLoaded)

	_, err = table.tracker(t.Context(), project)
	require.ErrorIs(t, err, errCredentialsNotLoaded)
}

func TestNewEngine_ARefusedCredentialStopsTheEngineBeforeItStarts(t *testing.T) {
	t.Parallel()

	cfg, opts := writerHome(t)
	require.NoError(t, os.Chmod(filepath.Join(opts.Env["HOME"], ".config", "beehave", "credentials", "writer.token"), 0o644))

	_, err := newEngine(t.Context(), cfg, opts)
	require.ErrorContains(t, err, "project p: the writer credential")

	_, err = newEngineOn(t.Context(), cfg, opts, nil, newAdapters(nil, nil, opts.Env))
	require.ErrorContains(t, err, "project p: the writer credential")
}

func TestPulls_PushOfAProjectWithCredentialsRunsGitAsTheWriterAndNoCredentialOfTheOwnerReachesIt(t *testing.T) {
	t.Setenv("GH_TOKEN", "owner-token")
	t.Setenv("GITHUB_TOKEN", "owner-github-token")
	t.Setenv("SSH_AUTH_SOCK", "/owner/agent.sock")

	rig := newPushRig(t)
	seen := filepath.Join(t.TempDir(), "hook-env")
	hook := "#!/bin/sh\nprintf '%s|%s|%s|%s|%s\\n' \"${GIT_CONFIG_GLOBAL:-unset}\" \"${GH_TOKEN:-unset}\" \"${GITHUB_TOKEN:-unset}\" \"${SSH_AUTH_SOCK:-unset}\" \"${BEEHAVE_FORGE_TOKEN:-unset}\" >> " + seen + "\nexit 0\n"
	require.NoError(t, exectest.Write(filepath.Join(rig.remote, "hooks", "pre-receive"), hook))

	rig.pulls.writers = &writers{byProject: map[string]writerRole{"p": {token: writerToken}}, env: map[string]string{"PATH": os.Getenv("PATH")}}

	require.NoError(t, rig.pulls.Push(t.Context(), "p", pushItem, pushItem, rig.head, ""))

	got, err := os.ReadFile(seen) //nolint:gosec // a path of this test
	require.NoError(t, err)
	assert.Equal(t, "/dev/null|unset|unset|unset|"+writerToken, strings.TrimSpace(string(got)))
	assert.Equal(t, rig.head, rig.remoteHead(t))
}

func TestPulls_PushOfAProjectWithNoCredentialsKeepsTheEnvironmentOfTheOwner(t *testing.T) {
	t.Setenv("GH_TOKEN", "owner-token")

	rig := newPushRig(t)
	seen := filepath.Join(t.TempDir(), "hook-env")
	require.NoError(t, exectest.Write(filepath.Join(rig.remote, "hooks", "pre-receive"), "#!/bin/sh\nprintf '%s\\n' \"${GH_TOKEN:-unset}\" >> "+seen+"\nexit 0\n"))

	require.NoError(t, rig.pulls.Push(t.Context(), "p", pushItem, pushItem, rig.head, ""))

	got, err := os.ReadFile(seen) //nolint:gosec // a path of this test
	require.NoError(t, err)
	assert.Equal(t, "owner-token", strings.TrimSpace(string(got)))
}
