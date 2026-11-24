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

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/forge"
	forgegithub "github.com/tobocop2/beehave/internal/forge/github"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc"
)

// fakeTip is the commit that a rev-parse in the push repository prints.
const fakeTip = "c0ffee"

// fakeGit is a git that records "<dir>: <args>" for each call. A call whose first word after any --git-dir is in fail exits with code 128.
// A worktree add makes the folder, and a worktree list shows every folder that an add made.
type fakeGit struct {
	mu     sync.Mutex
	calls  []string
	made   []string
	fail   string
	failIn string
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

	if pushRepo := strings.HasPrefix(args[0], "--git-dir="); pushRepo {
		return g.inPushRepository(r.dir, args[1:])
	}

	return g.answer(r.dir, args)
}

// fails reports whether the call of word in dir fails: word is the fail word, and failIn is empty or names dir.
func (g *fakeGit) fails(dir, word string) bool {
	return word == g.fail && (g.failIn == "" || g.failIn == dir)
}

// inPushRepository answers a call in the push repository: a rev-parse prints fakeTip.
func (g *fakeGit) inPushRepository(dir string, args []string) model.CommandResult {
	if !g.fails(dir, args[0]) && args[0] == "rev-parse" {
		return model.CommandResult{Stdout: fakeTip + "\n"}
	}

	return g.answer(dir, args)
}

