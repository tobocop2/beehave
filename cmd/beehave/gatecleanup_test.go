package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/exectest"
	"github.com/tobocop2/beehave/internal/proc/gitexec"
)

// ownTemp points the temporary folder at a new folder and returns the folder that beehave owns for gate copies in it.
func ownTemp(t *testing.T) string {
	t.Helper()

	base := t.TempDir()
	t.Setenv("TMPDIR", base)

	return filepath.Join(base, "beehave-gate-"+strconv.Itoa(os.Getuid()))
}

// notRoot skips a test that needs the file mode to bind the owner.
func notRoot(t *testing.T) {
	t.Helper()

	if os.Getuid() == 0 {
		t.Skip("root ignores file modes")
	}
}

// readOnlyFolder makes a folder with a file in it, and then takes the write right away.
func readOnlyFolder(t *testing.T, path string) {
	t.Helper()

	require.NoError(t, os.MkdirAll(filepath.Join(path, "sub"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(path, "sub", "f"), []byte("x"), 0o600))
	require.NoError(t, exectest.ChmodFolder(filepath.Join(path, "sub"), 0o500))
	require.NoError(t, exectest.ChmodFolder(path, 0o500))
}

func entriesOf(t *testing.T, dir string) []string {
	t.Helper()

	list, err := os.ReadDir(dir)
	require.NoError(t, err)

	names := make([]string, 0, len(list))
	for _, entry := range list {
		names = append(names, entry.Name())
	}

	return names
}

func TestGateSandbox_AGateThatLeavesAReadOnlyFolderStillLosesItsCopy(t *testing.T) { //nolint:paralleltest // t.Setenv changes TMPDIR for the process
	notRoot(t)

	root := ownTemp(t)
	repo := repoWithCommit(t)
	fake := &fakeEnforcer{during: func(dir string) { readOnlyFolder(t, filepath.Join(dir, "cache")) }}

	out, code := sandboxFor(t, fake, nil).cloneOf(repo).run(t.Context(), "p", repo, []string{"true"}, nil, 0)

	assert.Zero(t, code)
	assert.NotContains(t, out, "stays at")
	assert.Empty(t, entriesOf(t, root), "the copy is gone")
	assert.Equal(t, []string{evaluated(t, repo)}, worktreesOf(t, repo), "the record is gone")
}

func TestGateSandbox_ACopyThatCannotBeRemovedIsNamedInTheOutput(t *testing.T) { //nolint:paralleltest // t.Setenv changes TMPDIR for the process
	notRoot(t)

	root := ownTemp(t)
	repo := repoWithCommit(t)
	t.Cleanup(func() { assert.NoError(t, exectest.ChmodFolder(root, 0o700)) })

	// The gate takes the write right of the folder that holds the copy, so the copy cannot go.
	fake := &fakeEnforcer{during: func(string) { require.NoError(t, exectest.ChmodFolder(root, 0o500)) }}

	out, code := sandboxFor(t, fake, nil).cloneOf(repo).run(t.Context(), "p", repo, []string{"true"}, nil, 0)

	assert.Zero(t, code)
	assert.Contains(t, out, "the copy of the gate stays at "+root)
}

func TestGateSandbox_TheNextGateReclaimsTheCopyOfAGateThatWasKilled(t *testing.T) { //nolint:paralleltest // t.Setenv changes TMPDIR for the process
	notRoot(t)

	root := ownTemp(t)
	repo := repoWithCommit(t)

	// The copy of a gate whose process is gone, with its record in the repository and a read-only folder in it.
	dead := filepath.Join(root, "999999999-dead")
	require.NoError(t, os.MkdirAll(root, 0o700))
	require.NoError(t, os.Mkdir(dead, 0o700))
	runGit(t, repo, "worktree", "add", "--detach", "--quiet", filepath.Join(dead, "work"), "HEAD")
	readOnlyFolder(t, filepath.Join(dead, "work", "cache"))
	require.Len(t, worktreesOf(t, repo), 2)

	// The copy of a gate that still runs, and a folder that beehave did not name.
	live := filepath.Join(root, strconv.Itoa(os.Getppid())+"-live") // the parent process runs, and is not this process
	require.NoError(t, os.Mkdir(live, 0o700))
	require.NoError(t, os.Mkdir(filepath.Join(root, "stray"), 0o700))

	_, code := sandboxFor(t, &fakeEnforcer{}, nil).cloneOf(repo).run(t.Context(), "p", repo, []string{"true"}, nil, 0)
	require.Zero(t, code)

	assert.Equal(t, []string{filepath.Base(live)}, entriesOf(t, root))
	assert.Equal(t, []string{evaluated(t, repo)}, worktreesOf(t, repo), "the record of the dead copy is pruned")
}

func TestGateSandbox_AFolderOfGatesThatIsALinkIsRefused(t *testing.T) { //nolint:paralleltest // t.Setenv changes TMPDIR for the process
	root := ownTemp(t)
	require.NoError(t, os.Symlink(t.TempDir(), root))

	out, code := sandboxFor(t, &fakeEnforcer{}, nil).run(t.Context(), "p", t.TempDir(), []string{"true"}, nil, 0)

	assert.Equal(t, sandboxFailed, code)
	assert.Contains(t, out, "make the temporary folder")
}

func TestGateSandbox_AGateKeepsTheRecordOfAnOwnerWorktreeWhoseFolderIsAway(t *testing.T) { //nolint:paralleltest // t.Setenv changes TMPDIR for the process
	ownTemp(t)
	repo := repoWithCommit(t)

	// The owner's worktree on a disk that is not mounted: its record stays and its folder is gone.
	away := filepath.Join(t.TempDir(), "away")
	runGit(t, repo, "worktree", "add", "--detach", "--quiet", away, "HEAD")
	require.NoError(t, os.RemoveAll(away))

	_, code := sandboxFor(t, &fakeEnforcer{}, nil).cloneOf(repo).run(t.Context(), "p", repo, []string{"true"}, nil, 0)
	require.Zero(t, code)

	assert.ElementsMatch(t, []string{evaluated(t, repo), evaluated(t, filepath.Dir(away)) + "/away"}, worktreesOf(t, repo), "only the record of the gate copy goes")
}

func TestGateSandbox_AFolderOfGatesThatAnotherUserCanWriteRunsNoCommand(t *testing.T) { //nolint:paralleltest // t.Setenv changes TMPDIR for the process
	notRoot(t)

	root := ownTemp(t)
	require.NoError(t, os.MkdirAll(root, 0o700))

	for _, mode := range []os.FileMode{0o770, 0o707, 0o777} {
		require.NoError(t, exectest.ChmodFolder(root, mode))

		enforcer := &fakeEnforcer{}
		out, code := sandboxFor(t, enforcer, nil).run(t.Context(), "p", t.TempDir(), []string{"true"}, nil, 0)

		assert.Equal(t, sandboxFailed, code, mode.String())
		assert.Contains(t, out, "can be written by another user", mode.String())
		assert.Nil(t, enforcer.argv, "no command ran")
		assert.Empty(t, entriesOf(t, root), "no gate folder was made")
	}
}

func TestGateSandbox_AFolderOfGatesThatCannotBeListedOrWrittenRunsNoCommand(t *testing.T) { //nolint:paralleltest // t.Setenv changes TMPDIR for the process
	notRoot(t)

	for name, mode := range map[string]os.FileMode{"list": 0o300, "write": 0o500} {
		root := ownTemp(t)
		require.NoError(t, os.MkdirAll(root, 0o700))
		require.NoError(t, exectest.ChmodFolder(root, mode))
		t.Cleanup(func() { assert.NoError(t, exectest.ChmodFolder(root, 0o700)) })

		out, code := sandboxFor(t, &fakeEnforcer{}, nil).run(t.Context(), "p", t.TempDir(), []string{"true"}, nil, 0)

		assert.Equal(t, sandboxFailed, code, name)
		assert.Contains(t, out, "make the temporary folder", name)
	}
}

func TestForgetCopies_AListThatFailsRemovesNoRecord(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	root := t.TempDir()
	calls := filepath.Join(t.TempDir(), "calls")
	// The failed listing still prints the path of a gate copy that is gone. A listing that failed is not trusted, so no record goes.
	putGitOnPath(t, "#!/bin/sh\necho \"$*\" >> '"+calls+"'\necho 'worktree "+filepath.Join(root, "gone")+"'\necho 'fatal: not a git repository' >&2\nexit 128\n")

	forgetCopies(t.Context(), gitexec.New(t.TempDir(), 0), root)

	text, err := os.ReadFile(calls) //nolint:gosec // a file of the test
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(text)), "\n")
	require.Len(t, lines, 1, "git ran the listing and nothing else")
	assert.Contains(t, lines[0], "worktree list --porcelain")
}
