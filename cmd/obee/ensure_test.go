package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/config"
	"github.com/tobocop2/obee/internal/forge"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/proc"
)

// fakeGit is a git that records "<dir>: <args>" for each call. A call whose first word is in fail exits with code 128.
// A worktree add makes the folder, and a worktree list shows every folder that an add made.
type fakeGit struct {
	mu     sync.Mutex
	calls  []string
	made   []string
	fail   string
	branch bool
}

// fakeForge names a fixed clone URL.
type fakeForge struct {
	forge.Forge
}

func (fakeForge) CloneURL() string { return "https://example.test/owner/name.git" }

func fakeForges(model.Project) (forge.Forge, error) { return fakeForge{}, nil }

func (g *fakeGit) runner(dir string) proc.Runner { return fakeGitIn{g: g, dir: dir} }

type fakeGitIn struct {
	g   *fakeGit
	dir string
}

func (r fakeGitIn) Run(_ context.Context, args []string) model.CommandResult {
	g := r.g
	g.mu.Lock()
	defer g.mu.Unlock()

	g.calls = append(g.calls, r.dir+": "+strings.Join(args, " "))

	if args[0] == g.fail {
		return model.CommandResult{ExitCode: 128, Stderr: "fatal: refused\n"}
	}

	switch {
	case args[0] == "clone":
		makeFolder(filepath.Join(args[2], ".git"))
	case args[0] == "rev-parse" && !g.branch:
		return model.CommandResult{ExitCode: 1}
	case args[0] == "worktree" && args[1] == "add":
		g.made = append(g.made, addedFolder(args))
	case args[0] == "worktree" && args[1] == "list":
		return model.CommandResult{Stdout: "worktree " + strings.Join(g.made, "\nworktree ") + "\n"}
	}

	return model.CommandResult{}
}

// addedFolder makes the folder that a worktree add names and returns it. A new branch puts the folder after the branch name.
func addedFolder(args []string) string {
	folder := args[2]
	if folder == "-b" {
		folder = args[4]
	}

	makeFolder(folder)

	return folder
}

func makeFolder(path string) {
	if err := os.MkdirAll(path, 0o700); err != nil {
		panic(err)
	}
}

func ensureFixture(t *testing.T) (worktrees, *fakeGit, string) {
	t.Helper()

	state := t.TempDir()
	git := &fakeGit{}
	trees := worktrees{
		root: filepath.Join(state, "trees"), repos: filepath.Join(state, "repos"),
		projects: map[string]config.Project{"p": {Project: model.Project{Name: "p", Repo: "owner/name", BaseBranch: "main"}}},
		git:      git.runner, forge: fakeForges,
	}

	return trees, git, state
}

func TestWorktrees_EnsureAddsAWorktreeOnABranchNamedForTheItemFromTheBaseBranch(t *testing.T) {
	t.Parallel()

	trees, git, state := ensureFixture(t)
	clone := filepath.Join(state, "repos", "p")
	want := filepath.Join(state, "trees", "p", "bb-1")

	got, err := trees.Ensure(t.Context(), "p", "bb-1")

	require.NoError(t, err)
	assert.Equal(t, want, got)
	assert.Equal(t, []string{
		filepath.Join(state, "repos") + ": clone https://example.test/owner/name.git " + clone,
		clone + ": fetch origin main",
		clone + ": rev-parse --verify --quiet refs/heads/bb-1",
		clone + ": worktree add -b bb-1 " + want + " origin/main",
	}, git.calls)
}

func TestWorktrees_EnsureClonesFromTheSourceOfTheProjectAndNeverAsksTheForge(t *testing.T) {
	t.Parallel()

	for _, source := range []string{"/srv/repos/shop", "https://example.test/shop.git"} {
		t.Run(source, func(t *testing.T) {
			t.Parallel()

			trees, git, state := ensureFixture(t)
			trees.projects = map[string]config.Project{"p": {Project: model.Project{Name: "p", Source: source, BaseBranch: "main"}}}
			trees.forge = func(model.Project) (forge.Forge, error) { return nil, errFake }

			_, err := trees.Ensure(t.Context(), "p", "fix-login")

			require.NoError(t, err)
			assert.Equal(t, filepath.Join(state, "repos")+": clone "+source+" "+filepath.Join(state, "repos", "p"), git.calls[0])
		})
	}
}

func TestWorktrees_EnsureWithNoSourceAndNoForgeSaysThereIsNoForge(t *testing.T) {
	t.Parallel()

	trees, git, _ := ensureFixture(t)
	trees.forge = newAdapters(nil, nil).forge
	trees.projects = map[string]config.Project{"p": {Project: model.Project{Name: "p", BaseBranch: "main"}}}

	_, err := trees.Ensure(t.Context(), "p", "fix-login")

	require.ErrorIs(t, err, forge.ErrNoForge)
	assert.Empty(t, git.calls)
}

