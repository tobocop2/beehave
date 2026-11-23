package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/exectest"
	"github.com/tobocop2/beehave/internal/flow"
)

// basicServer answers every request with a 401 and records the Authorization header of each, so a credential helper is asked on each request.
type basicServer struct {
	mu   sync.Mutex
	auth []string
	*httptest.Server
}

func newBasicServer(t *testing.T) *basicServer {
	t.Helper()

	found := &basicServer{}
	found.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		found.mu.Lock()
		found.auth = append(found.auth, r.Header.Get("Authorization"))
		found.mu.Unlock()

		w.Header().Set("WWW-Authenticate", `Basic realm="r"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(found.Close)

	return found
}

// seen returns the Authorization headers that the server has read.
func (s *basicServer) seen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]string(nil), s.auth...)
}

// noPushCheck accepts every config of a push repository, for a test whose subject is not the check and that sets a git setting there to steer git.
func noPushCheck(context.Context, string) error { return nil }

// reachServer reads the remote on the server through the owner runner of the roles and expects the read to fail with the 401 of the server.
func reachServer(t *testing.T, rig pushRig, roles *writers, server *basicServer) {
	t.Helper()

	trees := rig.pulls.trees
	trees.roles = roles
	repo, err := trees.openPush(t.Context(), "p", server.URL+"/o/r.git")
	require.NoError(t, err)

	_, err = repo.run(t.Context(), "ls-remote", server.URL+"/o/r.git")
	require.Error(t, err)
}

func TestOwnerLogin_AHelperThatAnAgentWroteAfterTheFirstStartNeverRunsOnARestartOrAClientCommand(t *testing.T) { //nolint:paralleltest // t.Setenv changes HOME for the process
	server := newBasicServer(t)
	rig := newPushRig(t)
	helper := filepath.Join(t.TempDir(), "helper")
	require.NoError(t, exectest.Write(helper, "#!/bin/sh\necho username=owner\necho password=secret\n"))
	planted := plantGlobalConfig(t, "[credential]\n\thelper = "+helper+"\n")
	state := t.TempDir()
	opts := flow.Options{StateDir: state}

	require.NoError(t, (&writers{}).start(t.Context(), config.Config{}, opts), "the first start of the owner")

	agentRuns := filepath.Join(t.TempDir(), "agent-runs")
	gitconfig := filepath.Join(planted.home, ".gitconfig")
	text, err := os.ReadFile(gitconfig) //nolint:gosec // a file of the test
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(gitconfig, append(text, []byte("[credential]\n\thelper = !echo ran >> "+agentRuns+"\n")...), 0o600)) //nolint:gosec // a file of the test

	restarted := &writers{}
	require.NoError(t, restarted.start(t.Context(), config.Config{}, opts), "an engine that starts again after the agent wrote")
	reachServer(t, rig, restarted, server)
	assert.Zero(t, runs(t, agentRuns), "the helper that the agent wrote ran on a call of the restarted engine")
	assert.Contains(t, server.seen(), "Basic b3duZXI6c2VjcmV0", "the helper of the owner still answers after the restart")
}

func TestOwnerLogin_ASshCommandInTheConfigOfThePushRepositoryRunsOnNoFetchAndNoPush(t *testing.T) { //nolint:paralleltest // t.Setenv changes HOME for the process
	rig := newPushRig(t)
	planted := plantGlobalConfig(t, "")

	const remote = "ssh://git@nothing.test/o/r.git"

	project := rig.pulls.projects["p"]
	project.Source = remote
	rig.pulls.projects["p"] = project
	rig.pulls.writers = startOwnerLogin(t)
	rig.pulls.trees.roles = rig.pulls.writers

	require.Error(t, rig.pulls.Push(t.Context(), "p", pushItem, pushItem, rig.head, ""), "the first push makes the push repository and fails at the ssh remote")

	_, dir := pushRepository(rig.pulls.trees.repos, "p")
	runGit(t, dir, "config", "core.sshCommand", filepath.Join(filepath.Dir(planted.home), "ssh"))

	err := rig.pulls.Push(t.Context(), "p", pushItem, pushItem, rig.head, "")
	require.ErrorContains(t, err, "core.sshcommand, which beehave does not allow")
	assert.Zero(t, runs(t, planted.sshRuns), "the push ran the ssh command of the push repository config")

	_, err = rig.pulls.trees.openPush(t.Context(), "p", remote)
	require.ErrorContains(t, err, "core.sshcommand, which beehave does not allow")
	assert.Zero(t, runs(t, planted.sshRuns), "the open of the push repository ran the ssh command")
}

func TestOwnerLogin_AHooksPathInTheGlobalConfigRunsNoHookOnAPushThatReachesItsRemote(t *testing.T) { //nolint:paralleltest // t.Setenv changes HOME for the process
	rig := newPushRig(t)
	planted := plantGlobalConfig(t, "")
	rig.pulls.writers = startOwnerLogin(t)

	before := runs(t, planted.hookRuns)
	require.NoError(t, rig.pulls.Push(t.Context(), "p", pushItem, pushItem, rig.head, ""))
	assert.Equal(t, rig.head, rig.remoteHead(t), "the push reached its own remote")
	assert.Equal(t, before, runs(t, planted.hookRuns), "the push ran the hooks of the global config in its remote")

	ownerGit(t, "-C", rig.tree, "push", rig.remote, rig.head+":refs/heads/control")
	assert.Greater(t, runs(t, planted.hookRuns), before, "control: a push that reads the global config runs the hook in the same remote")
}

func TestOwnerLogin_ASshCommandInTheXdgConfigRunsOnNoCloneAndNoFetchOfTheEngine(t *testing.T) { //nolint:paralleltest // t.Setenv changes HOME for the process
	rig := newPushRig(t)
	planted := plantConfigAt(t, "", ".config/git/config")

	const remote = "ssh://git@nothing.test/o/r.git"

	trees := rig.pulls.trees
	trees.roles = startOwnerLogin(t)
	project := rig.pulls.projects["p"]
	project.Source = remote
	require.NoError(t, os.MkdirAll(trees.repos, 0o700))

	require.Error(t, trees.clone(t.Context(), project, filepath.Join(t.TempDir(), "clone")))
	assert.Zero(t, runs(t, planted.sshRuns), "the clone of the engine ran the ssh command of the XDG config")

	before := runs(t, planted.sshRuns)
	ownerGit(t, "ls-remote", remote)
	assert.Greater(t, runs(t, planted.sshRuns), before, "control: a git call that reads the XDG config runs the planted ssh command")
}
