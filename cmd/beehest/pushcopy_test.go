package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/exectest"
	"github.com/tobocop2/beehest/internal/sandbox"
)

// foreignStore is a repository outside the clone, with a blob that only it holds, in one pack.
type foreignStore struct {
	gitDir string
	pack   string
	blob   string
	commit string
}

func newForeignStore(t *testing.T) foreignStore {
	t.Helper()

	repo := repoWithCommit(t)
	require.NoError(t, os.WriteFile(filepath.Join(repo, "secret"), []byte("secret of "+t.Name()+"\n"), 0o600))
	runGit(t, repo, "add", "secret")
	commit := commitIn(t, repo, "secret")
	runGit(t, repo, "repack", "-adq")

	packs, err := filepath.Glob(filepath.Join(repo, ".git", "objects", "pack", "*.pack"))
	require.NoError(t, err)
	require.Len(t, packs, 1)

	return foreignStore{gitDir: filepath.Join(repo, ".git"), pack: strings.TrimSuffix(packs[0], ".pack"), blob: gitOut(t, repo, "rev-parse", "HEAD:secret"), commit: commit}
}

// gitWithInput runs git in dir with input on its standard input and returns what it printed.
func gitWithInput(t *testing.T, dir, input string, args ...string) string {
	t.Helper()

	command := exec.CommandContext(t.Context(), "git", args...) //nolint:gosec // a test runs git on its own temporary folder
	command.Dir, command.Stdin = dir, strings.NewReader(input)

	out, err := command.CombinedOutput()
	require.NoError(t, err, string(out))

	return strings.TrimSpace(string(out))
}

// commitNaming makes a commit in the worktree whose tree names the blob, which the clone does not hold, and returns the commit.
func commitNaming(t *testing.T, tree, blob string) string {
	t.Helper()

	id := gitWithInput(t, tree, "100644 blob "+blob+"\tsecret\n", "mktree", "--missing")

	return gitOut(t, tree, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit-tree", "-m", "names the secret", "-p", "HEAD", id)
}

// attack is one way to make a blob of a foreign store reachable from the git folder of the clone. It returns the head that the item pushes.
type attack func(t *testing.T, rig pushRig, store foreignStore, git string) string

var attacks = map[string]attack{
	"a list of other object stores": func(t *testing.T, rig pushRig, store foreignStore, git string) string {
		t.Helper()

		require.NoError(t, os.WriteFile(filepath.Join(git, "objects", "info", "alternates"), []byte(filepath.Join(store.gitDir, "objects")+"\n"), 0o600))

		return commitNaming(t, rig.tree, store.blob)
	},
	"an object folder swapped for one with a list": func(t *testing.T, rig pushRig, store foreignStore, git string) string {
		t.Helper()

		head := commitNaming(t, rig.tree, store.blob)
		stage := filepath.Join(git, "stage")
		require.NoError(t, os.MkdirAll(filepath.Join(stage, "info"), 0o700))
		list := filepath.Join(git, "objects.old") + "\n" + filepath.Join(store.gitDir, "objects") + "\n"
		require.NoError(t, os.WriteFile(filepath.Join(stage, "info", "alternates"), []byte(list), 0o600))
		require.NoError(t, os.Rename(filepath.Join(git, "objects"), filepath.Join(git, "objects.old")))
		require.NoError(t, os.Rename(stage, filepath.Join(git, "objects")))

		return head
	},
	"a link to a foreign pack": func(t *testing.T, rig pushRig, store foreignStore, git string) string {
		t.Helper()

		for _, ext := range []string{".idx", ".pack"} {
			require.NoError(t, os.Symlink(store.pack+ext, filepath.Join(git, "objects", "pack", filepath.Base(store.pack)+ext)))
		}

		return commitNaming(t, rig.tree, store.blob)
	},
	"the git folder replaced by a link to a foreign one": func(t *testing.T, _ pushRig, store foreignStore, git string) string {
		t.Helper()

		require.NoError(t, os.Rename(git, filepath.Join(t.TempDir(), "moved")))
		require.NoError(t, os.Symlink(store.gitDir, git))

		return store.commit
	},
}

// Each attack makes a foreign blob reachable from the git folder of the clone for a reader with the rights of the engine. The push copies the head under
// the enforcer of the machine, which reads the clone folder only, so the push fails and the remote never gets the blob.
func TestPulls_APushCarriesNoObjectThatTheCloneReachesOnlyThroughALink(t *testing.T) {
	t.Parallel()

	for name, plant := range attacks {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig := newPushRig(t)
			store := newForeignStore(t)
			require.NoError(t, rig.pulls.Push(t.Context(), "p", pushItem, "clean", rig.head, ""), "control: a commit of the clone pushes")

			git := filepath.Join(rig.opts.StateDir, "repos", "p", ".git")
			head := plant(t, rig, store, git)
			runGit(t, rig.tree, "--git-dir="+git, "cat-file", "-e", store.blob) // control: a reader with the rights of the engine reaches the blob

			require.ErrorContains(t, rig.pulls.Push(t.Context(), "p", pushItem, pushItem, head, ""), "copy "+head+" from the clone")
			assert.Empty(t, gitOut(t, rig.remote, "branch", "--list", pushItem), "the remote has no branch of the item")
			assert.Error(t, exec.CommandContext(t.Context(), "git", "-C", rig.remote, "cat-file", "-e", store.blob).Run(), "the remote never got the foreign blob") //nolint:gosec // a test runs git on its own temporary folder
		})
	}
}

