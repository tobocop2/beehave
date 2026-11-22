package main

import (
	"context"
	"net/http"
	"net/http/cgi" //nolint:gosec // a test serves git http-backend on a loopback server; Go 1.27 is past the httpoxy fix
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/flow"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc/gitexec"
	"github.com/tobocop2/beehave/internal/proc/gitexec/reach"
)

// realGate is the gate of project p of the rig as main.go builds it, whose source is the address source: the base read and the fetch of a missing
// base commit go through the worktrees of the rig.
func realGate(rig fetchRig, source string) projectGate {
	projects := map[string]config.Project{"p": {Project: model.Project{Name: "p", Source: source, BaseBranch: "main", Gate: "true", BuildHost: config.LocalHost}}}
	trees := rig.pulls.trees

	return projectGate{
		projects: projects,
		programs: programs{trees: trees, local: localRunner(projects, gitexec.NewRepositories(trees.repos))},
		tip:      trees.BaseTip,
		fetch:    trees.FetchCommit,
	}
}

// An agent writes a remote named for the address of the source, with the address of a decoy that lacks the base commit, into the clone config. The
// key is on the allow list of the clone config. The gate reads the tip and fetches the commit that the clone lacks in the push repository, so the
// clone config does not steer it. The control is the same fetch in the worktree, which the setting sends to the decoy.
func TestProjectGate_AFetchOfAMissingBaseCommitRunsInThePushRepositoryAndARemoteThatTheCloneConfigNamesDoesNotSteerIt(t *testing.T) {
	t.Parallel()

	rig := newFetchRig(t)
	source := "file://" + rig.remote
	rig.plant(t, "remote."+source+".url", rig.decoy)

	out := plainGit(t, rig.tree, "fetch", "-q", "--no-tags", "--", source, rig.tip)
	assert.Contains(t, out, "fatal", "control: the same fetch in the worktree reads the decoy, which lacks the commit: %s", out)
	assert.Contains(t, plainGit(t, rig.tree, "cat-file", "-t", rig.tip), "fatal", "control: the clone lacks the base commit before the gate")

	key, err := realGate(rig, source).Key(t.Context(), "p", pushItem, "", "main", "")

	require.NoError(t, err, "the gate reads and fetches the base commit from the source that the engine read")
	assert.Equal(t, rig.tip, key.BaseSHA)
	assert.Equal(t, "commit", gitOut(t, rig.tree, "cat-file", "-t", rig.tip), "the clone holds the base commit after the gate")
}

// The copy into the clone is a fetch from the push repository, not from the source. When it fails, the hint about the source does not belong on it.
func TestWorktrees_AFailedCopyIntoTheCloneCarriesNoHintAboutTheSource(t *testing.T) {
	t.Parallel()

	rig := newFetchRig(t)
	trees := rig.pulls.trees
	rig.plant(t, "url."+rig.decoy+".insteadOf", rig.remote)

	err := trees.FetchCommit(t.Context(), "p", rig.remote, rig.tip)

	require.ErrorContains(t, err, "which beehave does not allow", "the copy into the clone fails on the clone config")
	assert.NotContains(t, err.Error(), "a fetch by id needs a source", "the hint names the source, and the copy does not read it")
}

// The source serves git over smart HTTP, as a forge does. The gate reads the tip and fetches the base commit by its id, which the clone lacks.
func TestProjectGate_ASourceOverHTTPServesTheBaseCommitByItsId(t *testing.T) {
	t.Parallel()

	gitPath, err := exec.LookPath("git")
	require.NoError(t, err)

	rig := newFetchRig(t)
	server := httptest.NewServer(&cgi.Handler{
		Path: gitPath, Args: []string{"http-backend"},
		Env: []string{"GIT_PROJECT_ROOT=" + filepath.Dir(rig.remote), "GIT_HTTP_EXPORT_ALL=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"},
	})
	t.Cleanup(server.Close)

	key, err := realGate(rig, server.URL+"/"+filepath.Base(rig.remote)).Key(t.Context(), "p", pushItem, "", "main", "")

	require.NoError(t, err, "the gate reads the tip and fetches the commit over HTTP")
	assert.Equal(t, rig.tip, key.BaseSHA)
	assert.Equal(t, "commit", gitOut(t, rig.tree, "cat-file", "-t", rig.tip), "the clone holds the base commit after the gate")
}

func TestWorktrees_BaseTipReadsTheBranchAtTheSourceAndNamesAMissingOrUnreadableOne(t *testing.T) {
	t.Parallel()

	rig := newFetchRig(t)
	trees := rig.pulls.trees

	tip, err := trees.BaseTip(t.Context(), "p", rig.remote, "main")
	require.NoError(t, err)
	assert.Equal(t, rig.tip, tip)

	_, err = trees.BaseTip(t.Context(), "p", rig.remote, "release")
	require.ErrorIs(t, err, gitexec.ErrNoTip)
	require.ErrorContains(t, err, "refs/heads/release")

	_, err = trees.BaseTip(t.Context(), "p", filepath.Join(t.TempDir(), "absent"), "main")
	require.Error(t, err)
	require.NotErrorIs(t, err, gitexec.ErrNoTip)
	require.ErrorContains(t, err, "ls-remote")
}

