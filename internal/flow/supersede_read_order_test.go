package flow

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/review"
)

// readThenHoldForge reads the pull request, then holds the read that the test names until the test lets it go, and returns what it read.
// The holder of that read acts on a head that has moved since.
type readThenHoldForge struct {
	*fakeForge
	reads atomic.Int32
	at    int32
	hold  func()
}

func (f *readThenHoldForge) Pull(ctx context.Context, project, item string) (review.Standing, error) {
	standing, err := f.fakeForge.Pull(ctx, project, item)
	if f.reads.Add(1) == f.at {
		f.hold()
	}

	return standing, err //nolint:wrapcheck // a test double
}

func TestSuperseded_AStopThatReadTheHeadBeforeItMovedNeverStopsTheGateOfTheNewHead(t *testing.T) {
	t.Parallel()

	gated := newSupersedeGate("")

	var holding *readThenHoldForge

	h, engine := supersedeEngineWith(t, gated, "A", func(fake *fakeForge) forge {
		holding = &readThenHoldForge{fakeForge: fake}

		return holding
	})

	// Read 1 is the tip of gate A, read 2 is the read of the head in the stop of gate A. Read 2 sees head A; before the stop lists the gates,
	// the head moves to B and the gate of head B queues and records its own stop.
	var current <-chan gateEnd

	holding.at = 2
	holding.hold = func() {
		setHead(h, "B")

		var currentRun string

		currentRun, current = queueGateRun(engine, "B")

		waitOn(t, condition(stopRecorded(engine, currentRun)))
	}

	firstRun, first := queueGateRun(engine, "A")

	waitOn(t, condition(stopRecorded(engine, firstRun)))
	require.ErrorIs(t, endsAfterTicks(t, h, first).err, errSuperseded)

	close(gated.release)

	require.NoError(t, receiveEnd(t, engine, gated, current).err, "a stop that read the old head canceled the gate of the new head")
}
