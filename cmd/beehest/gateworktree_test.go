package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/config"
	"github.com/tobocop2/beehest/internal/exectest"
	"github.com/tobocop2/beehest/internal/proc/gitexec"
)

// gateCloneOf is the clone of a gate whose repository is dir, as a clone in the repos folder of the engine, with the lock hold.
func gateCloneOf(dir string, hold underLock) gateClone {
	return gateClone{hold: hold, git: gitOf(repositoryProject(), "p", gitexec.NewRepositories(filepath.Dir(dir)))}
}

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

func TestGateSandbox_AGitWorkTreeRunsInADisposableWorktreeAndTheRealFolderIsOutsideThePlan(t *testing.T) { //nolint:paralleltest // t.Setenv gives the gate its own temporary folder
	// The parallel tests of the package share one root of gate copies and make the folder name of a finished copy again, so only a root of its own tells
	// whether this copy is gone.
	root := ownTemp(t)
	dir := repoWithCommit(t)
	fake := &fakeEnforcer{}

	_, code := sandboxFor(t, fake, nil).cloneOf(dir).run(t.Context(), "p", dir, []string{"true"}, nil, 0)
	require.Zero(t, code)

	assert.NotEqual(t, dir, fake.dir, "the command works in a copy")
	assert.Equal(t, "work", filepath.Base(fake.dir))
	assert.Contains(t, fake.plan.Write, fake.dir, "the copy is writable whole")
	assert.Contains(t, fake.plan.Read, fake.dir)
	assert.NotContains(t, fake.plan.Write, dir)
	assert.NotContains(t, fake.plan.Read, dir, "the real folder is not in the plan")
	assert.Equal(t, []string{dir}, worktreesOf(t, dir), "the worktree of the gate is gone from the repository")
	assert.NoDirExists(t, fake.dir)
	assert.Empty(t, entriesOf(t, root), "the gate leaves no folder of its own")
}

func TestGateSandbox_ALinkedWorkTreeKeepsItsSharedGitFolderReadOnlyInThePlan(t *testing.T) {
	t.Parallel()

	main := repoWithCommit(t)
	linked := filepath.Join(t.TempDir(), "item")
	runGit(t, main, "worktree", "add", "-q", "--detach", linked, "HEAD")

	fake := &fakeEnforcer{}

	_, code := sandboxFor(t, fake, nil).cloneOf(main).run(t.Context(), "p", linked, []string{"true"}, nil, 0)
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

	// The gate runs in a linked worktree, so only the shared git folder of the clone can protect its hooks.
	main := repoWithCommit(t)
	linked := filepath.Join(t.TempDir(), "item")
	runGit(t, main, "worktree", "add", "-q", "--detach", linked, "HEAD")

	box := sandboxFor(t, &fakeEnforcer{}, nil).cloneOf(main)
	found := box.projects["p"]
	found.GateAllowWrite = []string{filepath.Join(main, ".git", "hooks")}
	box.projects["p"] = found

	out, code := box.run(t.Context(), "p", linked, []string{"true"}, nil, 0)
	assert.Equal(t, sandboxFailed, code)
	assert.Contains(t, out, "which reaches .git", "the hooks of the shared git folder")

	// A hooks folder that the clone config names is a setting outside the allow list, so the gate does not run at all.
	runGit(t, main, "config", "core.hooksPath", t.TempDir())

	out, code = box.run(t.Context(), "p", linked, []string{"true"}, nil, 0)
	assert.Equal(t, sandboxFailed, code)
	assert.Contains(t, out, "holds core.hookspath, which beehest does not allow", "the folder core.hooksPath names")
}

func TestGateSandbox_AWorkTreeWithNoCommitRunsNoCommand(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")

	out, code := sandboxFor(t, &fakeEnforcer{}, nil).cloneOf(dir).run(t.Context(), "p", dir, []string{"true"}, nil, 0)

	assert.Equal(t, sandboxFailed, code)
	assert.Contains(t, out, "make the worktree for the gate")
}

func TestGateSandbox_AGitThatCannotNameTheRecordOfTheWorktreeRunsNoCommand(t *testing.T) {
	bin := t.TempDir()
	calls := filepath.Join(bin, "calls")
	// A git that lists no clone setting, logs the worktree calls and says it made the worktree, and fails on every other call. No clone holds a record of
	// the copy, so the gate cannot name the record.
	script := "#!/bin/sh\ncase \"$*\" in config*) exit 0;; *worktree*) echo \"$@\" >> " + calls + "; exit 0;; esac\necho boom\nexit 1\n"
	require.NoError(t, exectest.Write(filepath.Join(bin, "git"), script))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+config.ProcessEnv()["PATH"])

	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, ".git"), 0o700))

	out, code := sandboxFor(t, &fakeEnforcer{}, nil).cloneOf(dir).run(t.Context(), "p", dir, []string{"true"}, nil, 0)

	assert.Equal(t, sandboxFailed, code)
	assert.Contains(t, out, "find the git record of the worktree for the gate")

	log, err := os.ReadFile(calls) //nolint:gosec // a test file
	require.NoError(t, err)
	assert.True(t, strings.HasSuffix(string(log), "worktree list --porcelain\n"), "the last call looks for the record that the copy had: %q", log)
	assert.NotContains(t, string(log), "prune", "no call drops the records of the owner's worktrees")
}
