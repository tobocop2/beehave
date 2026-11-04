package host_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/host"
	"github.com/tobocop2/beehave/internal/proc"
)

// orderHost is a host whose Down runs a hook, so a test sees what is true of the remotes at the moment the host stops.
type orderHost struct {
	stubHost

	onDown func()
}

func (o *orderHost) Down(ctx context.Context) error {
	if o.onDown != nil {
		o.onDown()
	}

	return o.stubHost.Down(ctx)
}

// workRoot makes a work root on the "host" with one repository whose main branch is pushed to origin. It returns the root and the repository.
func workRoot(t *testing.T) (root, repo string) {
	t.Helper()

	root = t.TempDir()
	repo = filepath.Join(root, "lane")
	require.NoError(t, os.Mkdir(repo, 0o750))
	git(t, repo, "init")
	commit(t, repo, "a")
	bare(t, repo, originURL)
	git(t, repo, "remote", "add", "origin", originURL)
	git(t, repo, "push", "origin", "main")

	return root, repo
}

func TestTeardown_WithRealGitBanksAnUnpushedBranchBeforeTheHostStops(t *testing.T) { //nolint:paralleltest // sets the git environment
	isolate(t)

	root, repo := workRoot(t)
	git(t, repo, "switch", "-c", "item-7")
	commit(t, repo, "work")
	tip := git(t, repo, "rev-parse", "item-7")
	private := bare(t, repo, privateURL)

	var bankedAtDown []string

	h := &orderHost{stubHost: stubHost{root: root}}
	h.onDown = func() { bankedAtDown = refsOf(t, private) }

	err := host.Teardown(t.Context(), h, proc.NewExecRunner("git", ""), proc.NewExecRunner("find", ""), privateURL)

	require.NoError(t, err)
	assert.Equal(t, 1, h.downs, "the host stops once")
	assert.Equal(t, []string{bankedRef(tip)}, bankedAtDown, "the unpushed branch was on the private remote before Down ran")
	assert.Empty(t, missing(t, repo, privateURL), "nothing is left that only the host holds")
}

func TestTeardown_WithRealGitKeepsTheHostUpWhenUnpushedWorkHasNoPrivateRemote(t *testing.T) { //nolint:paralleltest // sets the git environment
	isolate(t)

	root, repo := workRoot(t)
	git(t, repo, "switch", "-c", "item-7")
	commit(t, repo, "work")

	h := &orderHost{stubHost: stubHost{root: root}}

	err := host.Teardown(t.Context(), h, proc.NewExecRunner("git", ""), proc.NewExecRunner("find", ""), "")

	require.ErrorIs(t, err, host.ErrUnbanked)
	require.ErrorContains(t, err, "host stays up")
	assert.Zero(t, h.downs, "the host is not deleted while a branch exists only on it")
	assert.NotEmpty(t, missing(t, repo), "the branch is still unbanked")
}

func TestTeardown_WithRealGitStopsAHostWhoseWorkIsAllOnARemote(t *testing.T) { //nolint:paralleltest // sets the git environment
	isolate(t)

	root, _ := workRoot(t)
	h := &orderHost{stubHost: stubHost{root: root}}

	err := host.Teardown(t.Context(), h, proc.NewExecRunner("git", ""), proc.NewExecRunner("find", ""), "")

	require.NoError(t, err, "banked work needs no private remote")
	assert.Equal(t, 1, h.downs)
}