// systemStore returns a pack and a blob in it that lie in a system folder, which a sandboxed step may read but a project can deny to the agent: a
// repository that the test makes under /dev/shm on Linux, or the Homebrew repository under /opt or /usr/local on macOS, which the test only reads.
func systemStore(t *testing.T) foreignStore {
	t.Helper()

	if info, err := os.Stat("/dev/shm"); err == nil && info.IsDir() {
		dir, err := os.MkdirTemp("/dev/shm", "beehest-store-")
		require.NoError(t, err)
		t.Cleanup(func() { assert.NoError(t, os.RemoveAll(dir)) })
		runGit(t, dir, "init", "-q")
		require.NoError(t, os.WriteFile(filepath.Join(dir, "secret"), []byte("secret of "+t.Name()+"\n"), 0o600))
		runGit(t, dir, "add", "secret")
		runGit(t, dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-qm", "secret")
		runGit(t, dir, "repack", "-adq")

		packs, err := filepath.Glob(filepath.Join(dir, ".git", "objects", "pack", "*.pack"))
		require.NoError(t, err)
		require.Len(t, packs, 1)

		return foreignStore{gitDir: filepath.Join(dir, ".git"), pack: strings.TrimSuffix(packs[0], ".pack"), blob: gitOut(t, dir, "rev-parse", "HEAD:secret")}
	}

	for _, gitDir := range []string{"/opt/homebrew/.git", "/usr/local/Homebrew/.git"} {
		indexes, err := filepath.Glob(filepath.Join(gitDir, "objects", "pack", "*.idx"))
		if err != nil || len(indexes) == 0 {
			continue
		}

		return blobOfPack(t, gitDir, strings.TrimSuffix(indexes[0], ".idx"))
	}

	t.Skip("this machine has no git repository in a system folder: Linux gives /dev/shm, macOS a Homebrew repository")

	return foreignStore{}
}

// blobOfPack returns the store of the pack in gitDir with the first blob that the pack holds.
func blobOfPack(t *testing.T, gitDir, pack string) foreignStore {
	t.Helper()

	index, err := os.ReadFile(pack + ".idx") //nolint:gosec // a test reads the index of a pack that it chose
	require.NoError(t, err)

	var ids strings.Builder

	for line := range strings.SplitSeq(gitWithInput(t, "/", string(index), "show-index"), "\n") {
		if fields := strings.Fields(line); len(fields) > 1 {
			ids.WriteString(fields[1] + "\n")
		}
	}

	for line := range strings.SplitSeq(gitWithInput(t, "/", ids.String(), "--git-dir="+gitDir, "cat-file", "--batch-check=%(objectname) %(objecttype)"), "\n") {
		if id, kind, _ := strings.Cut(line, " "); kind == "blob" {
			return foreignStore{gitDir: gitDir, pack: pack, blob: id}
		}
	}

	require.FailNow(t, "the pack holds no blob", pack)

	return foreignStore{}
}

// A pack in a system folder can hold what a project denies to the agent, such as a repository under /opt. A link to it in the clone names its blob,
// and the copy reads no system folder whole, so the push fails and the remote never gets the blob.
func TestPulls_APushCarriesNoObjectOfAPackInASystemFolder(t *testing.T) {
	t.Parallel()

	store := systemStore(t)
	rig := newPushRig(t)
	git := filepath.Join(rig.opts.StateDir, "repos", "p", ".git")

	for _, ext := range []string{".idx", ".pack"} {
		require.NoError(t, os.Symlink(store.pack+ext, filepath.Join(git, "objects", "pack", filepath.Base(store.pack)+ext)))
	}

	head := commitNaming(t, rig.tree, store.blob)
	runGit(t, rig.tree, "--git-dir="+git, "cat-file", "-e", store.blob) // control: a reader with the rights of the engine reaches the blob

	require.ErrorContains(t, rig.pulls.Push(t.Context(), "p", pushItem, pushItem, head, ""), "copy "+head+" from the clone")
	assert.Error(t, exec.CommandContext(t.Context(), "git", "-C", rig.remote, "cat-file", "-e", store.blob).Run(), "the remote never got the blob") //nolint:gosec // a test runs git on its own temporary folder
}

// recorded is an enforcer that records the plan and the command it gets, and runs nothing.
type recorded struct {
	name    string
	plan    sandbox.Plan
	command sandbox.Command
	runErr  error
}

func (r *recorded) Name() string { return r.name }

func (*recorded) Check() error { return nil }

func (r *recorded) Run(_ context.Context, plan sandbox.Plan, _ int, command sandbox.Command) (int, error) {
	r.plan, r.command = plan, command

	return 3, r.runErr
}

// shellWords returns the words that a POSIX shell makes of line.
func shellWords(t *testing.T, line string) []string {
	t.Helper()

	out, err := exec.CommandContext(t.Context(), "sh", "-c", `for word in `+line+`; do printf '%s\n' "$word"; done`).Output() //nolint:gosec // a test reads its own line
	require.NoError(t, err)

	return strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
}

// The engine writes the upload-pack program, git appends the clone folder, and the helper runs upload-pack on it under the enforcer of the project with
// read on the clone folder and on what git reads to start only, no system folder whole, no write and no network. Its stdin and stdout are the ones of the
// helper.
func TestPulls_TheCopyOfTheHeadReadsOnlyTheCloneFolderWithNoWriteAndNoNetwork(t *testing.T) {
	t.Parallel()

	rig := newPushRig(t)
	fake := &recorded{name: "fake"}
	rig.pulls.copier.enforcer = func(string) (sandbox.Enforcer, error) { return fake, nil }

	folder, program, err := rig.pulls.copySource("p")
	require.NoError(t, err)
	assert.Equal(t, evaluated(t, filepath.Join(rig.opts.StateDir, "repos", "p")), folder, "the read folder is the clone folder, not its git folder")

	args := shellWords(t, program+" "+folder)
	self, err := os.Executable()
	require.NoError(t, err)
	require.Equal(t, []string{self, uploadPackArg, "fake", folder, folder}, args)

	chosen, searched := "", ""
	stdin, stdout := strings.NewReader("want"), &bytes.Buffer{}
	tools := copyTools{
		choose: func(name string) (sandbox.Enforcer, error) { chosen = name; return fake, nil },
		git: func(_ context.Context, paths string) (gitProgram, error) {
			searched = paths
			return gitProgram{binary: "/g/bin/git", reads: []string{"/g/bin/git", "/g/core", "/g/lib/libz.so"}}, nil
		},
	}

	code, helper := runUploadPack(t.Context(), args, map[string]string{"PATH": "/bin"}, tools, helperIO{stdin: stdin, stdout: stdout, stderr: &bytes.Buffer{}})
	assert.True(t, helper)
	assert.Equal(t, 3, code, "the exit code of upload-pack passes through")
	assert.Equal(t, "fake", chosen)
	assert.Equal(t, "/bin", searched, "git is the one that the search path of the engine finds")
	want := sandbox.Plan{Read: []string{"/g/bin/git", "/g/core", "/g/lib/libz.so", folder}, Write: []string{os.DevNull}}
	assert.Equal(t, want, fake.plan, "read the clone folder and what git reads to start, write the null device only, no network")

	for _, system := range sandbox.SystemRead() {
		assert.NotContains(t, fake.plan.Read, system, "no system folder is read whole")
	}

	assert.Equal(t, []string{"/g/bin/git", "-c", "uploadpack.allowAnySHA1InWant=true", "upload-pack", folder}, fake.command.Argv)
	assert.Equal(t, []string{"PATH=/bin", noSystemConfig}, fake.command.Env, "the environment of upload-pack is the search path and no system config")
	assert.Same(t, stdin, fake.command.Stdin)
	assert.Same(t, stdout, fake.command.Stdout)
}

func TestUploadPackProgram_KeepsEachWordWholeForTheShell(t *testing.T) {
	t.Parallel()

	program := uploadPackProgram("/a b/beehest", "seat'belt", "/c $d/e")
	assert.Equal(t, []string{"/a b/beehest", uploadPackArg, "seat'belt", "/c $d/e", "/x"}, shellWords(t, program+" /x"))
}

func TestRunUploadPack_LeavesAnotherCallAlone(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{{"beehest", "version"}, {"beehest", "--other", "a", "b", "c"}, {"beehest", uploadPackArg, "a", "b"}} {
		code, helper := runUploadPack(t.Context(), args, nil, copyTools{}, helperIO{})
		assert.False(t, helper, "%q", args)
		assert.Zero(t, code)
	}
}

func TestRunUploadPack_AnEnforcerOrAGitThatCannotRunOrStartStopsTheCopyAndSaysWhy(t *testing.T) {
	t.Parallel()

	found := func(context.Context, string) (gitProgram, error) { return gitProgram{binary: "/g/git"}, nil }
	cases := map[string]copyTools{
		"no enforcer": {choose: func(string) (sandbox.Enforcer, error) {
			return nil, fmt.Errorf("%w: none here", sandbox.ErrUnavailable)
		}, git: found},
		"a start that fails": {choose: func(string) (sandbox.Enforcer, error) { return &recorded{runErr: errors.New("none here")}, nil }, git: found},
		"a git whose read set cannot be found": {choose: func(string) (sandbox.Enforcer, error) { return &recorded{}, nil }, git: func(context.Context, string) (gitProgram, error) {
			return gitProgram{}, errors.New("none here")
		}},
	}

	for name, tools := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var stderr bytes.Buffer

			code, helper := runUploadPack(t.Context(), []string{"beehest", uploadPackArg, "x", "/c", "/c"}, nil, tools, helperIO{stderr: &stderr})
			assert.True(t, helper)
			assert.Equal(t, uploadPackFailed, code)
			assert.Contains(t, stderr.String(), "none here")
			assert.Contains(t, stderr.String(), "did not start")
		})
	}
}

