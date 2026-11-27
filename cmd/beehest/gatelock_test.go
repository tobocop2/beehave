package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/config"
	"github.com/tobocop2/beehest/internal/flow"
)

// lockSpan is what one hold of a lock saw: the worktree records of the repository when the hold started and when it ended.
type lockSpan struct{ before, after int }

// recordingLock is a lock of the clone that records, for each hold, the count of worktree records of the repository dir before and after fn.
// A worktree command that runs outside every hold changes the count between two spans, and no span shows it.
func recordingLock(t *testing.T, dir string, spans *[]lockSpan) underLock {
	t.Helper()

	return func(_ context.Context, fn func() error) error {
		before := worktreeRecords(t, dir)
		err := fn()
		*spans = append(*spans, lockSpan{before: before, after: worktreeRecords(t, dir)})

		return err
	}
}

func worktreeRecords(t *testing.T, dir string) int {
	t.Helper()

	entries, err := os.ReadDir(filepath.Join(dir, ".git", "worktrees"))
	if os.IsNotExist(err) {
		return 0
	}

	require.NoError(t, err)

	return len(entries)
}

func TestDisposableTree_AddsTheCopyOfTheGateWhileItHoldsTheLockOfTheClone(t *testing.T) {
	t.Parallel()

	dir := repoWithCommit(t)

	var spans []lockSpan

	_, release, err := disposableTree(t.Context(), dir, t.TempDir(), gateCloneOf(dir, recordingLock(t, dir, &spans)))
	require.NoError(t, err)

	defer release()

	assert.Contains(t, spans, lockSpan{before: 0, after: 1}, "the git worktree add of the copy ran outside the lock of the clone")
}

func TestDisposableTree_RemovesTheRecordOfTheCopyWhileItHoldsTheLockOfTheClone(t *testing.T) {
	t.Parallel()

	dir := repoWithCommit(t)

	var spans []lockSpan

	_, release, err := disposableTree(t.Context(), dir, t.TempDir(), gateCloneOf(dir, recordingLock(t, dir, &spans)))
	require.NoError(t, err)

	spans = nil

	release()

	assert.Equal(t, []lockSpan{{before: 1, after: 0}}, spans, "the record of the copy went outside the lock of the clone")
}

func TestDiscard_ForgetsTheRecordOfTheCopyWhileItHoldsTheLockOfTheClone(t *testing.T) {
	t.Parallel()

	dir := repoWithCommit(t)
	tmp := t.TempDir()

	var spans []lockSpan

	_, _, err := disposableTree(t.Context(), dir, tmp, gateCloneOf(dir, recordingLock(t, dir, &spans)))
	require.NoError(t, err)

	spans = nil

	assert.Empty(t, discard(t.Context(), tmp, dir, gateCloneOf(dir, recordingLock(t, dir, &spans))))
	assert.Equal(t, []lockSpan{{before: 1, after: 0}}, spans, "the git worktree remove of the discard ran outside the lock of the clone")
}

func TestGateSandbox_ARunWaitsWhileTheLockOfTheCloneIsHeldAndThenTouchesTheClone(t *testing.T) { //nolint:paralleltest // ownTemp changes TMPDIR for the process
	ownTemp(t)

	repo := repoWithCommit(t)
	fake := &fakeEnforcer{}
	sb := sandboxFor(t, fake, nil).cloneOf(repo)

	held, release := make(chan struct{}), make(chan struct{})
	holder := make(chan error, 1)

	go func() {
		holder <- sb.clones.hold(context.WithoutCancel(t.Context()), "p", func(string) error {
			close(held)
			<-release

			return nil
		})
	}()

	<-held

	finished := make(chan int, 1)

	go func() {
		_, code := sb.run(t.Context(), "p", repo, []string{"true"}, nil, 0)
		finished <- code
	}()

	select {
	case <-finished:
		close(release)
		t.Fatal("the gate ran while the test held the lock of the clone of the project")
	case <-time.After(500 * time.Millisecond):
	}

	assert.Zero(t, worktreeRecords(t, repo), "the gate added its copy to the clone while the lock was held")

	close(release)
	require.NoError(t, <-holder)
	require.Zero(t, <-finished)
	assert.NotEmpty(t, fake.dir, "the command ran once the lock was free")
}

func TestDepsOn_TheGateOfAProjectTakesTheLockOfTheCloneThatItsWorktreesTake(t *testing.T) {
	t.Parallel()

	deps := depsOn(config.Config{}, flow.Options{StateDir: t.TempDir()}, logins{}, newAdapters(nil, nil, nil))

	trees, ok := deps.Trees.(worktrees)
	require.True(t, ok)

	commands, ok := deps.Commands.(programs)
	require.True(t, ok)

	held, done := make(chan struct{}), make(chan struct{})
	finished := make(chan error, 1)

	go func() {
		finished <- cloneLock{repos: trees.repos}.hold(context.WithoutCancel(t.Context()), "p", func(string) error {
			close(held)
			<-done

			return nil
		})
	}()

	<-held

	bounded, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()

	err := commands.sandbox.clones.of("p")(bounded, func() error { return nil })

	close(done)
	require.NoError(t, <-finished)
	require.ErrorIs(t, err, context.DeadlineExceeded, "the gate took a lock that the worktrees of the project never hold")
}
