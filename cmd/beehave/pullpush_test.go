package main

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/exectest"
	"github.com/tobocop2/beehave/internal/flow"
	"github.com/tobocop2/beehave/internal/forge"
	forgegithub "github.com/tobocop2/beehave/internal/forge/github"
	"github.com/tobocop2/beehave/internal/model"
)

const pushItem = "fix-7"

// pushRig is a bare local remote, the real worktrees of a project cloned from it, and the worktree of pushItem with one new commit.
type pushRig struct {
	remote string
	tree   string
	head   string
	opts   flow.Options
	pulls  pulls
}

func newPushRig(t *testing.T) pushRig {
	t.Helper()

	remote := filepath.Join(evaluated(t, t.TempDir()), "remote.git")
	runGit(t, repoWithCommit(t), "clone", "-q", "--bare", ".", remote)

	projects := map[string]config.Project{"p": {Project: model.Project{Name: "p", Source: remote, BaseBranch: "main", Forge: forgegithub.Name}}}
	opts := flow.Options{StateDir: evaluated(t, t.TempDir())}
	trees := newWorktrees(projects, opts, nil, nil)

	tree, err := trees.Ensure(t.Context(), "p", pushItem, "", "")
	require.NoError(t, err)

	built := func(model.Project) (forge.Forge, error) { return fileForge{dir: t.TempDir()}, nil }

	return pushRig{remote: remote, tree: tree, head: commitIn(t, tree, "one"), opts: opts, pulls: pulls{projects: projects, forge: built, trees: trees, copier: newHeadCopy()}}
}