// countObjects returns the number of objects in packs and the number of loose objects of the bare repository dir.
func countObjects(t *testing.T, dir string) (packed, loose int) {
	t.Helper()

	for line := range strings.SplitSeq(gitOut(t, dir, "count-objects", "-v"), "\n") {
		name, value, _ := strings.Cut(line, ": ")
		n, err := strconv.Atoi(value)
		require.NoError(t, err, line)

		switch name {
		case "in-pack":
			packed = n
		case "count":
			loose = n
		}
	}

	return packed, loose
}

// The engine keeps one push repository for the project, so the second push fetches only the objects that the first did not copy. The fetch keeps each
// received pack, so the new pack holds exactly the objects that the copy sent.
func TestPulls_ASecondPushCopiesOnlyTheNewObjects(t *testing.T) {
	t.Parallel()

	rig := newPushRig(t)
	rig.pulls.trees.pushCheck = noPushCheck
	require.NoError(t, rig.pulls.Push(t.Context(), "p", pushItem, pushItem, rig.head, ""))

	repo := filepath.Join(rig.opts.StateDir, "push", "p.git")
	runGit(t, repo, "config", "fetch.unpackLimit", "1")

	packed, loose := countObjects(t, repo)
	require.Greater(t, packed+loose, 3, "control: a full copy would send more objects than the new ones")

	require.NoError(t, os.WriteFile(filepath.Join(rig.tree, "new"), []byte("new\n"), 0o600))
	runGit(t, rig.tree, "add", "new")
	next := commitIn(t, rig.tree, "two")
	require.NoError(t, rig.pulls.Push(t.Context(), "p", pushItem, pushItem, next, rig.head))
	assert.Equal(t, next, rig.remoteHead(t))

	after, _ := countObjects(t, repo)
	assert.Equal(t, packed+3, after, "the copy sent the commit, its tree and the new blob")

	entries, err := os.ReadDir(filepath.Join(rig.opts.StateDir, "push"))
	require.NoError(t, err)
	require.Len(t, entries, 1, "one push repository for the project")
	assert.Equal(t, "p.git", entries[0].Name())
}

