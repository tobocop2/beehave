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
	"github.com/tobocop2/beehave/internal/flow"
	forgegithub "github.com/tobocop2/beehave/internal/forge/github"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc"
	"github.com/tobocop2/beehave/internal/proc/gitexec"
	storegithub "github.com/tobocop2/beehave/internal/store/github"
)

const (
	writerToken = "writer-token-value"
	// testPath holds git and env on every machine that runs the tests.
	testPath = "/usr/bin:/bin"
)

// loosen gives the token file of the home folder the mode 0644, which the loader refuses.
func loosen(t *testing.T, home string) {
	t.Helper()

	mode := os.FileMode(0o644)
	require.NoError(t, os.Chmod(filepath.Join(home, ".config", "beehave", "credentials", "writer.token"), mode))
}

// writerHome makes a home folder with the writer token file and returns the options of an engine that runs with it and the config of a project that names the file.
func writerHome(t *testing.T) (config.Config, flow.Options) {
	t.Helper()

	home := evaluated(t, t.TempDir())
	folder := filepath.Join(home, ".config", "beehave", "credentials")
	require.NoError(t, os.MkdirAll(folder, 0o700))
	require.NoError(t, exectest.ChmodFolder(folder, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(folder, "writer.token"), []byte(writerToken+"\n"), 0o600))

	project := model.Project{Name: "p", Repo: "o/r", Credentials: map[model.Role]model.Credential{model.RoleWriter: {File: "~/.config/beehave/credentials/writer.token"}}}
	opts := flow.Options{StateDir: evaluated(t, t.TempDir()), Env: map[string]string{"HOME": home, "PATH": testPath}}

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
	loosen(t, opts.Env["HOME"])

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

	planted := map[string]string{ //nolint:gosec // G101: the values are planted test strings, not credentials
		"PATH": testPath, "GH_TOKEN": "planted-gh-token", "GITHUB_TOKEN": "planted-github-token", "GITLAB_TOKEN": "planted-gitlab-token",
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
	loosen(t, opts.Env["HOME"])

	_, err := newEngine(t.Context(), cfg, opts)
	require.ErrorContains(t, err, "project p: the writer credential")

	_, err = newEngineOn(t.Context(), cfg, opts, nil, newAdapters(nil, nil, opts.Env))
	require.ErrorContains(t, err, "project p: the writer credential")
}

func TestPulls_PushOfAProjectWithCredentialsRunsGitAsTheWriterAndNoCredentialOfTheOwnerReachesIt(t *testing.T) {
	t.Setenv("GH_TOKEN", "owner-token")
	t.Setenv("GITHUB_TOKEN", "owner-github-token")
	t.Setenv("SSH_AUTH_SOCK", "/owner/agent.sock")

	rig := credentialRig(t)
	seen := filepath.Join(t.TempDir(), "hook-env")
	hook := "#!/bin/sh\nprintf '%s|%s|%s|%s|%s\\n' \"${GIT_CONFIG_GLOBAL:-unset}\" \"${GH_TOKEN:-unset}\" \"${GITHUB_TOKEN:-unset}\" \"${SSH_AUTH_SOCK:-unset}\" \"${BEEHAVE_FORGE_TOKEN:-unset}\" >> " + seen + "\nexit 0\n"
	require.NoError(t, exectest.Write(filepath.Join(rig.remote, "hooks", "pre-receive"), hook))

	require.NoError(t, rig.pulls.Push(t.Context(), "p", pushItem, pushItem, rig.head, ""))

	got, err := os.ReadFile(seen) //nolint:gosec // a path of this test
	require.NoError(t, err)
	assert.Equal(t, "/dev/null|unset|unset|unset|"+writerToken, strings.TrimSpace(string(got)))
	assert.Equal(t, rig.head, rig.remoteHead(t))
}

func TestWritersStart_ASourceOfAProjectWithCredentialsMustBeAnHTTPSAddress(t *testing.T) {
	t.Parallel()

	for source, accepted := range map[string]bool{
		"https://github.com/o/r.git": true, "": true,
		"git@github.com:o/r.git": false, "ssh://git@github.com/o/r.git": false, "git+ssh://github.com/o/r.git": false, "ssh+git://h/o/r": false,
		"host:path": false, "h.com:o/r.git": false, "git://github.com/o/r.git": false, "http://github.com/o/r.git": false,
		"file:///srv/repo": false, "/srv/repo": false, "./repo": false,
	} {
		cfg, opts := writerHome(t)
		project := cfg.Projects["p"]
		project.Source = source
		cfg.Projects["p"] = project

		err := (&writers{}).start(t.Context(), cfg, opts)

		if accepted {
			require.NoError(t, err, source)
		} else {
			require.ErrorContains(t, err, "project p: the writer credential is a token for https", source)
		}
	}
}

func TestWorktrees_AGitCallToTheRemoteRunsAsTheWriterOnlyForAProjectWithCredentialsAndAnHTTPSAddress(t *testing.T) {
	t.Parallel()

	owner := &fakeGit{}
	loaded := &writers{byProject: map[string]writerRole{"p": {token: writerToken}}, env: map[string]string{"PATH": testPath, "HOME": "/owner"}}
	trees := worktrees{remote: owner.runner, roles: loaded}

	role, isGit := trees.reach("p", "/clone", "https://github.com/o/r.git").(gitexec.Runner)
	require.True(t, isGit, "a project with credentials reaches its remote through the writer runner")
	assert.Contains(t, role.Only, "GIT_CONFIG_GLOBAL=/dev/null")
	assert.Contains(t, role.Only, "BEEHAVE_FORGE_TOKEN="+writerToken)
	assert.Contains(t, role.Only, "BEEHAVE_FORGE_HOST=github.com")
	assert.NotContains(t, strings.Join(role.Only, "\n"), "HOME=")

	out := trees.reach("p", "/clone", "git@github.com:o/r.git").Run(t.Context(), []string{"fetch"})
	assert.Equal(t, 1, out.ExitCode, "a remote that is not https is refused for the writer")
	assert.Contains(t, out.Stderr, "project p: the writer credential is a token for https")

	_ = trees.reach("q", "/clone", "https://github.com/o/r.git").Run(t.Context(), []string{"fetch"})
	assert.Len(t, owner.calls, 1, "a project with no credentials keeps the runner of the owner")

	_ = worktrees{remote: owner.runner}.reach("p", "/clone", "/srv/repo").Run(t.Context(), []string{"fetch"})
	assert.Len(t, owner.calls, 2, "a table with no writers keeps the runner of the owner for a project with no credentials")
}

func TestWorktrees_AClientThatHoldsNoWriterRefusesTheRemoteCallOfAProjectWithCredentials(t *testing.T) {
	t.Parallel()

	cfg, _ := writerHome(t)
	project := cfg.Projects["p"]
	project.Source = "https://github.com/o/r.git"
	cfg.Projects["p"] = project

	deps := releaseDeps(cfg, flow.Options{StateDir: t.TempDir()}, newAdapters(nil, nil, nil))
	trees, ok := deps.Trees.(worktrees)
	require.True(t, ok)

	_, err := trees.Heads(t.Context(), "p")
	require.ErrorContains(t, err, "the credentials of the project are not loaded")
}

func TestWorktrees_TheSourceOfTheProgramBuildsWorktreesOnlyInTheConstructorWithTheWritersAndPicksTheOwnerRunnerInReachOnly(t *testing.T) {
	t.Parallel()

	files, err := filepath.Glob("*.go")
	require.NoError(t, err)

	owners, builders, literals := 0, 0, 0

	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}

		text, readErr := os.ReadFile(file) //nolint:gosec // a source file of this package
		require.NoError(t, readErr)

		owners += strings.Count(string(text), "w.remote(")
		builders += strings.Count(string(text), "newWorktrees(cfg.Projects, opts, table.forge, table.writers)")

		literals += strings.Count(string(text), "worktrees{")
	}

	assert.Equal(t, 1, literals, "only the constructor builds the type, so no literal leaves the roles unset")
	assert.Equal(t, 1, owners, "the runner of the owner is picked in reach only")
	assert.Equal(t, 2, builders, "the engine and the client both pass the writers of their table")
}

