package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/exectest"
	"github.com/tobocop2/beehave/internal/flow"
	"github.com/tobocop2/beehave/internal/forge"
	forgegithub "github.com/tobocop2/beehave/internal/forge/github"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc/gitexec"
)

// plantedFsmonitor writes a global git config that names a core.fsmonitor command, in the HOME and XDG_CONFIG_HOME of the test, and returns the file that
// the command appends one line to at each run.
func plantedFsmonitor(t *testing.T) string {
	t.Helper()

	home := evaluated(t, t.TempDir())
	ran := filepath.Join(evaluated(t, t.TempDir()), "ran")
	command := filepath.Join(home, "fsmonitor")
	require.NoError(t, exectest.Write(command, "#!/bin/sh\necho run >> "+ran+"\n"))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("[core]\n\tfsmonitor = "+command+"\n"), 0o600))

	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	require.NoError(t, os.Unsetenv("GIT_CONFIG_GLOBAL"))

	return ran
}

func monitorRuns(t *testing.T, ran string) int {
	t.Helper()

	data, err := os.ReadFile(ran) //nolint:gosec // a file of the test
	if os.IsNotExist(err) {
		return 0
	}

	require.NoError(t, err)

	return strings.Count(string(data), "run")
}

func TestEngineGit_NoCallOnAnItemWorktreeRunsTheFsmonitorOfTheUserGlobalConfig(t *testing.T) { //nolint:paralleltest // t.Setenv changes the git config of the process
	remote := filepath.Join(evaluated(t, t.TempDir()), "remote.git")
	runGit(t, repoWithCommit(t), "clone", "-q", "--bare", ".", remote)

	projects := map[string]config.Project{"p": {Project: model.Project{Name: "p", Source: remote, BaseBranch: "main", Forge: forgegithub.Name}}}
	trees := newWorktrees(projects, flow.Options{StateDir: evaluated(t, t.TempDir())}, nil, nil)
	built := func(model.Project) (forge.Forge, error) { return fileForge{dir: t.TempDir()}, nil }
	pushes := pulls{projects: projects, forge: built, trees: trees, copier: newHeadCopy()}
	runs := programs{local: localRunner(projects, gitexec.NewRepositories(trees.repos))}

	ran := plantedFsmonitor(t)

	control := filepath.Join(evaluated(t, t.TempDir()), "control")
	runGit(t, remote, "worktree", "add", "-q", "--detach", control, "main")
	runGit(t, control, "status", "--porcelain")
	require.Positive(t, monitorRuns(t, ran), "control: a plain git status in a worktree runs the planted command")

	var tree, head string

	steps := []struct {
		name string
		do   func()
	}{
		{"create the worktree", func() {
			var err error

			tree, err = trees.Ensure(t.Context(), "p", pushItem, "", "")
			require.NoError(t, err)
		}},
		{"read the status", func() {
			_, err := runs.git(t.Context(), "p", tree, time.Minute, "git status", "status", "--porcelain")
			require.NoError(t, err)
		}},
		{"commit", func() {
			require.NoError(t, os.WriteFile(filepath.Join(tree, "new.txt"), []byte("x\n"), 0o600))
			git := trees.Git(tree)
			require.Zero(t, git.Run(t.Context(), []string{"add", "new.txt"}).ExitCode)
			out := git.Run(t.Context(), []string{"-c", "user.name=e", "-c", "user.email=e@example.com", "commit", "-q", "-m", "x"})
			require.Zero(t, out.ExitCode, out.Stderr)
			head = strings.TrimSpace(git.Run(t.Context(), []string{"rev-parse", "HEAD"}).Stdout)
		}},
		{"push to the remote", func() {
			require.NoError(t, pushes.Push(t.Context(), "p", pushItem, pushItem, head, ""))
		}},
		{"move the worktree", func() {
			require.NoError(t, trees.moveTo(t.Context(), tree, "HEAD"))
			require.NoError(t, trees.refuseUnpushed(t.Context(), trees.repos, tree, pushItem), "the commit that moveTo placed holds no work of the item")
		}},
		{"add and remove a detached worktree", func() {
			dir, err := trees.detach(t.Context(), projects["p"], "copy", head)
			require.NoError(t, err)
			require.NoError(t, trees.drop(t.Context(), "p", dir))
		}},
	}

	for _, step := range steps {
		before := monitorRuns(t, ran)

		step.do()

		assert.Equal(t, before, monitorRuns(t, ran), "%s: the planted fsmonitor command of the user config ran", step.name)
	}
}