func TestPulls_APushWithNoEnforcerIsRefusedAndNamesWhy(t *testing.T) {
	t.Parallel()

	rig := newPushRig(t)
	rig.pulls.copier.enforcer = func(string) (sandbox.Enforcer, error) {
		return nil, fmt.Errorf("%w: this kernel does not run Landlock", sandbox.ErrUnavailable)
	}

	err := rig.pulls.Push(t.Context(), "p", pushItem, pushItem, rig.head, "")

	require.ErrorIs(t, err, sandbox.ErrUnavailable)
	require.ErrorContains(t, err, "this kernel does not run Landlock")
	require.ErrorContains(t, err, "sandbox_enforcer")
	assert.Empty(t, gitOut(t, rig.remote, "branch", "--list", pushItem), "the remote has no branch of the item")
	assert.Empty(t, gitOut(t, filepath.Join(rig.opts.StateDir, "push", "p.git"), "branch", "--list", pushItem), "nothing was copied")
}

func TestPulls_APushWhoseProgramPathIsUnknownIsRefused(t *testing.T) {
	t.Parallel()

	rig := newPushRig(t)
	rig.pulls.copier.executable = func() (string, error) { return "", errors.New("no path") }

	err := rig.pulls.Push(t.Context(), "p", pushItem, pushItem, rig.head, "")

	require.ErrorContains(t, err, "no path")
	assert.Empty(t, gitOut(t, rig.remote, "branch", "--list", pushItem), "the remote has no branch of the item")
}

