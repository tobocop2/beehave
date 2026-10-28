package host_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/host"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/proc"
	"github.com/tobocop2/obee/internal/proc/proctest"
)

func TestFindRepos(t *testing.T) {
	t.Parallel()

	find := &proctest.Scripted{}
	find.Plan("/work/a/.git\x00/work/a/deep/er/b/.git\x00/work/bare.git/objects\x00\x00", "/work")

	repos, err := host.FindRepos(t.Context(), find, "/work")

	require.NoError(t, err)
	assert.Equal(t, []string{"/work/a", "/work/a/deep/er/b", "/work/bare.git"}, repos)
	assert.NotContains(t, find.Calls[0], "-maxdepth")
	assert.Equal(t, []string{"-H", "/work"}, find.Calls[0][:2], "the root is followed when it is a symlink")
	assert.Contains(t, find.Calls[0], "-print0")
}

func TestFindRepos_KeepsANameWithANewlineInOnePiece(t *testing.T) {
	t.Parallel()

	find := &proctest.Scripted{}
	find.Plan("/work/a\nb/.git\x00", "/work")

	repos, err := host.FindRepos(t.Context(), find, "/work")

	require.NoError(t, err)
	assert.Equal(t, []string{"/work/a\nb"}, repos)
}

func TestFindRepos_RefusesZeroRepositories(t *testing.T) {
	t.Parallel()

	find := &proctest.Scripted{}
	find.Plan("\x00", "/work")

	_, err := host.FindRepos(t.Context(), find, "/work")

	require.ErrorIs(t, err, host.ErrNoRepos)
}

func TestFindRepos_RefusesAScanThatFails(t *testing.T) {
	t.Parallel()

	find := &proctest.Scripted{}
	find.PlanResult(model.CommandResult{ExitCode: 1, Stdout: "/work/a/.git\x00", Stderr: "Permission denied"}, "/work")

	_, err := host.FindRepos(t.Context(), find, "/work")

	require.ErrorContains(t, err, "exit 1: Permission denied")
}

func TestFindRepos_WithTheRealFind(t *testing.T) { //nolint:paralleltest // sets the git environment
	isolate(t)

	root := t.TempDir()
	normal := filepath.Join(root, "normal")
	require.NoError(t, os.Mkdir(normal, 0o750))
	git(t, normal, "init")

	bareDir := filepath.Join(root, "deep", "mirror.git")
	require.NoError(t, os.MkdirAll(filepath.Dir(bareDir), 0o750))
	git(t, root, "init", "--bare", bareDir)

	notARepo := filepath.Join(root, "objects-only", "objects")
	require.NoError(t, os.MkdirAll(notARepo, 0o750))

	repos, err := host.FindRepos(t.Context(), proc.NewExecRunner("find", ""), root)

	require.NoError(t, err)
	assert.ElementsMatch(t, []string{normal, bareDir}, repos)
}

func TestFindRepos_DoesNotEnterTheObjectsOfABareRepository(t *testing.T) { //nolint:paralleltest // sets the git environment
	isolate(t)

	root := t.TempDir()
	bareDir := filepath.Join(root, "mirror.git")
	git(t, root, "init", "--bare", bareDir)

	// A folder inside objects that looks like a bare repository is object data, not a repository.
	inner := filepath.Join(bareDir, "objects")
	require.NoError(t, os.MkdirAll(filepath.Join(inner, "objects"), 0o750))
	require.NoError(t, os.MkdirAll(filepath.Join(inner, "refs"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(inner, "HEAD"), []byte("x"), 0o600))

	repos, err := host.FindRepos(t.Context(), proc.NewExecRunner("find", ""), root)

	require.NoError(t, err)
	assert.Equal(t, []string{bareDir}, repos)
}

func TestFindRepos_FollowsASymlinkedRootButNotAnInnerLink(t *testing.T) { //nolint:paralleltest // sets the git environment
	isolate(t)

	target := t.TempDir()
	repo := filepath.Join(target, "repo")
	require.NoError(t, os.Mkdir(repo, 0o750))
	git(t, repo, "init")

	link := filepath.Join(t.TempDir(), "root")
	require.NoError(t, os.Symlink(target, link))

	repos, err := host.FindRepos(t.Context(), proc.NewExecRunner("find", ""), link)

	require.NoError(t, err)
	assert.Equal(t, []string{filepath.Join(link, "repo")}, repos)

	inner := t.TempDir()
	require.NoError(t, os.Symlink(target, filepath.Join(inner, "linked")))

	_, err = host.FindRepos(t.Context(), proc.NewExecRunner("find", ""), inner)

	require.ErrorIs(t, err, host.ErrNoRepos, "an inner symlink is not followed")
}

func TestFindRepos_AnUnreadableDirectoryRefuses(t *testing.T) { //nolint:paralleltest // sets the git environment
	isolate(t)

	if os.Geteuid() == 0 {
		t.Skip("root reads every directory")
	}

	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	require.NoError(t, os.Mkdir(repo, 0o750))
	git(t, repo, "init")

	locked := filepath.Join(root, "locked")
	require.NoError(t, os.Mkdir(locked, 0o750))
	require.NoError(t, os.Chmod(locked, 0))

	t.Cleanup(func() { require.NoError(t, os.Chmod(locked, 0o600)) })
	_, err := host.FindRepos(t.Context(), proc.NewExecRunner("find", ""), root)

	require.ErrorContains(t, err, "Permission denied")
}
