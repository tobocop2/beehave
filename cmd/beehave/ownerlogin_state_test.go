package main

import (
	"context"
	"encoding/base64"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/exectest"
	"github.com/tobocop2/beehave/internal/flow"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc/gitexec/reach"
)

// basicServer answers every request with a 401 and records the Authorization header of each, so a credential helper is asked on each request.
type basicServer struct {
	mu   sync.Mutex
	auth []string
	cert string
	*httptest.Server
}

// newBasicServer starts the server. A secure server is a TLS server whose certificate an engine trusts through GIT_SSL_CAINFO, which an owner call must keep.
func newBasicServer(t *testing.T, secure bool) *basicServer {
	t.Helper()

	found := &basicServer{}
	challenge := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		found.mu.Lock()
		found.auth = append(found.auth, r.Header.Get("Authorization"))
		found.mu.Unlock()

		w.Header().Set("WWW-Authenticate", `Basic realm="r"`)
		w.WriteHeader(http.StatusUnauthorized)
	})

	if !secure {
		found.Server = httptest.NewServer(challenge)
		t.Cleanup(found.Close)

		return found
	}

	found.Server = httptest.NewTLSServer(challenge)
	t.Cleanup(found.Close)

	cert := filepath.Join(t.TempDir(), "ca.pem")
	require.NoError(t, os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: found.Certificate().Raw}), 0o600))
	found.cert = cert

	return found
}

// env returns the environment of an engine that trusts the certificate of the server.
func (s *basicServer) env() map[string]string {
	return map[string]string{"PATH": testPath, "GIT_SSL_CAINFO": s.cert}
}

// seenBasic reports whether a request carried the Basic login user:password.
func (s *basicServer) seenBasic(user, password string) bool {
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+password))

	return slices.Contains(s.seen(), want)
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

// fakeGH puts a fake gh first on PATH. It appends its arguments to the file that the function returns, and prints token for gh auth token. An empty token
// makes it fail, as gh does when there is no login.
func fakeGH(t *testing.T, token string) string {
	t.Helper()

	bin := t.TempDir()
	log := filepath.Join(bin, "gh-calls")
	body := "#!/bin/sh\necho \"$*\" >> " + log + "\n"

	if token == "" {
		body += "exit 1\n"
	} else {
		body += "echo " + token + "\n"
	}

	require.NoError(t, exectest.Write(filepath.Join(bin, "gh"), body))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+testPath)

	return log
}

// ghCalls returns the lines that the fake gh wrote.
func ghCalls(t *testing.T, log string) []string {
	t.Helper()

	data, err := os.ReadFile(log) //nolint:gosec // a file of the test
	if os.IsNotExist(err) {
		return nil
	}

	require.NoError(t, err)

	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func TestOwnerLogin_AnHttpsCallTakesTheTokenOfGhAndNoHelperOfAConfigThatAnAgentWroteBeforeOrAfterTheStart(t *testing.T) { //nolint:paralleltest // t.Setenv changes HOME for the process
	server := newBasicServer(t, true)
	rig := newPushRig(t)
	fakeGH(t, "tok-gh")

	agentRuns := filepath.Join(t.TempDir(), "agent-runs")
	helper := "[credential]\n\thelper = !echo ran >> " + agentRuns + "; echo username=planted; echo password=planted-secret\n"
	planted := plantGlobalConfig(t, helper)
	opts := flow.Options{StateDir: t.TempDir(), Env: server.env()}

	require.NoError(t, (&writers{}).start(t.Context(), config.Config{}, opts), "the first start of the owner")

	gitconfig := filepath.Join(planted.home, ".gitconfig")
	text, err := os.ReadFile(gitconfig) //nolint:gosec // a file of the test
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(gitconfig, append(text, []byte(helper)...), 0o600)) //nolint:gosec // a file of the test

	reachServer(t, rig, startOwnerLoginWith(t, server.env()), server)

	assert.Zero(t, runs(t, agentRuns), "the helper that the agent wrote ran on a call of the restarted engine")
	assert.True(t, server.seenBasic("x-access-token", "tok-gh"), "the server got the token of gh: %v", server.seen())
	assert.False(t, server.seenBasic("planted", "planted-secret"), "the server got the secret of the planted helper")
}

func TestOwnerLogin_AnHttpsCallWithNoGhLoginIsRefusedAndNamesTheHostAndTheLogin(t *testing.T) { //nolint:paralleltest // t.Setenv changes HOME for the process
	server := newBasicServer(t, true)
	rig := newPushRig(t)
	plantGlobalConfig(t, "")
	fakeGH(t, "")

	trees := rig.pulls.trees
	trees.roles = startOwnerLoginWith(t, server.env())
	_, err := trees.openPush(t.Context(), "p", server.URL+"/o/r.git")

	host := strings.TrimPrefix(server.URL, "https://")
	require.ErrorContains(t, err, "the owner has no gh token for "+host+". Run gh auth login --hostname "+host+", or declare credentials for the project")
	assert.Empty(t, server.seen(), "a refused call reaches no server")
}

func TestOwnerLogin_AnHttpCallSendsNoTokenOfGh(t *testing.T) { //nolint:paralleltest // t.Setenv changes HOME for the process
	server := newBasicServer(t, false)
	rig := newPushRig(t)
	plantGlobalConfig(t, "")
	fakeGH(t, "tok-gh")

	reachServer(t, rig, startOwnerLoginWith(t, server.env()), server)

	assert.False(t, server.seenBasic("x-access-token", "tok-gh"), "the token of gh went to an http address")
}