func TestPulls_APushWhoseRepositoryCannotBeMadeIsRefused(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		plant func(t *testing.T, root string)
		want  string
	}{
		"the folder is a file": {func(t *testing.T, root string) {
			t.Helper()
			require.NoError(t, os.WriteFile(root, nil, 0o600))
		}, "make the push repository"},
		"another user can write the folder": {func(t *testing.T, root string) {
			t.Helper()
			require.NoError(t, os.MkdirAll(root, 0o700))
			require.NoError(t, exectest.ChmodFolder(root, 0o777))
		}, "can be written by another user"},
		"git cannot write the repository": {func(t *testing.T, root string) {
			t.Helper()
			require.NoError(t, os.MkdirAll(filepath.Join(root, "p.git"), 0o700))
			require.NoError(t, exectest.ChmodFolder(filepath.Join(root, "p.git"), 0o500))
		}, "git init"},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig := newPushRig(t)
			root := filepath.Join(rig.opts.StateDir, "push")
			// The fetch that made the worktree of the rig made the folder; each case plants its own.
			require.NoError(t, os.RemoveAll(root))
			c.plant(t, root)

			err := rig.pulls.Push(t.Context(), "p", pushItem, pushItem, rig.head, "")

			require.ErrorContains(t, err, c.want)
			assert.Empty(t, gitOut(t, rig.remote, "branch", "--list", pushItem), "the remote has no branch of the item")
		})
	}
}
