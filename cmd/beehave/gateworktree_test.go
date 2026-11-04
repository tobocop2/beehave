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
)

// repoWithCommit makes a git repository with one commit and returns its folder.
func repoWithCommit(t *testing.T) string {
	t.Helper()

	// git reports real paths, and on macOS the temp folder sits behind a link.
	dir := evaluated(t, t.TempDir())
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README"), []byte("hi\n"), 0o600))
	runGit(t, dir, "init", "-q", "-b", "main")
	runGit(t, dir, "add", "README")
	runGit(t, dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "start")

	return dir
}

// worktreesOf lists the worktree folders that the repository in dir records.
func worktreesOf(t *testing.T, dir string) []string {
	t.Helper()

	command := exec.CommandContext(t.Context(), "git", "worktree", "list", "--porcelain")
	command.Dir = dir

	out, err := command.Output()
	require.NoError(t, err)

	var folders []string

	for _, line := range strings.Split(string(out), "\n") {
		if folder, ok := strings.CutPrefix(line, "worktree "); ok {
			folders = append(folders, folder)
		}
	}

	return folders
}

func TestGateSandbox_AGitWorkTreeRunsInADisposableWorktreeAndTheRealFolderIsOutsideThePlan(t *testing.T) {
	t.Parallel()

	dir := repoWithCommit(t)
	fake := &fakeEnforcer{}

	_, code := sandboxFor(t, fake, nil).run(t.Context(), "p", dir, []string{"true"}, nil, 0)
	require.Zero(t, code)

	assert.NotEqual(t, dir, fake.dir, "the command works in a copy")
	assert.Equal(t, "work", filepath.Base(fake.dir))
	assert.Contains(t, fake.plan.Write, fake.dir, "the copy is writable whole")
	assert.Contains(t, fake.plan.Read, fake.dir)
	assert.NotContains(t, fake.plan.Write, dir)
	assert.NotContains(t, fake.plan.Read, dir, "the real folder is not in the plan")
	assert.Equal(t, []string{dir}, worktreesOf(t, dir), "the worktree of the gate is gone from the repository")
	assert.NoDirExists(t, fake.dir)
}

func TestGateSandbox_ALinkedWorkTreeKeepsItsSharedGitFolderReadOnlyInThePlan(t *testing.T) {
	t.Parallel()

	main := repoWithCommit(t)
	linked := filepath.Join(t.TempDir(), "item")
	runGit(t, main, "worktree", "add", "-q", "--detach", linked, "HEAD")

	fake := &fakeEnforcer{}

	_, code := sandboxFor(t, fake, nil).run(t.Context(), "p", linked, []string{"true"}, nil, 0)
	require.Zero(t, code)

	shared := filepath.Join(evaluated(t, main), ".git")
	assert.Contains(t, fake.plan.Read, shared, "git can read the history")
	assert.NotContains(t, fake.plan.Write, shared, "and cannot write its hooks or its config")
	assert.Len(t, worktreesOf(t, main), 2, "the main folder and the linked one remain")
}

func evaluated(t *testing.T, path string) string {
	t.Helper()

	real, err := filepath.EvalSymlinks(path)
	require.NoError(t, err)

	return real
}

func TestGateSandbox_AGateAllowWritePathInTheRealHooksFoldersIsRefused(t *testing.T) {
	t.Parallel()

	// The git folder is kept apart under a name that is not .git, so only the shared git folder of the linked work tree can protect its hooks.
	store := filepath.Join(t.TempDir(), "store")
	main := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(main, "README"), []byte("hi\n"), 0o600))
	runGit(t, main, "init", "-q", "-b", "main", "--separate-git-dir", store)
	runGit(t, main, "add", "README")
	runGit(t, main, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "start")

	linked := filepath.Join(t.TempDir(), "item")
	runGit(t, main, "worktree", "add", "-q", "--detach", linked, "HEAD")

	hooksTarget := t.TempDir()
	runGit(t, main, "config", "core.hooksPath", hooksTarget)

	for name, path := range map[string]string{
		"the hooks of the shared git folder": filepath.Join(store, "hooks"),
		"the folder core.hooksPath names":    hooksTarget,
	} {
		box := sandboxFor(t, &fakeEnforcer{}, nil)
		found := box.projects["p"]
		found.GateAllowWrite = []string{path}
		box.projects["p"] = found

		out, code := box.run(t.Context(), "p", linked, []string{"true"}, nil, 0)
		assert.Equal(t, sandboxFailed, code, name)
		assert.Contains(t, out, "overlaps the protected path", name)
	}
}

func TestGateSandbox_AWorkTreeWithNoCommitRunsNoCommand(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")

	out, code := sandboxFor(t, &fakeEnforcer{}, nil).run(t.Context(), "p", dir, []string{"true"}, nil, 0)

	assert.Equal(t, sandboxFailed, code)
	assert.Contains(t, out, "make the worktree for the gate")
}

func TestGateSandbox_AGitThatCannotNameTheRecordOfTheWorktreeRunsNoCommand(t *testing.T) {
	bin := t.TempDir()
	calls := filepath.Join(bin, "calls")
	// A git that logs the worktree calls, makes the worktree and then fails on every other call.
	script := "#!/bin/sh\ncase \"$*\" in *worktree*) echo \"$@\" >> " + calls + "; exit 0;; esac\necho boom\nexit 1\n"
	require.NoError(t, os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o700)) //nolint:gosec // a test script
	t.Setenv("PATH", bin+string(os.PathListSeparator)+config.ProcessEnv()["PATH"])

	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, ".git"), 0o700))

	out, code := sandboxFor(t, &fakeEnforcer{}, nil).run(t.Context(), "p", dir, []string{"true"}, nil, 0)

	assert.Equal(t, sandboxFailed, code)
	assert.Contains(t, out, "find the git record of the worktree for the gate")

	log, err := os.ReadFile(calls) //nolint:gosec // a test file
	require.NoError(t, err)
	assert.True(t, strings.HasSuffix(string(log), "worktree list --porcelain\n"), "the last call looks for the record that the copy had: %q", log)
	assert.NotContains(t, string(log), "prune", "no call drops the records of the owner's worktrees")
}