func TestWorktrees_FetchCommitFailsForACommitTheSourceLacksAndForAClonePastTheAllowList(t *testing.T) {
	t.Parallel()

	rig := newFetchRig(t)
	trees := rig.pulls.trees

	err := trees.FetchCommit(t.Context(), "p", rig.remote, gitOut(t, rig.decoy, "rev-parse", "refs/heads/main"))
	require.ErrorContains(t, err, "git fetch in", "the source does not hold the commit of the decoy")

	rig.plant(t, "url."+rig.decoy+".insteadOf", rig.remote)

	err = trees.FetchCommit(t.Context(), "p", rig.remote, rig.tip)
	require.ErrorContains(t, err, "which beehave does not allow", "the copy into the clone refuses a clone config past the allow list")
}

// A source that speaks protocol version 0 serves only the commits that it advertises. The commit that the base branch moved away from is not
// advertised, so the fetch by its id fails. The error says that a fetch by id needs a source that serves a commit by its id, and that a base that moved needs a new run. Git's text follows.
func TestWorktrees_AFetchByIdThatASourceOfProtocolVersionZeroRefusesSaysThatTheSourceMustServeTheCommitOrTheBaseMoved(t *testing.T) {
	t.Parallel()

	rig := newFetchRig(t)
	trees := rig.pulls.trees
	_, err := trees.BaseTip(t.Context(), "p", rig.remote, "main")
	require.NoError(t, err, "the read makes the push repository")

	_, dir := pushRepository(trees.repos, "p")
	runGit(t, dir, "config", "protocol.version", "0")

	moved := strings.TrimSpace(gitOut(t, rig.remote, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit-tree", rig.tip+"^{tree}", "-p", rig.tip, "-m", "moved away"))
	err = trees.FetchCommit(t.Context(), "p", rig.remote, moved)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "a fetch by id needs a source that serves a commit by its id (git protocol version 2); if the base moved since the read, run again")
	assert.Contains(t, err.Error(), "unadvertised object", "git's own text follows")
}

// Two gates of one project read the base tip at once. The read of a tip does not touch the clone, so the second read starts while the first still
// waits on the source. The source holds each read of its refs until both reads are in flight.
func TestWorktrees_TwoReadsOfTheBaseTipOfOneProjectReachTheSourceAtTheSameTime(t *testing.T) {
	t.Parallel()

	gitPath, err := exec.LookPath("git")
	require.NoError(t, err)

	rig := newFetchRig(t)
	backend := &cgi.Handler{
		Path: gitPath, Args: []string{"http-backend"},
		Env: []string{"GIT_PROJECT_ROOT=" + filepath.Dir(rig.remote), "GIT_HTTP_EXPORT_ALL=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"},
	}

	var (
		inFlight   atomic.Int32
		overlapped atomic.Bool
		both       = make(chan struct{})
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/info/refs") {
			defer inFlight.Add(-1)

			if inFlight.Add(1) == 2 {
				overlapped.Store(true)
				close(both)
			}

			select {
			case <-both:
			case <-time.After(3 * time.Second):
			}
		}

		backend.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)

	source := server.URL + "/" + filepath.Base(rig.remote)
	reads := make(chan error, 2)

	for range 2 {
		go func() {
			_, err := rig.pulls.trees.BaseTip(context.Background(), "p", source, "main")
			reads <- err
		}()
	}

	require.NoError(t, <-reads)
	require.NoError(t, <-reads)

	assert.True(t, overlapped.Load(), "the second read of the tip waited for the first: both reads were never in flight at the source")
}

// A project with writer credentials that are not loaded reaches no remote: the read and the fetch take the runner that the credentials choose.
func TestWorktrees_TheBaseReadAndFetchOfAProjectWithUnloadedCredentialsFailClosed(t *testing.T) {
	t.Parallel()

	project := model.Project{Name: "p", Source: "/src", BaseBranch: "main", Credentials: map[model.Role]model.Credential{model.RoleWriter: {File: "f"}}}
	trees := newWorktrees(map[string]config.Project{"p": {Project: project}}, flow.Options{StateDir: t.TempDir()}, nil, nil)

	_, err := trees.BaseTip(t.Context(), "p", "https://example.invalid/r.git", "main")
	require.ErrorContains(t, err, reach.ErrNotLoaded.Error())

	err = trees.FetchCommit(t.Context(), "p", "https://example.invalid/r.git", anchorOld)
	require.ErrorContains(t, err, reach.ErrNotLoaded.Error())
}
