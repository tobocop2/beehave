package flow

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/merge"
)

// slowGate holds each gate until the test lets it end, and tells the test when it started.
type slowGate struct {
	started chan struct{}
	release chan struct{}
}

func (slowGate) Submit(context.Context, string, string) (GateJob, error) { return GateJob{}, nil }

func (g slowGate) Run(ctx context.Context, _, _ string, _ GateJob) (GateResult, error) {
	g.started <- struct{}{}

	select {
	case <-g.release:
	case <-ctx.Done():
	}

	return GateResult{Passed: true, Head: gatedHead, Steps: commandStep(true)}, nil
}

func TestExclusive_AMergeOfARepositoryStartsOnlyAfterTheGateOfThatRepositoryEnds(t *testing.T) {
	t.Parallel()

	gated := slowGate{started: make(chan struct{}, 1), release: make(chan struct{})}
	h := mergeHarness(t, merge.Result{Stop: merge.Done})
	h.gate.via = gated
	merged := make(chan struct{}, 1)
	h.merge.over = func(context.Context, string, MergeSettings) (merge.Result, error) {
		merged <- struct{}{}

		return merge.Result{Stop: merge.Done}, nil
	}

	engine := h.engine(t)
	gateFile := writeFlow(t, bodyFlow("gate(name = \"check\")\n"))
	mergeFile := writeFlow(t, shipFlow("merge = True, "))

	var group sync.WaitGroup

	ended := make(chan error, 2)

	group.Go(func() {
		_, err := engine.RunFile("gate-item", testProject, gateFile)
		ended <- err
	})

	<-gated.started

	group.Go(func() {
		_, err := engine.RunFile("merge-item", testProject, mergeFile)
		ended <- err
	})

	select {
	case <-merged:
		t.Fatal("the merge started while the gate of the same repository was running")
	case <-time.After(2 * time.Second):
	}

	close(gated.release)

	select {
	case <-merged:
	case <-time.After(eventTimeout):
		t.Fatal("the merge never started after the gate ended")
	}

	group.Wait()
	require.NoError(t, <-ended)
	require.NoError(t, <-ended)
	require.Len(t, h.merge.asked, 1)
	assert.Contains(t, h.merge.asked[0], testProject)
}

func TestExclusive_AGateOfOneProjectDoesNotHoldTheMergeOfAnotherProject(t *testing.T) {
	t.Parallel()

	gated := slowGate{started: make(chan struct{}, 1), release: make(chan struct{})}
	h := mergeHarness(t, merge.Result{Stop: merge.Done})
	h.gate.via = gated
	merged := make(chan struct{}, 1)
	h.merge.over = func(context.Context, string, MergeSettings) (merge.Result, error) {
		merged <- struct{}{}

		return merge.Result{Stop: merge.Done}, nil
	}

	engine := h.engine(t)
	gateFile := writeFlow(t, bodyFlow("gate(name = \"check\")\n"))
	mergeFile := writeFlow(t, shipFlow("merge = True, "))
	ended := make(chan error, 2)

	go func() {
		_, err := engine.RunFile("gate-item", "project-a", gateFile)
		ended <- err
	}()

	<-gated.started

	go func() {
		_, err := engine.RunFile("merge-item", "project-b", mergeFile)
		ended <- err
	}()

	select {
	case <-merged:
	case <-time.After(eventTimeout):
		t.Fatal("the merge of project B waited for the gate of project A")
	}

	close(gated.release)
	require.NoError(t, <-ended)
	require.NoError(t, <-ended)
}

type cancelGate struct {
	started chan struct{}
	runs    *atomic.Int32
}

func (cancelGate) Submit(context.Context, string, string) (GateJob, error) { return GateJob{}, nil }

func (g cancelGate) Run(ctx context.Context, _, _ string, _ GateJob) (GateResult, error) {
	if g.runs.Add(1) == 1 {
		g.started <- struct{}{}
		<-ctx.Done()

		return GateResult{}, ctx.Err()
	}

	return GateResult{Passed: true, Head: gatedHead, Steps: commandStep(true)}, nil
}

func TestExclusive_ACancelWhileTheGateRunsThenAResumeEndsTheItemWithOneMerge(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})
	gated := cancelGate{started: make(chan struct{}, 1), runs: &atomic.Int32{}}
	deps := h.deps()
	deps.Gate = gated
	h.migrated(t)

	engine, err := NewEngine(t.Context(), h.opts, deps)
	require.NoError(t, err)
	require.NoError(t, engine.Launch())
	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	file := writeFlow(t, bodyFlow("gate(name = \"check\")\n"+shipCall("merge = True, ")))
	ended := make(chan error, 1)

	go func() {
		_, err := engine.RunFile(testItem, testProject, file)
		ended <- err
	}()

	<-gated.started

	client := h.client(t, testVersion)
	require.NoError(t, client.Cancel(testItem))
	h.tick(t)

	select {
	case err := <-ended:
		require.Error(t, err)
	case <-time.After(20 * time.Second):
		t.Fatal("the run did not end after the cancel")
	}

	assert.Empty(t, h.merge.asked, "no merge ran before the gate ended")
	result, err := resumeAndJoin(t, engine, client)

	require.NoError(t, err)
	assert.Equal(t, "merged", result.Merge)
	assert.EqualValues(t, 2, gated.runs.Load(), "the canceled gate ran again once")
	assert.Len(t, h.merge.asked, 1, "the merge ran once")
}

func TestExclusive_ABatchMergeOfARepositoryStartsOnlyAfterTheGateOfThatRepositoryEnds(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	gated := slowGate{started: make(chan struct{}, 1), release: make(chan struct{})}
	deps := rig.deps()
	deps.Gate = gated
	rig.h.migrated(t)

	engine, err := NewEngine(t.Context(), rig.h.opts, deps)
	require.NoError(t, err)
	require.NoError(t, engine.Launch())
	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	var group sync.WaitGroup

	ended := make(chan error, 2)

	group.Go(func() {
		_, err := engine.RunFile("gate-item", testProject, writeFlow(t, bodyFlow("gate(name = \"check\")\n")))
		ended <- err
	})

	<-gated.started

	group.Go(func() {
		_, err := engine.RunFile("batch-item", testProject, writeFlow(t, bodyFlow(rig.call("1 3", `gate_run = ["check-all"], `))))
		ended <- err
	})

	time.Sleep(2 * time.Second)
	assert.Empty(t, rig.h.commands.ran(), "the gate of the batch did not start while the gate of the repository ran")
	assert.Empty(t, rig.merger.landed(), "no member was merged while the gate of the repository ran")

	close(gated.release)
	group.Wait()
	require.NoError(t, <-ended)
	require.NoError(t, <-ended)
	assert.Equal(t, []string{"1", "3"}, rig.merger.landed(), "the batch merged after the gate ended")
}
