package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc/gitexec"
	"github.com/tobocop2/beehave/internal/sandbox"
)

// cloneOf gives the sandbox the repos folder that holds the clone dir, as the engine layout does, and gives the project p a source, so it has a clone.
func (g gateSandbox) cloneOf(dir string) gateSandbox {
	g.clones.repos = filepath.Dir(dir)
	project := g.projects["p"]
	project.Source = repositoryProject()["p"].Source
	g.projects["p"] = project

	return g
}

// pointAt rewrites the .git file of the worktree tree so that it names the repository of the folder decoy, as an agent in the tree can.
func pointAt(t *testing.T, tree, decoy string) {
	t.Helper()

	require.NoError(t, os.WriteFile(filepath.Join(tree, ".git"), []byte("gitdir: "+filepath.Join(decoy, ".git")+"\n"), 0o600))
}

// Each call of the worktrees runs git in the clone or in a worktree of it. The clone config names a program that a status or a checkout starts; no
// call starts it, and each names the setting that beehave does not allow.
func TestWorktrees_EngineCallsStartNoProgramThatTheCloneConfigNames(t *testing.T) {
	t.Parallel()

	// project says that the call reads the config of the clone of the project, and not the config of the lane of pushItem, which the worktree of the rig shares.
	calls := map[string]struct {
		run     func(t *testing.T, rig pushRig) error
		project bool
	}{
		"the refresh of an item that has a worktree": {run: func(t *testing.T, rig pushRig) error {
			t.Helper()

			_, err := rig.pulls.trees.Ensure(t.Context(), "p", pushItem, "", "")

			return err
		}},
		"the worktree of a new item": {project: true, run: func(t *testing.T, rig pushRig) error {
			t.Helper()

			_, err := rig.pulls.trees.Ensure(t.Context(), "p", "other", "", "")

			return err
		}},
		"the fetch of the project": {project: true, run: func(t *testing.T, rig pushRig) error {
			t.Helper()

			return rig.pulls.trees.Fetch(t.Context(), "p")
		}},
		"the list of the worktrees": {run: func(t *testing.T, rig pushRig) error {
			t.Helper()

			_, err := rig.pulls.trees.Path(t.Context(), "p", pushItem)

			return err
		}},
	}

	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig := newPushRig(t)
			marker, ran := newMarker(t)

			if call.project {
				rig.config = filepath.Join(rig.pulls.trees.repos, "p", ".git", "config")
			}

			rig.plant(t, "core.fsmonitor", marker)

			err := call.run(t, rig)

			require.ErrorContains(t, err, "holds core.fsmonitor, which beehave does not allow")
			assert.NoFileExists(t, ran, "the engine call ran the program that the clone config names")
		})
	}
}

// An agent writes a program into the config of its own lane. The engine refuses that lane, and the other items go on: a worktree of a new item, the fetch of
// the project and the gate fetch of a commit run no program and fail on no lane but the one that holds the setting.
func TestWorktrees_AProgramInTheConfigOfOneLaneDoesNotStopTheOtherItemsOrRunInThem(t *testing.T) {
	t.Parallel()

	rig := newPushRig(t)
	marker, ran := newMarker(t)
	rig.plant(t, "core.fsmonitor", marker)

	_, err := rig.pulls.trees.Ensure(t.Context(), "p", "other", "", "")
	require.NoError(t, err, "a new item gets a lane of its own, whose config holds no setting")

	require.NoError(t, rig.pulls.trees.Fetch(t.Context(), "p"), "the fetch of the project skips the lane that the engine refuses")

	_, err = rig.pulls.trees.Ensure(t.Context(), "p", pushItem, "", "")
	require.ErrorContains(t, err, "holds core.fsmonitor, which beehave does not allow", "the item of the lane meets the refusal")
	assert.NoFileExists(t, ran, "no engine call ran the program that the lane config names")
}

// The agent rewrites the .git file of its worktree so that it names another repository, with another HEAD and a file that is not committed. The git of
// the worktrees still reads the real tree: the HEAD and the status that the move to the tip reads are those of the worktree that the clone holds.
func TestWorktrees_ARewrittenGitFileDoesNotMoveTheRepository(t *testing.T) {
	t.Parallel()

	rig := newPushRig(t)
	decoy := repoWithCommit(t)
	commitIn(t, decoy, "decoy")
	require.NoError(t, os.WriteFile(filepath.Join(decoy, "untracked"), []byte("x"), 0o600))

	pointAt(t, rig.tree, decoy)

	git := rig.pulls.trees.Git(rig.tree)
	head := git.Run(t.Context(), []string{"rev-parse", "HEAD"})
	status := git.Run(t.Context(), []string{"status", "--porcelain", "--untracked-files=no"})

	require.Zero(t, head.ExitCode, head.Stderr)
	assert.Equal(t, rig.head, strings.TrimSpace(head.Stdout), "the engine reads HEAD of the worktree that the clone holds")
	require.Zero(t, status.ExitCode, status.Stderr)
	assert.Empty(t, strings.TrimSpace(status.Stdout), "the engine reads the status of the worktree that the clone holds")
}

