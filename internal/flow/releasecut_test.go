package flow

import (
	"context"
	"slices"
	"sync"
	"testing"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// freedBeats answers a renewal of a claim that the lifecycle already released as the claim store does: the holder holds no claim.
type freedBeats struct {
	*fakeBeats
	life *fakeLifecycle
}

func (b freedBeats) Beat(ctx context.Context, project, owner string) (string, error) {
	b.life.mu.Lock()
	freed := slices.Contains(b.life.released, owner)
	b.life.mu.Unlock()

	if freed {
		return "heartbeat for " + owner + " failed: the holder holds no claim", nil
	}

	return b.fakeBeats.Beat(ctx, project, owner)
}

func TestRelease_ARunWhoseReleaseWasCutByAnEngineStopFinishesOnTheNextEngine(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	deps := h.deps()
	deps.Beats = freedBeats{fakeBeats: h.beats, life: h.life}

	first, err := NewEngine(t.Context(), h.opts, deps)
	require.NoError(t, err)
	require.NoError(t, first.Launch())
	t.Cleanup(func() { require.NoError(t, first.Shutdown()) })

	// The engine stops inside the release: the claim is freed, and the library records no result for the cut step.
	h.life.onRelease = sync.OnceFunc(func() {
		go func() { assert.NoError(t, first.Shutdown()) }()

		<-first.ctx.Done()
	})

	done := runInBackground(first, testItem)

	require.Error(t, receive(t, done).err, "the stopped engine ends the call and leaves the run open")
	require.NoError(t, first.Shutdown())

	second := engineOn(t, h, deps)
	handle, err := dbos.RetrieveWorkflow[ItemResult](second.ctx, testRun)
	require.NoError(t, err, "the launch of the second engine recovers the open run")

	_, err = resultOfHandle(t, handle)

	require.NoError(t, err, "the run does not renew a claim that its own release freed")
	assert.Len(t, h.life.released, 2, "the second engine releases again: the first release has no record")
}
