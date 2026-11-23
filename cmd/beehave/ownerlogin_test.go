package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/exectest"
	"github.com/tobocop2/beehave/internal/flow"
	"github.com/tobocop2/beehave/internal/model"
)

// plant is a global git config under a temporary HOME that an agent could have written, with a counter for each program it names.
type plant struct {
	home, decoy, sshRuns, hookRuns string
}

// plantGlobalConfig sets HOME to a new folder whose .gitconfig names an ssh command, a hooks folder and a url rewrite that sends https://example.test/o/ to a
// bare repository in the folder decoy. Each program appends a line to a counter file. extra is more text for the config file.
func plantGlobalConfig(t *testing.T, extra string) plant {
	t.Helper()

	return plantConfigAt(t, extra, ".gitconfig")
}

// plantConfigAt is plantGlobalConfig with the config file at the path file under HOME, as .config/git/config is for the XDG folder.
func plantConfigAt(t *testing.T, extra, file string) plant {
	t.Helper()

	root := evaluated(t, t.TempDir())
	found := plant{home: filepath.Join(root, "home"), decoy: filepath.Join(root, "decoy"), sshRuns: filepath.Join(root, "ssh-runs"), hookRuns: filepath.Join(root, "hook-runs")}
	require.NoError(t, os.MkdirAll(found.home, 0o700))
	require.NoError(t, os.MkdirAll(found.decoy, 0o700))

	ssh := filepath.Join(root, "ssh")
	require.NoError(t, exectest.Write(ssh, "#!/bin/sh\necho ran >> "+found.sshRuns+"\nexit 1\n"))

	hooks := filepath.Join(root, "hooks")
	require.NoError(t, os.MkdirAll(hooks, 0o700))
	require.NoError(t, exectest.Write(filepath.Join(hooks, "pre-receive"), "#!/bin/sh\necho ran >> "+found.hookRuns+"\nexit 0\n"))
	runGit(t, found.decoy, "init", "-q", "--bare", "r.git")

	text := "[core]\n\tsshCommand = " + ssh + "\n\thooksPath = " + hooks + "\n[url \"file://" + found.decoy + "/\"]\n\tinsteadOf = https://example.test/o/\n" + extra
	require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(found.home, file)), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(found.home, file), []byte(text), 0o600))
	t.Setenv("HOME", found.home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(found.home, ".config"))
	t.Setenv("GIT_CONFIG_GLOBAL", "")
	require.NoError(t, os.Unsetenv("GIT_CONFIG_GLOBAL"))

	return found
}

// runs returns how many times a planted program ran.
func runs(t *testing.T, counter string) int {
	t.Helper()

	data, err := os.ReadFile(counter) //nolint:gosec // a file of the test
	if os.IsNotExist(err) {
		return 0
	}

	require.NoError(t, err)

	return strings.Count(string(data), "ran")
}

// startOwnerLogin starts the writers of an engine for a config with no project that has credentials, which is the start that reads the owner login.
func startOwnerLogin(t *testing.T) *writers {
	t.Helper()

	return startOwnerLoginWith(t, map[string]string{"PATH": testPath})
}

// startOwnerLoginWith is startOwnerLogin with the environment env, which an owner call takes its network names from.
func startOwnerLoginWith(t *testing.T, env map[string]string) *writers {
	t.Helper()

	roles := &writers{}
	require.NoError(t, roles.start(t.Context(), config.Config{}, flow.Options{StateDir: t.TempDir(), Env: env}))

	return roles
}

func TestOwnerLogin_ASshCommandInTheGlobalConfigRunsOnNoCloneAndNoFetchOfTheEngine(t *testing.T) { //nolint:paralleltest // t.Setenv changes HOME for the process
	rig := newPushRig(t)
	planted := plantGlobalConfig(t, "")

	const remote = "ssh://git@nothing.test/o/r.git"

	trees := rig.pulls.trees
	trees.roles = startOwnerLogin(t)
	project := rig.pulls.projects["p"]
	project.Source = remote
	require.NoError(t, os.MkdirAll(trees.repos, 0o700))

	require.Error(t, trees.clone(t.Context(), project, filepath.Join(t.TempDir(), "clone")))
	assert.Zero(t, runs(t, planted.sshRuns), "the clone of the engine ran the ssh command of the global config")

	repo, err := trees.openPush(t.Context(), "p", remote)
	require.NoError(t, err)

	_, err = repo.run(t.Context(), "fetch", "-q", remote, "+refs/heads/*:refs/remotes/x/*")
	require.Error(t, err)
	assert.Zero(t, runs(t, planted.sshRuns), "the fetch of the engine ran the ssh command of the global config")

	before := runs(t, planted.sshRuns)
	ownerGit(t, "ls-remote", remote)
	assert.Equal(t, before+1, runs(t, planted.sshRuns), "control: a git call that reads the global config runs the planted ssh command")
}

func TestOwnerLogin_AUrlRewriteAndAHooksPathInTheGlobalConfigNeitherRedirectNorRunOnThePushOfTheEngine(t *testing.T) { //nolint:paralleltest // t.Setenv changes HOME for the process
	rig := newPushRig(t)

	const address = "https://example.test/o/r.git"

	project := rig.pulls.projects["p"]
	project.Source = address
	rig.pulls.projects["p"] = project
	planted := plantGlobalConfig(t, "")
	rig.pulls.writers = startOwnerLogin(t)

	err := rig.pulls.Push(t.Context(), "p", pushItem, pushItem, rig.head, "")

	assert.Error(t, err, "the address does not resolve, so the push cannot land anywhere") //nolint:testifylint // the checks below read the effects of a push that failed
	assert.Zero(t, runs(t, planted.hookRuns), "the push of the engine ran the hooks of the global config in the decoy")
	assert.Empty(t, gitOut(t, filepath.Join(planted.decoy, "r.git"), "for-each-ref"), "the push of the engine landed in the decoy of the url rewrite")

	before := runs(t, planted.hookRuns)
	ownerGit(t, "-C", rig.tree, "push", address, rig.head+":refs/heads/control")
	assert.Equal(t, before+1, runs(t, planted.hookRuns), "control: a push that reads the global config reaches the decoy and runs its hook")
}

// ownerGit runs git with the environment of the process, which holds the planted global config, and ignores the exit code.
func ownerGit(t *testing.T, args ...string) {
	t.Helper()

	if err := exec.CommandContext(t.Context(), "git", args...).Run(); err != nil { //nolint:gosec // a test runs git on its own temporary folders
		t.Logf("git %v: %v", args, err)
	}
}

func TestOwnerLogin_AClientStartReadsTheTokenOfTheHttpsHostOfAProjectWithNoCredentials(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	log := fakeGH(t, "tok-gh")
	opts := flow.Options{StateDir: t.TempDir() + "/state", Version: "v1", MaxConcurrent: 1, MaxReviewConcurrent: 1, Env: map[string]string{}}
	cfg := config.Config{Projects: map[string]config.Project{"p": {Project: model.Project{Name: "p", Source: "https://example.test/o/r.git"}}}}

	_, err := flowFactory().Client(t.Context(), cfg, opts)

	require.NoError(t, err)
	assert.Equal(t, []string{"auth token --hostname example.test"}, ghCalls(t, log))
}