// answer answers a call in the clone, a worktree or the repos folder.
func (g *fakeGit) answer(dir string, args []string) model.CommandResult {
	if g.fails(dir, args[0]) {
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

// addedFolder makes the folder that a worktree add names and returns it. A new branch puts the folder after the branch name, and a detached add after the flag.
func addedFolder(args []string) string {
	folder := args[2]

	switch folder {
	case "-b":
		folder = args[4]
	case "--force":
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
		projects: map[string]config.Project{"p": {Project: model.Project{Name: "p", Repo: "owner/name", Forge: forgegithub.Name, BaseBranch: "main"}}},
		git:      git.runner, remote: git.runner, forge: fakeForges,
	}

	return trees, git, state
}

// baseFetch is the git calls that fetch branch of the fixture remote into the push repository and then into the clone.
func baseFetch(state, branch string) []string {
	push := filepath.Join(state, "push", "p.git")
	clone := filepath.Join(state, "repos", "p")
	ref := "refs/remotes/origin/" + branch

	return []string{
		push + ": init -q --bare --template= " + push,
		push + ": --git-dir=" + push + " fetch -q --no-tags --no-write-fetch-head https://example.test/owner/name.git +refs/heads/" + branch + ":" + ref,
		push + ": --git-dir=" + push + " rev-parse --verify " + ref + "^{commit}",
		clone + ": fetch -q --no-tags --no-write-fetch-head " + push + " " + ref,
		clone + ": update-ref " + ref + " " + fakeTip,
	}
}

func TestWorktrees_EnsureAddsAWorktreeOnABranchNamedForTheItemFromTheBaseBranch(t *testing.T) {
	t.Parallel()

	trees, git, state := ensureFixture(t)
	clone := filepath.Join(state, "repos", "p")
	want := filepath.Join(state, "trees", "p", "bb-1")

	got, err := trees.Ensure(t.Context(), "p", "bb-1", "", "")

	require.NoError(t, err)
	assert.Equal(t, want, got)
	calls := []string{filepath.Join(state, "repos") + ": clone https://example.test/owner/name.git " + clone}
	calls = append(calls, baseFetch(state, "main")...)
	assert.Equal(t, append(calls,
		clone+": rev-parse --verify --quiet refs/heads/bb-1",
		clone+": worktree add -b bb-1 "+want+" origin/main",
	), git.calls)
}

func TestWorktrees_EnsureClonesFromTheSourceOfTheProjectAndNeverAsksTheForge(t *testing.T) {
	t.Parallel()

	for _, source := range []string{"/srv/repos/shop", "https://example.test/shop.git"} {
		t.Run(source, func(t *testing.T) {
			t.Parallel()

			trees, git, state := ensureFixture(t)
			trees.projects = map[string]config.Project{"p": {Project: model.Project{Name: "p", Source: source, BaseBranch: "main"}}}
			trees.forge = func(model.Project) (forge.Forge, error) { return nil, errFake }

			_, err := trees.Ensure(t.Context(), "p", "fix-login", "", "")

			require.NoError(t, err)
			assert.Equal(t, filepath.Join(state, "repos")+": clone "+source+" "+filepath.Join(state, "repos", "p"), git.calls[0])
		})
	}
}

func TestWorktrees_EnsureAddsTheWorktreeOnAnExistingItemBranchAndNeverResetsIt(t *testing.T) {
	t.Parallel()

	trees, git, state := ensureFixture(t)
	git.branch = true
	clone := filepath.Join(state, "repos", "p")
	want := filepath.Join(state, "trees", "p", "bb-1")

	got, err := trees.Ensure(t.Context(), "p", "bb-1", "", "")

	require.NoError(t, err)
	assert.Equal(t, want, got)
	assert.Equal(t, []string{clone + ": rev-parse --verify --quiet refs/heads/bb-1", clone + ": worktree prune", clone + ": worktree add " + want + " bb-1"}, git.calls[len(git.calls)-3:],
		"a prune drops the record of a removed folder, then a plain add with no force flag takes the branch")

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

	got, err := trees.Ensure(t.Context(), "p", "bb-1", "", "")

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

	first, err := trees.Ensure(t.Context(), "p", "bb-1", "", "")
	require.NoError(t, err)

	git.calls = nil

	again, err := trees.Ensure(t.Context(), "p", "bb-1", "", "")
	require.NoError(t, err)
	assert.Equal(t, first, again)
	assert.Equal(t, []string{first + ": worktree list --porcelain"}, git.calls, "a resume adds nothing")

	git.calls = nil

	_, err = trees.Ensure(t.Context(), "p", "bb-2", "", "")
	require.NoError(t, err)
	assert.Len(t, git.calls, 7, "the second item fetches, looks for its branch and adds, and does not clone again")
	assert.Equal(t, baseFetch(state, "main"), git.calls[:5])
}

func TestWorktrees_EnsureNamesTheGitCallThatFailed(t *testing.T) {
	t.Parallel()

	for _, step := range []string{"clone", "fetch", "worktree"} {
		trees, _, _ := ensureFixture(t)

		failing := (&fakeGit{fail: step}).runner
		trees.git, trees.remote = failing, failing

		got, err := trees.Ensure(t.Context(), "p", "bb-1", "", "")

		require.ErrorContains(t, err, "git "+step)
		require.ErrorContains(t, err, "exited with code 128: fatal: refused")
		assert.Empty(t, got)
	}
}

func TestWorktrees_EnsureNamesTheGitCallOfTheBaseFetchThatFailed(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		remote, clone string
		want          string
	}{
		"the push repository cannot be made":     {remote: "init", want: "git init in "},
		"the fetch from the remote fails":        {remote: "fetch", want: "git fetch in "},
		"the tip in the push repository is gone": {remote: "rev-parse", want: "git rev-parse in "},
		"the fetch into the clone fails":         {clone: "fetch", want: "git fetch in "},
		"the ref of the clone cannot be set":     {clone: "update-ref", want: "git update-ref in "},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			trees, _, state := ensureFixture(t)
			trees.remote = (&fakeGit{fail: c.remote}).runner
			trees.git = (&fakeGit{fail: c.clone}).runner

			got, err := trees.Ensure(t.Context(), "p", "bb-1", "", "")

			dir := filepath.Join(state, "push", "p.git")
			if c.clone != "" {
				dir = filepath.Join(state, "repos", "p")
			}

			require.ErrorContains(t, err, c.want+dir+" exited with code 128")
			assert.Empty(t, got)
		})
	}
}

func TestWorktrees_FetchRefusesAProjectTheConfigLacksAndRunsNoGit(t *testing.T) {
	t.Parallel()

	trees, git, _ := ensureFixture(t)

	err := trees.Fetch(t.Context(), "nope")

	require.ErrorIs(t, err, errUnknownProject)
	assert.Empty(t, git.calls)
}

