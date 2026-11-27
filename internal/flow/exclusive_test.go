package flow

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/model"
)

// slowGate holds each gate until the test lets it end, and tells the test when it started.
type slowGate struct {
	cleanTree

	started chan struct{}
	release chan struct{}
}

func (slowGate) Submit(context.Context, string, string, string, string, string, string, model.Slot) (GateJob, error) {
	return GateJob{}, nil
}

func (g slowGate) Run(ctx context.Context, _, _, _ string, _ GateJob) (GateResult, error) {
	g.started <- struct{}{}

	select {
	case <-g.release:
	case <-ctx.Done():
	}

	return GateResult{Passed: true, Head: gatedHead, Steps: commandStep(true)}, nil
}

func TestExclusive_ABatchMergeOfARepositoryStartsOnlyAfterTheGateOfThatRepositoryEnds(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	gated := slowGate{started: make(chan struct{}, 1), release: make(chan struct{})}
	deps := rig.deps()
	deps.Gate = gated

	engine, err := NewEngine(t.Context(), rig.h.opts, deps)
	require.NoError(t, err)
	require.NoError(t, engine.Launch())
	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	var group sync.WaitGroup

	ended := make(chan error, 2)

	group.Go(func() {
		_, err := engine.RunFile("gate-item", testProject, writeFlow(t, bodyFlow("gate(name = \"check\")\n")), modeOff())
		ended <- err
	})

	receive(t, gated.started)

	group.Go(func() {
		_, err := engine.RunFile("batch-item", testProject, writeFlow(t, bodyFlow(`merge(members = `+rig.members("1 3")+`, base = "main", repo = "o/r", name = "b")`)), modeOff())
		ended <- err
	})

	waitOn(t, condition(waitsInTheQueue(engine, 1)))

	// The queue has no recorded poll, so the check spans three polls of the queue. It is a listed wall-time site.
	require.Never(t, func() bool { return len(rig.merger.landed()) > 0 }, 3*storePoll, 100*time.Millisecond, "the batch merged while the gate of the repository ran")

	close(gated.release)
	group.Wait()
	require.NoError(t, receive(t, ended))
	require.NoError(t, receive(t, ended))
	assert.Equal(t, []string{"1", "3"}, rig.merger.landed(), "the batch merged after the gate ended")
}
