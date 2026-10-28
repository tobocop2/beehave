package merge_test

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/filelock"
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
	held, err := filelock.Exclusive(filepath.Join(dir, "integration-9.lock"))
	require.NoError(t, err)

	done := make(chan error, 1)

	go func() { done <- merge.EditBody(context.Background(), dir, forge, 9, addRow("x")) }()

	select {
	case err := <-done:
		require.Failf(t, "the edit ran while another holder had the lock", "result: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	require.NoError(t, held.Release())
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		require.FailNow(t, "timed out after 2 s: the edit after the release")
	}

	assert.Equal(t, "x\n", forge.body)
}

func TestEditBody_AFailedWriteReleasesTheLock(t *testing.T) {
	t.Parallel()

	dir, forge := t.TempDir(), &slowBody{failSet: true}

	require.ErrorIs(t, merge.EditBody(context.Background(), dir, forge, 9, addRow("x")), errDown)

	again, err := filelock.TryExclusive(filepath.Join(dir, "integration-9.lock"))
	require.NoError(t, err)
	require.NoError(t, again.Release())
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