func TestWorktrees_FetchNamesTheGitCallThatFailed(t *testing.T) {
	t.Parallel()

	t.Run("the forge cannot be built", func(t *testing.T) {
		t.Parallel()

		trees, git, _ := ensureFixture(t)
		trees.forge = func(model.Project) (forge.Forge, error) { return nil, errFake }

		err := trees.Fetch(t.Context(), "p")

		require.ErrorIs(t, err, errFake)
		assert.Empty(t, git.calls, "no git call runs with no address of the remote")
	})

	cases := map[string]struct {
		remote, clone string
	}{
		"the push repository cannot be made": {remote: "init"},
		"the fetch from the remote fails":    {remote: "fetch"},
		"the copy into the clone fails":      {clone: "fetch"},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			trees, _, state := ensureFixture(t)
			remote, local := &fakeGit{fail: c.remote}, &fakeGit{fail: c.clone}
			trees.remote, trees.git = remote.runner, local.runner

			err := trees.Fetch(t.Context(), "p")

			if c.clone == "" {
				require.ErrorContains(t, err, "git "+c.remote+" in "+filepath.Join(state, "push", "p.git")+" exited with code 128")
				assert.Empty(t, local.calls, "the clone gets no call when the push repository fails")

				return
			}

			require.ErrorContains(t, err, "git "+c.clone+" in "+filepath.Join(state, "repos", "p")+" exited with code 128")
		})
	}
}

func TestWorktrees_EnsureFetchesNothingWhenTheForgeOfAClonedProjectCannotBeBuilt(t *testing.T) {
	t.Parallel()

	trees, git, state := ensureFixture(t)
	makeFolder(filepath.Join(state, "repos", "p", ".git"))
	trees.forge = func(model.Project) (forge.Forge, error) { return nil, errFake }

	_, err := trees.Ensure(t.Context(), "p", "bb-1", "", "")

	require.ErrorIs(t, err, errFake)

	for _, call := range git.calls {
		assert.NotContains(t, call, "fetch", "no fetch runs with no address of the remote")
	}
}

func TestWorktrees_EnsureFailsWhenTheForgeOfTheProjectCannotBeBuilt(t *testing.T) {
	t.Parallel()

	trees, git, _ := ensureFixture(t)
	trees.forge = func(model.Project) (forge.Forge, error) { return nil, errFake }

	_, err := trees.Ensure(t.Context(), "p", "bb-1", "", "")

	require.ErrorIs(t, err, errFake)
	assert.Empty(t, git.calls)
}

func TestWorktrees_EnsureRefusesAProjectTheConfigLacks(t *testing.T) {
	t.Parallel()

	trees, git, _ := ensureFixture(t)

	_, err := trees.Ensure(t.Context(), "zzz", "bb-1", "", "")

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

		_, err := trees.Ensure(t.Context(), "p", "bb-1", "", "")

		require.ErrorContains(t, err, "read the folder")
	})

	t.Run("clone unreadable", func(t *testing.T) {
		t.Parallel()

		trees, _, _ := ensureFixture(t)
		require.NoError(t, os.MkdirAll(trees.repos, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(trees.repos, "p"), nil, 0o600))

		_, err := trees.Ensure(t.Context(), "p", "bb-1", "", "")

		require.ErrorContains(t, err, "read the clone of project p")
	})

	t.Run("clone folder cannot be made", func(t *testing.T) {
		t.Parallel()

		trees, _, state := ensureFixture(t)
		require.NoError(t, os.Symlink(filepath.Join(state, "gone"), trees.repos))

		_, err := trees.Ensure(t.Context(), "p", "bb-1", "", "")

		require.ErrorContains(t, err, "make the folder for the clone of project p")
	})

	t.Run("trees folder cannot be made", func(t *testing.T) {
		t.Parallel()

		trees, _, state := ensureFixture(t)
		require.NoError(t, os.MkdirAll(trees.root, 0o700))
		require.NoError(t, os.Symlink(filepath.Join(state, "gone"), filepath.Join(trees.root, "p")))

		_, err := trees.Ensure(t.Context(), "p", "bb-1", "", "")

		require.ErrorContains(t, err, "make the folder for the worktree of item bb-1")
	})
}