// commitIn makes an empty commit in dir and returns its id.
func commitIn(t *testing.T, dir, message string) string {
	t.Helper()

	runGit(t, dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", message)

	return gitOut(t, dir, "rev-parse", "HEAD")
}

func (r pushRig) remoteHead(t *testing.T) string {
	t.Helper()

	return gitOut(t, r.remote, "rev-parse", "refs/heads/"+pushItem)
}

func TestPulls_PushRunsGitFromTheEngineWithTheLeaseAndNoHooks(t *testing.T) {
	t.Parallel()

	rig := newPushRig(t)
	ran := filepath.Join(t.TempDir(), "hook-ran")
	hook := filepath.Join(gitOut(t, rig.tree, "rev-parse", "--path-format=absolute", "--git-common-dir"), "hooks", "pre-push")
	require.NoError(t, exectest.Write(hook, "#!/bin/sh\ntouch "+ran+"\nexit 1\n"))

	require.NoError(t, rig.pulls.Push(t.Context(), "p", pushItem, pushItem, rig.head, ""))
	assert.Equal(t, rig.head, rig.remoteHead(t), "the first push of a new branch lands")
	assert.NoFileExists(t, ran, "the pre-push hook of the clone never runs")

	next := commitIn(t, rig.tree, "two")
	require.NoError(t, rig.pulls.Push(t.Context(), "p", pushItem, pushItem, next, rig.head))
	assert.Equal(t, next, rig.remoteHead(t), "a push with the head of the last push as the lease lands")

	// Control: a plain git push in the same tree runs the hook, which refuses it.
	control := exec.CommandContext(t.Context(), "git", "push", "-q", "origin", "HEAD:refs/heads/control")
	control.Dir = rig.tree
	require.Error(t, control.Run(), "the hook refuses a push that runs it")
	assert.FileExists(t, ran)
}

func TestPulls_PushOfAHeadThatTheBranchHoldsAlreadyPasses(t *testing.T) {
	t.Parallel()

	rig := newPushRig(t)
	require.NoError(t, rig.pulls.Push(t.Context(), "p", pushItem, pushItem, rig.head, ""))

	// The replay of a push that landed before its step record carries the lease of the push before it, which the remote no longer holds.
	stale := gitOut(t, rig.tree, "rev-parse", "main")
	require.NoError(t, rig.pulls.Push(t.Context(), "p", pushItem, pushItem, rig.head, stale))
	assert.Equal(t, rig.head, rig.remoteHead(t))
}

func TestPulls_PushToABranchThatMovedIsRefusedAndNamesTheBranch(t *testing.T) {
	t.Parallel()

	rig := newPushRig(t)
	require.NoError(t, rig.pulls.Push(t.Context(), "p", pushItem, pushItem, rig.head, ""))

	other := filepath.Join(t.TempDir(), "other")
	runGit(t, t.TempDir(), "clone", "-q", "-b", pushItem, rig.remote, other)
	moved := commitIn(t, other, "other writer")
	runGit(t, other, "push", "-q", "origin", pushItem)

	next := commitIn(t, rig.tree, "two")
	err := rig.pulls.Push(t.Context(), "p", pushItem, pushItem, next, rig.head)

	require.ErrorContains(t, err, "the branch "+pushItem)
	assert.Equal(t, moved, rig.remoteHead(t), "the lease kept the commit of the other writer")
}

func TestPulls_PushOfAProjectWithNoCloneIsRefusedAndPushesNothing(t *testing.T) {
	t.Parallel()

	rig := newPushRig(t)
	rig.pulls.trees = worktrees{repos: filepath.Join(t.TempDir(), "repos")}

	err := rig.pulls.Push(t.Context(), "p", pushItem, pushItem, rig.head, "")

	require.ErrorIs(t, err, errNoCloneToPush)
	assert.Empty(t, gitOut(t, rig.remote, "branch", "--list", pushItem), "the remote has no branch of the item")
}

func TestPulls_PushWithNoLeaseTakesAFastForwardAndRefusesABranchThatMovedAway(t *testing.T) {
	t.Parallel()

	rig := newPushRig(t)
	require.NoError(t, rig.pulls.Push(t.Context(), "p", pushItem, pushItem, rig.head, ""))

	next := commitIn(t, rig.tree, "two")
	require.NoError(t, rig.pulls.Push(t.Context(), "p", pushItem, pushItem, next, ""), "a fast forward lands with no lease")
	assert.Equal(t, next, rig.remoteHead(t))

	other := filepath.Join(t.TempDir(), "other")
	runGit(t, t.TempDir(), "clone", "-q", "-b", pushItem, rig.remote, other)
	moved := commitIn(t, other, "other writer")
	runGit(t, other, "push", "-q", "origin", pushItem)

	err := rig.pulls.Push(t.Context(), "p", pushItem, pushItem, commitIn(t, rig.tree, "three"), "")
	require.ErrorContains(t, err, "the branch "+pushItem)
	assert.Equal(t, moved, rig.remoteHead(t), "a push with no lease never replaces a commit that the branch holds")
}

func TestDepsOn_TheForgeOfTheEnginePushesACommitOfTheCloneThatTheEngineMade(t *testing.T) {
	t.Parallel()

	rig := newPushRig(t)
	table := newAdapters(nil, nil, nil)
	table.forges[forgegithub.Name] = forgeAdapter{build: rig.pulls.forge}

	deps := depsOn(config.Config{Projects: rig.pulls.projects}, rig.opts, logins{}, table)

	require.NoError(t, deps.Forge.Push(t.Context(), "p", pushItem, pushItem, rig.head, ""))
	assert.Equal(t, rig.head, rig.remoteHead(t))
}

func TestPulls_PushOfAProjectWithASourceAndNoForgePushesToTheSource(t *testing.T) {
	t.Parallel()

	rig := newPushRig(t)
	local := rig.pulls.projects["p"]
	local.Forge = ""
	rig.pulls.projects["p"] = local
	rig.pulls.forge = func(model.Project) (forge.Forge, error) { return nil, forge.ErrNoForge }

	require.NoError(t, rig.pulls.Push(t.Context(), "p", pushItem, pushItem, rig.head, ""))
	assert.Equal(t, rig.head, rig.remoteHead(t), "the branch lands in the source of the project")
}

func TestPulls_PushWithNoAddressToPushToIsRefusedAndPushesNothing(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		project  string
		noSource bool
		want     error
	}{
		"no_source_and_no_forge": {project: "p", noSource: true, want: forge.ErrNoForge},
		"unknown_project":        {project: "missing", want: errUnknownProject},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig := newPushRig(t)
			if tc.noSource {
				rig.pulls.projects["p"] = config.Project{Project: model.Project{Name: "p", BaseBranch: "main"}}
			}
			rig.pulls.forge = func(model.Project) (forge.Forge, error) { return nil, forge.ErrNoForge }

			err := rig.pulls.Push(t.Context(), tc.project, pushItem, pushItem, rig.head, "")

			require.ErrorIs(t, err, tc.want)
			assert.Empty(t, gitOut(t, rig.remote, "branch", "--list", pushItem), "the remote has no branch of the item")
		})
	}
}