// The clone config names a hooks folder after the copy of the gate is made. The plan of the gate cannot read the hooks folder, so it makes no plan that
// leaves the folder out, and the gate does not run.
func TestGateSandbox_APlanForARefusedCloneRunsNoGate(t *testing.T) {
	t.Parallel()

	repo := repoWithCommit(t)
	runGit(t, repo, "config", "core.hooksPath", t.TempDir())

	box := sandboxFor(t, &fakeEnforcer{}, nil).cloneOf(repo)

	git := gitOf(box.projects, "p", gitexec.NewRepositories(box.clones.repos))
	plan, note := box.plan(t.Context(), box.projects["p"], repo, gitexec.Repository{WorkTree: t.TempDir()}, t.TempDir(), git, false)

	assert.Equal(t, sandbox.Plan{}, plan, "no plan is made")
	assert.Contains(t, note, "holds core.hookspath, which beehave does not allow")
	assert.True(t, strings.HasSuffix(note, " The gate did not run."), note)
}

// The step runs in a linked worktree of a clone in the repos folder, and its .git file names another repository. The plan gets the common folder of
// the clone from the records of the clones; a checkout with its own .git folder gets none.
func TestSandboxPlan_ALinkedWorktreeGetsTheCommonFolderThatItsCloneRecords(t *testing.T) {
	t.Parallel()

	r := newFolderRig(t)
	main := filepath.Join(filepath.Dir(r.work), "main")
	require.NoError(t, os.Mkdir(main, 0o700))
	runGit(t, main, "init", "-q", "-b", "main")
	commitIn(t, main, "start")
	runGit(t, main, "worktree", "add", "-q", "--detach", r.work, "HEAD")
	pointAt(t, r.work, repoWithCommit(t))

	req := request()
	req.Sandbox = &model.Sandbox{}

	_, err := r.f.agents.Run(t.Context(), req)
	require.NoError(t, err)
	assert.Equal(t, []string{filepath.Join(main, ".git")}, r.f.spec.Sandbox.GitDirs, "the common folder comes from the records of the clones")

	plain := newSandboxRig(t)

	_, err = plain.f.agents.Run(t.Context(), req)
	require.NoError(t, err)
	assert.Empty(t, plain.f.spec.Sandbox.GitDirs)
}

// The gate runs in a linked worktree. A gate_allow_write path that holds the clone holds the shared git folder of the worktree, so it is refused. A clone
// whose git folder is somewhere else is not a clone of the engine layout, so the gate does not run there.
func TestGateSandbox_AGateAllowWritePathThatHoldsTheCloneOfTheWorktreeIsRefused(t *testing.T) {
	t.Parallel()

	main := repoWithCommit(t)
	linked := filepath.Join(t.TempDir(), "item")
	runGit(t, main, "worktree", "add", "-q", "--detach", linked, "HEAD")

	box := sandboxFor(t, &fakeEnforcer{}, nil).cloneOf(main)
	found := box.projects["p"]
	found.GateAllowWrite = []string{main}
	box.projects["p"] = found

	out, code := box.run(t.Context(), "p", linked, []string{"true"}, nil, 0)
	assert.Equal(t, sandboxFailed, code)
	assert.Contains(t, out, "overlaps the protected path "+filepath.Join(main, ".git"))

	root := evaluated(t, t.TempDir())
	separate := filepath.Join(root, "clone")
	runGit(t, root, "init", "-q", "-b", "main", "--separate-git-dir", filepath.Join(root, "store"), separate)
	commitIn(t, separate, "start")

	out, code = sandboxFor(t, &fakeEnforcer{}, nil).cloneOf(separate).run(t.Context(), "p", separate, []string{"true"}, nil, 0)
	assert.Equal(t, sandboxFailed, code)
	assert.Contains(t, out, "the folder is not a clone or a worktree that beehave made")
}

// The .git file of the worktree of a gate names a decoy repository. The protected paths of the gate still name the common git folder of the clone,
// from the records of the clones: a gate_allow_write path that holds the clone is refused.
func TestGateSandbox_ARewrittenGitFileDoesNotMoveTheSharedGitFolderOfTheGate(t *testing.T) {
	t.Parallel()

	main := repoWithCommit(t)
	linked := filepath.Join(t.TempDir(), "item")
	runGit(t, main, "worktree", "add", "-q", "--detach", linked, "HEAD")
	decoy := repoWithCommit(t)
	pointAt(t, linked, decoy)

	shared := filepath.Join(main, ".git")

	box := sandboxFor(t, &fakeEnforcer{}, nil).cloneOf(main)
	found := box.projects["p"]
	found.GateAllowWrite = []string{main}
	box.projects["p"] = found

	out, code := box.run(t.Context(), "p", linked, []string{"true"}, nil, 0)
	assert.Equal(t, sandboxFailed, code)
	assert.Contains(t, out, "overlaps the protected path "+shared, "the protected paths hold the common folder of the clone")
}

// The .git file of the item worktree names another repository. The gate copies the worktree from the clone that holds it, and the other repository
// gets no worktree.
func TestGateSandbox_ARewrittenGitFileDoesNotMoveTheRepository(t *testing.T) {
	t.Parallel()

	repo := repoWithCommit(t)
	item := filepath.Join(t.TempDir(), "item")
	runGit(t, repo, "worktree", "add", "-q", "--detach", item, "HEAD")

	decoy := repoWithCommit(t)
	commitIn(t, decoy, "decoy")
	pointAt(t, item, decoy)

	var copied string

	fake := &fakeEnforcer{during: func(work string) { copied = gitOut(t, work, "rev-parse", "HEAD") }}

	out, code := sandboxFor(t, fake, nil).cloneOf(repo).run(t.Context(), "p", item, []string{"true"}, nil, 0)

	require.Zero(t, code, out)
	assert.Equal(t, gitOut(t, repo, "rev-parse", "HEAD"), copied, "the gate works in a copy of the worktree that the clone holds")
	assert.Equal(t, []string{decoy}, worktreesOf(t, decoy), "the other repository gets no worktree and loses none")
}