func TestOwnerLogin_TheStartReadsTheTokenOfEachHttpsHostOfTheProjectsWithNoCredentialsOnceAndALaterHostWhenItsCallComes(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	log := fakeGH(t, "tok-gh")
	project := func(source string) config.Project {
		return config.Project{Project: model.Project{Name: "p", Source: source}}
	}
	cfg := config.Config{Projects: map[string]config.Project{
		"a": project("https://one.test/o/r.git"), "b": project("https://one.test/o/s.git"), "c": project("git@two.test:o/r.git"), "d": project("http://three.test/o/r.git"),
		"e": project(""), "f": {Project: model.Project{Name: "f", Source: "https://four.test/o/r.git", Credentials: map[model.Role]model.Credential{model.RoleWriter: {File: "f"}}}},
	}}

	roles := &writers{}
	roles.readOwnerTokens(t.Context(), cfg)
	assert.Equal(t, []string{"auth token --hostname one.test"}, ghCalls(t, log), "one read for the one https host of a project with no credentials")

	token, err := roles.ownerTokenOf("one.test")
	require.NoError(t, err)
	assert.Equal(t, "tok-gh", token)
	assert.Len(t, ghCalls(t, log), 1, "a second call for the host reads nothing")

	_, err = roles.ownerTokenOf("five.test:8443")
	require.NoError(t, err)
	assert.Equal(t, "auth token --hostname five.test:8443", ghCalls(t, log)[1])

	_, err = (*writers)(nil).ownerTokenOf("one.test")
	require.ErrorIs(t, err, reach.ErrNoOwnerToken, "a nil writers holds no token")
}

// httpsProjects names one https project with no credentials, on the host one.test.
func httpsProjects() config.Config {
	return config.Config{Projects: map[string]config.Project{"a": {Project: model.Project{Name: "a", Source: "https://one.test/o/r.git"}}}}
}

func TestOwnerLogin_AFailedReadOfTheTokenIsNotHeldSoTheNextCallReadsItAfterGhAuthLogin(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	roles := &writers{}
	failed := fakeGH(t, "")

	_, err := roles.ownerTokenOf("one.test")
	require.ErrorIs(t, err, reach.ErrNoOwnerToken)
	assert.Len(t, ghCalls(t, failed), 1)

	logged := fakeGH(t, "tok-gh")
	token, err := roles.ownerTokenOf("one.test")
	require.NoError(t, err, "the same engine must read the token after gh auth login")
	assert.Equal(t, "tok-gh", token)

	again, err := roles.ownerTokenOf("one.test")
	require.NoError(t, err)
	assert.Equal(t, "tok-gh", again)
	assert.Len(t, ghCalls(t, logged), 1, "a token that was read is held, so the next call runs no gh")
}

func TestOwnerLogin_TheStartOfTheEngineReadsTheTokenOfEachHttpsHostBeforeAnyCall(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	log := fakeGH(t, "tok-gh")

	roles := &writers{}
	require.NoError(t, roles.start(t.Context(), httpsProjects(), flow.Options{StateDir: t.TempDir(), Env: map[string]string{"PATH": os.Getenv("PATH")}}))

	assert.Equal(t, []string{"auth token --hostname one.test"}, ghCalls(t, log), "the start read no token")
}

func TestOwnerLogin_AnEmptyTokenFromAZeroExitIsRefusedWithTheGhAuthLoginMessage(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	bin := t.TempDir()
	require.NoError(t, exectest.Write(filepath.Join(bin, "gh"), "#!/bin/sh\necho '  '\nexit 0\n"))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+testPath)

	_, err := (&writers{}).ownerTokenOf("one.test")

	require.ErrorIs(t, err, reach.ErrNoOwnerToken)
	require.ErrorContains(t, err, "Run gh auth login --hostname one.test, or declare credentials for the project")
}

func TestOwnerLogin_ASshRemoteKeepsTheAgentSocketAndTheCallReadsNoGitConfig(t *testing.T) {
	rig := newPushRig(t)
	planted := plantGlobalConfig(t, "")
	bin := t.TempDir()
	seen := filepath.Join(bin, "ssh-seen")
	require.NoError(t, exectest.Write(filepath.Join(bin, "ssh"), "#!/bin/sh\necho \"ran $SSH_AUTH_SOCK\" >> "+seen+"\nexit 255\n"))
	path := bin + string(os.PathListSeparator) + testPath
	t.Setenv("PATH", path)

	const remote = "ssh://git@nothing.test/o/r.git"

	trees := rig.pulls.trees
	trees.roles = startOwnerLoginWith(t, map[string]string{"PATH": path, "SSH_AUTH_SOCK": "/agent.sock"})
	repo, err := trees.openPush(t.Context(), "p", remote)
	require.NoError(t, err)

	_, err = repo.run(t.Context(), "ls-remote", remote)
	require.Error(t, err)

	data, err := os.ReadFile(seen) //nolint:gosec // a file of the test
	require.NoError(t, err)
	assert.Contains(t, string(data), "ran /agent.sock", "ssh got the agent socket of the engine")
	assert.Zero(t, runs(t, planted.sshRuns), "the planted ssh command of the global config ran")
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