func TestWorktrees_EnsureAddsTheWorktreeOnAnExistingItemBranchAndNeverResetsIt(t *testing.T) {
	t.Parallel()

	trees, git, state := ensureFixture(t)
	git.branch = true
	clone := filepath.Join(state, "repos", "p")
	want := filepath.Join(state, "trees", "p", "bb-1")

	got, err := trees.Ensure(t.Context(), "p", "bb-1")

	require.NoError(t, err)
	assert.Equal(t, want, got)
	assert.Equal(t, clone+": worktree add "+want+" bb-1", git.calls[len(git.calls)-1])

	for _, call := range git.calls {
		assert.NotContains(t, call, " -B ")
		assert.NotContains(t, call, "reset")
	}
}

func TestWorktrees_EnsureFailsOnAFolderThatGitDoesNotListAndDeletesNothing(t *testing.T) {
	t.Parallel()

	trees, git, state := ensureFixture(t)
	stray := filepath.Join(state, "trees", "p", "bb-1")
	require.NoError(t, os.MkdirAll(stray, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(stray, "keep.txt"), []byte("x"), 0o600))

	got, err := trees.Ensure(t.Context(), "p", "bb-1")

	require.ErrorContains(t, err, stray)
	require.ErrorContains(t, err, "git does not list")
	assert.Empty(t, got)
	assert.FileExists(t, filepath.Join(stray, "keep.txt"))

	for _, call := range git.calls {
		assert.NotContains(t, call, "worktree add")
	}
}

func TestWorktrees_EnsureReusesTheWorktreeOfAResumeAndTheClone(t *testing.T) {
	t.Parallel()

	trees, git, state := ensureFixture(t)

	first, err := trees.Ensure(t.Context(), "p", "bb-1")
	require.NoError(t, err)

	git.calls = nil

	again, err := trees.Ensure(t.Context(), "p", "bb-1")
	require.NoError(t, err)
	assert.Equal(t, first, again)
	assert.Equal(t, []string{first + ": worktree list --porcelain"}, git.calls, "a resume adds nothing")

	git.calls = nil

	_, err = trees.Ensure(t.Context(), "p", "bb-2")
	require.NoError(t, err)
	assert.Len(t, git.calls, 3, "the second item fetches, looks for its branch and adds, and does not clone again")
	assert.Equal(t, filepath.Join(state, "repos", "p")+": fetch origin main", git.calls[0])
}

func TestWorktrees_EnsureNamesTheGitCallThatFailed(t *testing.T) {
	t.Parallel()

	for _, step := range []string{"clone", "fetch", "worktree"} {
		trees, _, _ := ensureFixture(t)

		trees.git = (&fakeGit{fail: step}).runner

		got, err := trees.Ensure(t.Context(), "p", "bb-1")

		require.ErrorContains(t, err, "git "+step)
		require.ErrorContains(t, err, "exited with code 128: fatal: refused")
		assert.Empty(t, got)
	}
}

func TestWorktrees_EnsureFailsWhenTheForgeOfTheProjectCannotBeBuilt(t *testing.T) {
	t.Parallel()

	trees, git, _ := ensureFixture(t)
	trees.forge = func(model.Project) (forge.Forge, error) { return nil, errFake }

	_, err := trees.Ensure(t.Context(), "p", "bb-1")

	require.ErrorIs(t, err, errFake)
	assert.Empty(t, git.calls)
}

func TestWorktrees_EnsureRefusesAProjectTheConfigLacks(t *testing.T) {
	t.Parallel()

	trees, git, _ := ensureFixture(t)

	_, err := trees.Ensure(t.Context(), "zzz", "bb-1")

	require.ErrorIs(t, err, errUnknownProject)
	assert.Empty(t, git.calls)
}

func TestWorktrees_EnsureReportsAFolderItCannotRead(t *testing.T) {
	t.Parallel()

	t.Run("item folder unreadable", func(t *testing.T) {
		t.Parallel()

		trees, _, _ := ensureFixture(t)
		require.NoError(t, os.MkdirAll(trees.root, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(trees.root, "p"), nil, 0o600))

		_, err := trees.Ensure(t.Context(), "p", "bb-1")

		require.ErrorContains(t, err, "read the folder")
	})

	t.Run("clone unreadable", func(t *testing.T) {
		t.Parallel()

		trees, _, _ := ensureFixture(t)
		require.NoError(t, os.WriteFile(trees.repos, nil, 0o600))

		_, err := trees.Ensure(t.Context(), "p", "bb-1")

		require.ErrorContains(t, err, "read the clone of project p")
	})

	t.Run("clone folder cannot be made", func(t *testing.T) {
		t.Parallel()

		trees, _, state := ensureFixture(t)
		require.NoError(t, os.Symlink(filepath.Join(state, "gone"), trees.repos))

		_, err := trees.Ensure(t.Context(), "p", "bb-1")

		require.ErrorContains(t, err, "make the folder for the clone of project p")
	})

	t.Run("trees folder cannot be made", func(t *testing.T) {
		t.Parallel()

		trees, _, state := ensureFixture(t)
		require.NoError(t, os.MkdirAll(trees.root, 0o700))
		require.NoError(t, os.Symlink(filepath.Join(state, "gone"), filepath.Join(trees.root, "p")))

		_, err := trees.Ensure(t.Context(), "p", "bb-1")

		require.ErrorContains(t, err, "make the folder for the worktree of item bb-1")
	})
}
