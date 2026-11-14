package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/config"
	forgegithub "github.com/tobocop2/beehave/internal/forge/github"
	"github.com/tobocop2/beehave/internal/lockfile"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc"
)

// strictGit behaves as git does under a race. A clone into a folder that a clone already made fails, and so does a worktree add while another add runs on the same clone.
// A clone and an add take a short time, so overlapping calls meet.
type strictGit struct {
	mu     sync.Mutex
	cloned map[string]bool
	made   []string
	adding map[string]int
	// meet makes a clone wait, up to two seconds, for a second clone to start. peak is the most clones that ran at once.
	meet    bool
	running atomic.Int32
	peak    atomic.Int32
}

func newStrictGit() *strictGit {
	return &strictGit{cloned: map[string]bool{}, adding: map[string]int{}}
}

func (g *strictGit) runner(dir string) proc.Runner { return strictGitIn{g: g, dir: dir} }

type strictGitIn struct {
	g   *strictGit
	dir string
}

func (r strictGitIn) Run(_ context.Context, args []string) model.CommandResult {
	g := r.g

	switch {
	case args[0] == "clone":
		return g.clone(args[2])
	case args[0] == "worktree" && args[1] == "add":
		return g.add(r.dir, addedFolderPath(args))
	case args[0] == "rev-parse":
		return model.CommandResult{ExitCode: 1}
	case args[0] == "worktree" && args[1] == "list":
		g.mu.Lock()
		defer g.mu.Unlock()

		return model.CommandResult{Stdout: "worktree " + strings.Join(g.made, "\nworktree ") + "\n"}
	}

	return model.CommandResult{}
}

func (g *strictGit) clone(dir string) model.CommandResult {
	g.mu.Lock()
	again := g.cloned[dir]
	g.cloned[dir] = true
	g.mu.Unlock()

	if again {
		return model.CommandResult{ExitCode: 128, Stderr: "fatal: destination path '" + dir + "' already exists and is not an empty directory.\n"}
	}

	now := g.running.Add(1)

	g.mu.Lock()
	if now > g.peak.Load() {
		g.peak.Store(now)
	}
	g.mu.Unlock()

	if g.meet {
		for deadline := time.Now().Add(2 * time.Second); g.peak.Load() < 2 && time.Now().Before(deadline); {
			time.Sleep(time.Millisecond)
		}
	}

	time.Sleep(20 * time.Millisecond)
	makeFolder(filepath.Join(dir, ".git"))
	g.running.Add(-1)

	return model.CommandResult{}
}

func (g *strictGit) add(clone, folder string) model.CommandResult {
	g.mu.Lock()
	g.adding[clone]++
	busy := g.adding[clone] > 1
	g.mu.Unlock()

	if busy {
		g.release(clone)

		return model.CommandResult{ExitCode: 255, Stderr: "error: could not lock config file .git/config: File exists\n"}
	}

	time.Sleep(20 * time.Millisecond)
	makeFolder(folder)

	g.mu.Lock()
	g.made = append(g.made, folder)
	g.mu.Unlock()
	g.release(clone)

	return model.CommandResult{}
}

func (g *strictGit) release(clone string) {
	g.mu.Lock()
	g.adding[clone]--
	g.mu.Unlock()
}

func addedFolderPath(args []string) string {
	if args[2] == "-b" {
		return args[4]
	}

	return args[2]
}

func TestWorktrees_EnsureForTwoItemsOfOneProjectAtOnceClonesOnceAndAddsBoth(t *testing.T) {
	t.Parallel()

	state := t.TempDir()
	git := newStrictGit()
	trees := worktrees{
		root: filepath.Join(state, "trees"), repos: filepath.Join(state, "repos"),
		projects: map[string]config.Project{"p": {Project: model.Project{Name: "p", Repo: "owner/name", Forge: forgegithub.Name, BaseBranch: "main"}}},
		git:      git.runner, forge: fakeForges,
	}

	const items = 6

	errs := make([]error, items)

	var wg sync.WaitGroup

	for i := range items {
		wg.Add(1)

		go func() {
			defer wg.Done()

			_, errs[i] = trees.Ensure(t.Context(), "p", fmt.Sprintf("item-%d", i))
		}()
	}

	wg.Wait()

	for _, err := range errs {
		require.NoError(t, err)
	}

	assert.Len(t, git.made, items)
}

func TestWorktrees_EnsureForTwoProjectsAtOnceDoesNotMakeOneWaitForTheOther(t *testing.T) {
	t.Parallel()

	state := t.TempDir()
	git := newStrictGit()
	git.meet = true
	trees := worktrees{
		root: filepath.Join(state, "trees"), repos: filepath.Join(state, "repos"),
		projects: map[string]config.Project{
			"a": {Project: model.Project{Name: "a", Repo: "owner/a", Forge: forgegithub.Name, BaseBranch: "main"}},
			"b": {Project: model.Project{Name: "b", Repo: "owner/b", Forge: forgegithub.Name, BaseBranch: "main"}},
		},
		git: git.runner, forge: fakeForges,
	}

	errs := make([]error, 2)

	var wg sync.WaitGroup

	for i, project := range []string{"a", "b"} {
		wg.Add(1)

		go func() {
			defer wg.Done()

			_, errs[i] = trees.Ensure(t.Context(), project, "item")
		}()
	}

	wg.Wait()

	require.NoError(t, errs[0])
	require.NoError(t, errs[1])
	assert.Equal(t, int32(2), git.peak.Load(), "the clones of two projects must run at the same time")
}

func TestWorktrees_EnsureStopsWaitingForTheCloneLockWhenTheContextEnds(t *testing.T) {
	t.Parallel()

	trees, git, _ := ensureFixture(t)
	require.NoError(t, os.MkdirAll(trees.repos, 0o700))

	held := lockfile.Open(filepath.Join(trees.repos, "p.lock"))
	taken, err := held.TryLock()
	require.NoError(t, err)
	require.True(t, taken)

	t.Cleanup(func() { require.NoError(t, held.Unlock()) })

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()

	_, err = trees.Ensure(ctx, "p", "bb-1")

	require.ErrorContains(t, err, "lock the clone of project p")
	assert.Empty(t, git.calls)
}

func TestWorktrees_EnsureForTwoEnginesOnOneRepoFolderClonesOnce(t *testing.T) {
	t.Parallel()

	state := t.TempDir()
	git := newStrictGit()
	projects := map[string]config.Project{"p": {Project: model.Project{Name: "p", Repo: "owner/name", Forge: forgegithub.Name, BaseBranch: "main"}}}
	// Each value is the worktrees of one engine. Only the folders are shared.
	engine := func() worktrees {
		return worktrees{root: filepath.Join(state, "trees"), repos: filepath.Join(state, "repos"), projects: projects, git: git.runner, forge: fakeForges}
	}

	errs := make([]error, 2)

	var wg sync.WaitGroup

	for i, item := range []string{"one", "two"} {
		wg.Add(1)

		go func() {
			defer wg.Done()

			_, errs[i] = engine().Ensure(t.Context(), "p", item)
		}()
	}

	wg.Wait()

	require.NoError(t, errs[0])
	require.NoError(t, errs[1])
}
