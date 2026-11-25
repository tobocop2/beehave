package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/exectest"
	"github.com/tobocop2/beehave/internal/proc/gitexec/reach"
)

// blockedFetch is a gh run that stops at started and ends when release closes, with the result of its fields.
type blockedFetch struct {
	started chan string
	release chan struct{}
	runs    atomic.Int32
	err     error
}

func newBlockedFetch(err error) *blockedFetch {
	return &blockedFetch{started: make(chan string, 8), release: make(chan struct{}), err: err}
}

func (b *blockedFetch) fetch(ctx context.Context, host string) (string, error) {
	b.runs.Add(1)
	b.started <- host

	select {
	case <-b.release:
	case <-ctx.Done():
		return "", context.Cause(ctx)
	}

	if b.err != nil {
		return "", b.err
	}

	return "tok-" + host, nil
}

// watchedContext closes waiting when a call first asks for its Done channel, which a call does only when it waits for the run of another call.
type watchedContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func newWatchedContext(parent context.Context) *watchedContext {
	return &watchedContext{Context: parent, waiting: make(chan struct{})}
}

func (c *watchedContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })

	return c.Context.Done()
}

// readAsync reads the token of host in a goroutine and returns the channel of its result.
func readAsync(ctx context.Context, tokens *ownerTokens, host string) <-chan error {
	done := make(chan error, 1)

	go func() {
		_, err := tokens.read(ctx, host)
		done <- err
	}()

	return done
}

func TestOwnerTokens_AHeldTokenAnswersWhileAnotherHostRunsGh(t *testing.T) {
	t.Parallel()

	blocked := newBlockedFetch(nil)
	tokens := &ownerTokens{byHost: map[string]string{"a.test": "tok-a"}, fetch: blocked.fetch}

	other := readAsync(t.Context(), tokens, "b.test")
	require.Equal(t, "b.test", <-blocked.started, "the run of the other host must be in progress")

	token, err := tokens.read(t.Context(), "a.test")
	require.NoError(t, err)
	assert.Equal(t, "tok-a", token, "a held token waited for the gh run of another host")

	close(blocked.release)
	require.NoError(t, <-other)
}

func TestOwnerTokens_AFailedReadOfOneHostDoesNotBlockAHeldTokenOfAnotherHost(t *testing.T) {
	t.Parallel()

	blocked := newBlockedFetch(errors.New("no login"))
	tokens := &ownerTokens{byHost: map[string]string{"a.test": "tok-a"}, fetch: blocked.fetch}

	failing := readAsync(t.Context(), tokens, "b.test")
	require.Equal(t, "b.test", <-blocked.started)

	token, err := tokens.read(t.Context(), "a.test")
	require.NoError(t, err)
	assert.Equal(t, "tok-a", token, "a held token waited for a failing read of another host")

	close(blocked.release)
	require.ErrorContains(t, <-failing, "no login")
}

func TestOwnerTokens_CallsForOneFailingHostShareOneGhRunAndTheNextCallRunsAgain(t *testing.T) {
	t.Parallel()

	blocked := newBlockedFetch(errors.New("no login"))
	tokens := &ownerTokens{fetch: blocked.fetch}

	first := readAsync(t.Context(), tokens, "b.test")
	require.Equal(t, "b.test", <-blocked.started)

	watched := newWatchedContext(t.Context())
	second := readAsync(watched, tokens, "b.test")
	<-watched.waiting

	close(blocked.release)
	require.ErrorContains(t, <-first, "no login")
	require.ErrorContains(t, <-second, "no login")
	assert.EqualValues(t, 1, blocked.runs.Load(), "the two calls ran gh twice")

	_, err := tokens.read(t.Context(), "b.test")
	require.Error(t, err)
	assert.EqualValues(t, 2, blocked.runs.Load(), "a call after the failure did not read again")
}

func TestOwnerTokens_ACallForAHostThatIsReadGivesTheTokenToItsWaiters(t *testing.T) {
	t.Parallel()

	blocked := newBlockedFetch(nil)
	tokens := &ownerTokens{fetch: blocked.fetch}

	first := readAsync(t.Context(), tokens, "b.test")
	require.Equal(t, "b.test", <-blocked.started)

	watched := newWatchedContext(t.Context())
	second := make(chan string, 1)

	go func() {
		token, _ := tokens.read(watched, "b.test") //nolint:errcheck // the test reads the token and a failure gives an empty one
		second <- token
	}()

	<-watched.waiting
	close(blocked.release)
	require.NoError(t, <-first)
	assert.Equal(t, "tok-b.test", <-second)
	assert.EqualValues(t, 1, blocked.runs.Load())
}

func TestOwnerTokens_ACallThatIsCanceledWhileItWaitsForARunStopsWaiting(t *testing.T) {
	t.Parallel()

	blocked := newBlockedFetch(nil)
	tokens := &ownerTokens{fetch: blocked.fetch}

	first := readAsync(t.Context(), tokens, "b.test")
	require.Equal(t, "b.test", <-blocked.started)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := tokens.read(ctx, "b.test")
	require.ErrorIs(t, err, reach.ErrNoOwnerToken)
	require.ErrorIs(t, err, context.Canceled)

	close(blocked.release)
	require.NoError(t, <-first)
}

func TestOwnerTokens_AGhThatHangsIsStoppedAfterTheTimeoutAndTheErrorSaysItTimedOut(t *testing.T) {
	bin := t.TempDir()
	require.NoError(t, exectest.Write(filepath.Join(bin, "gh"), "#!/bin/sh\nexec sleep 60\n"))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+testPath)

	tokens := &ownerTokens{timeout: 200 * time.Millisecond}
	_, err := tokens.read(t.Context(), "one.test")

	require.ErrorIs(t, err, reach.ErrNoOwnerToken)
	require.ErrorContains(t, err, "gh auth token timed out after 200ms")
}