func TestWorktrees_TheCloneOfAProjectWithCredentialsRunsThroughTheWriterRunnerAndNeverTheOwnerRunner(t *testing.T) {
	t.Parallel()

	rig := newPushRig(t)
	trees := rig.pulls.trees
	trees.roles = &writers{byProject: map[string]writerRole{"p": {token: writerToken}}, env: map[string]string{"PATH": testPath}}
	trees.remote = func(string) proc.Runner { panic("the clone used the runner of the owner") }

	project := rig.pulls.projects["p"]
	project.Source = "https://nothing.test/o/r.git" // the reserved .test domain never resolves, so git fails and no network is reached
	require.NoError(t, os.MkdirAll(trees.repos, 0o700))

	err := trees.clone(t.Context(), project, filepath.Join(t.TempDir(), "clone"))
	require.ErrorContains(t, err, "git clone in "+trees.repos+" exited with code")
}

func TestWorktrees_TheReadOfTheBranchesOfAProjectWithCredentialsRunsAsTheWriter(t *testing.T) {
	t.Parallel()

	rig := credentialRig(t)
	trees := rig.pulls.trees
	trees.roles = rig.pulls.writers
	trees.remote = func(string) proc.Runner { panic("the read used the runner of the owner") }

	heads, err := trees.Heads(t.Context(), "p")
	require.NoError(t, err)
	assert.Contains(t, heads, "refs/heads/main")
}

// credentialRig is a push rig whose project has an https address, which the push repository maps to the local bare remote with url.insteadOf. The writer is
// loaded for the project, so the calls to the remote run as the writer.
func credentialRig(t *testing.T) pushRig {
	t.Helper()

	const address = "https://example.test/o/r.git"

	rig := newPushRig(t)
	project := rig.pulls.projects["p"]
	project.Source = address
	rig.pulls.projects["p"] = project
	rig.pulls.writers = &writers{byProject: map[string]writerRole{"p": {token: writerToken}}, env: map[string]string{"PATH": testPath}}

	root, dir := pushRepository(rig.pulls.trees.repos, "p")
	require.NoError(t, openPushRepository(t.Context(), pushGit(dir), root, dir))
	runGit(t, dir, "config", "url."+rig.remote+".insteadOf", address)

	return rig
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
