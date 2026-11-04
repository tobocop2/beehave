package merge_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/lockfile"
	"github.com/tobocop2/obee/internal/merge"
)

// slowBody is a body held on the forge. The pause between the read and the write is what the lock must cover.
type slowBody struct {
	mu       sync.Mutex
	body     string
	failRead bool
	failSet  bool
}

func (s *slowBody) PRBody(context.Context, int) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.failRead {
		return "", errDown
	}

	return s.body, nil
}

func (s *slowBody) SetBody(_ context.Context, _ int, body string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.failSet {
		return errDown
	}

	s.body = body

	return nil
}

func addRow(row string) func(string) string {
	return func(body string) string {
		time.Sleep(time.Millisecond)

		return body + row + "\n"
	}
}

func TestEditBody_TwentyWritersKeepEveryRow(t *testing.T) {
	t.Parallel()

	dir, forge := t.TempDir(), &slowBody{}

	var group sync.WaitGroup

	for i := range 20 {
		group.Go(func() {
			assert.NoError(t, merge.EditBody(context.Background(), dir, forge, 9, addRow("row "+strconv.Itoa(i))))
		})
	}

	group.Wait()

	assert.Equal(t, 20, strings.Count(forge.body, "\n"))
}

func TestEditBody_ALockHeldByAnotherHolderMakesTheCallWait(t *testing.T) {
	t.Parallel()

	dir, forge := t.TempDir(), &slowBody{}
	held := lockfile.Open(filepath.Join(dir, "integration-9.lock"))
	require.NoError(t, held.Lock())

	done := make(chan error, 1)

	go func() { done <- merge.EditBody(context.Background(), dir, forge, 9, addRow("x")) }()

	select {
	case err := <-done:
		require.Failf(t, "the edit ran while another holder had the lock", "result: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	require.NoError(t, held.Unlock())
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		require.FailNow(t, "timed out after 2 s: the edit after the release")
	}

	assert.Equal(t, "x\n", forge.body)
}

func TestEditBody_ACanceledContextEndsTheWaitForTheLock(t *testing.T) {
	t.Parallel()

	dir, forge := t.TempDir(), &slowBody{body: "keep"}
	held := lockfile.Open(filepath.Join(dir, "integration-9.lock"))
	require.NoError(t, held.Lock())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() { done <- merge.EditBody(ctx, dir, forge, 9, addRow("x")) }()

	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(2 * time.Second):
		require.NoError(t, held.Unlock())
		require.FailNow(t, "timed out after 2 s: the wait for the lock ignored the canceled context")
	}

	require.NoError(t, held.Unlock())
	assert.Equal(t, "keep", forge.body)
}

func TestEditBody_AFailedWriteReleasesTheLock(t *testing.T) {
	t.Parallel()

	dir, forge := t.TempDir(), &slowBody{failSet: true}

	require.ErrorIs(t, merge.EditBody(context.Background(), dir, forge, 9, addRow("x")), errDown)

	again := lockfile.Open(filepath.Join(dir, "integration-9.lock"))
	free, err := again.TryLock()
	require.NoError(t, err)
	require.True(t, free)
	require.NoError(t, again.Unlock())
}

func TestEditBody_ALockPathThatIsAFolderIsRefused(t *testing.T) {
	t.Parallel()

	dir, forge := t.TempDir(), &slowBody{body: "keep"}
	require.NoError(t, os.Mkdir(filepath.Join(dir, "integration-8.lock"), 0o700))

	require.ErrorContains(t, merge.EditBody(context.Background(), dir, forge, 8, addRow("x")), fmt.Sprintf("lock the body of #%d", 8))
	assert.Equal(t, "keep", forge.body)
}

func TestEditBody_AReadOnlyLockFileIsRefused(t *testing.T) {
	t.Parallel()

	if os.Geteuid() == 0 {
		t.Skip("root can write a mode 0400 file")
	}

	dir, forge := t.TempDir(), &slowBody{body: "keep"}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "integration-7.lock"), nil, 0o400))

	require.ErrorContains(t, merge.EditBody(context.Background(), dir, forge, 7, addRow("x")), fmt.Sprintf("lock the body of #%d", 7))
	assert.Equal(t, "keep", forge.body)
}

func TestEditBody_AFailedReadWritesNothing(t *testing.T) {
	t.Parallel()

	dir, forge := t.TempDir(), &slowBody{failRead: true, body: "keep"}

	require.ErrorIs(t, merge.EditBody(context.Background(), dir, forge, 9, addRow("x")), errDown)
	assert.Equal(t, "keep", forge.body)
}

func TestEditBody_AFolderThatIsMissingIsAnError(t *testing.T) {
	t.Parallel()

	err := merge.EditBody(context.Background(), filepath.Join(t.TempDir(), "none"), &slowBody{}, 9, addRow("x"))

	require.Error(t, err)
	assert.NotErrorIs(t, err, errDown)
}
