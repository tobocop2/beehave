package flow

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/config"
	"github.com/tobocop2/obee/internal/merge"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/review"
)

// orderGate holds the gate of the item "first" until the test lets it go, ends every other gate at once, and records the items in the order the gates start.
type orderGate struct {
	supersedeGate
	mu      *sync.Mutex
	order   *[]string
	holdRun string
}

func (g orderGate) Run(ctx context.Context, project, item, host string, job GateJob) (GateResult, error) {
	g.mu.Lock()
	*g.order = append(*g.order, project+"/"+item)
	g.mu.Unlock()

	if item != g.holdRun {
		return GateResult{Passed: true, Head: gatedHead, Steps: commandStep(true)}, nil
	}

	return g.supersedeGate.Run(ctx, project, item, host, job)
}

func (g orderGate) started() []string {
	g.mu.Lock()
	defer g.mu.Unlock()

	return append([]string(nil), *g.order...)
}

// personForge says that a person waits on the pull request of the items it names.
type personForge struct {
	*fakeForge
	waiting map[string]bool
}

func (f personForge) Pull(ctx context.Context, project, item string) (review.Standing, error) {
	standing, err := f.fakeForge.Pull(ctx, project, item)
	if f.waiting[item] {
		standing.UnresolvedThreads = 1
	}

	return standing, err
}

// hostGateRuns numbers the runs that queue a gate, so that no two tests share the id of a run, and so the channel of its end.
var hostGateRuns atomic.Int64

// queueHostGate queues the project gate of an item that runs on the build host, as a run of a flow does, and returns the channel of its end.
func queueHostGate(engine *Engine, project, item, head, host string) <-chan gateEnd {
	ended := make(chan gateEnd, 1)
	id := fmt.Sprintf("queue-host-gate-%d-%s-%s-%s-%s", hostGateRuns.Add(1), project, item, head, host)
	gateEndsMu.Lock()
	gateEnds[id] = ended
	gateEndsMu.Unlock()

	in := ExclusiveInput{Kind: kindGate, Project: project, Item: item, Head: head, Request: CommandRequest{Project: project, Item: item, BuildHost: host}}

	if _, err := dbos.RunWorkflow(engine.ctx, gateQueuer{engine}.queue, in, dbos.WithWorkflowID(id)); err != nil {
		ended <- gateEnd{err: err}
	}

	return ended
}

func TestExclusiveHost_TwoProjectsOnOneHostRunOneGateAtATime(t *testing.T) {
	t.Parallel()

	gated := newSupersedeGate("")
	_, engine := supersedeEngine(t, gated, "A")

	first := queueHostGate(engine, "project-one", "1", "A", "box")

	eventually(t, startedCount(gated, 1))

	second := queueHostGate(engine, "project-two", "2", "A", "box")

	eventually(t, waitsOnHost(engine, 1))

	// The queue polls once for each second, so the second job would have started within three polls had its host been free.
	require.Never(t, func() bool { return gated.started.Load() > 1 }, 3*storePoll, 100*time.Millisecond, "the second gate waits while the first runs on the same host")

	close(gated.release)

	require.NoError(t, (<-first).err)
	require.NoError(t, (<-second).err)
	assert.EqualValues(t, 2, gated.started.Load())
}

func TestExclusiveHost_TwoHostsRunTheirGatesTogether(t *testing.T) {
	t.Parallel()

	gated := newSupersedeGate("")
	_, engine := supersedeEngine(t, gated, "A")

	first := queueHostGate(engine, "project-one", "1", "A", "box-one")
	second := queueHostGate(engine, "project-two", "2", "A", "box-two")

	eventually(t, startedCount(gated, 2))
	assert.True(t, waitsOnHost(engine, 0)(), "no gate waits when each host has one")

	close(gated.release)

	require.NoError(t, (<-first).err)
	require.NoError(t, (<-second).err)
}

func TestExclusiveHost_AGateWithNoHostKeepsTheQueueOfItsProject(t *testing.T) {
	t.Parallel()

	gated := newSupersedeGate("")
	_, engine := supersedeEngine(t, gated, "A")

	first := queueHostGate(engine, "project-one", "1", "A", "")
	second := queueHostGate(engine, "project-two", "2", "A", "")

	eventually(t, startedCount(gated, 2))

	third := queueHostGate(engine, "project-one", "3", "A", "")

	eventually(t, waitsInTheQueue(engine, 1))

	close(gated.release)

	for _, ended := range []<-chan gateEnd{first, second, third} {
		require.NoError(t, (<-ended).err)
	}
}

func TestExclusiveHost_ThePersonWaitingGateGoesFirstOnItsHost(t *testing.T) {
	t.Parallel()

	var (
		mu    sync.Mutex
		order []string
	)

	gate := orderGate{supersedeGate: newSupersedeGate(""), mu: &mu, order: &order, holdRun: "first"}
	h := newHarness(t)
	h.forge.state = review.Standing{Number: testPull, Head: "A"}
	deps := h.deps()
	deps.Gate = gate
	deps.Forge = personForge{fakeForge: h.forge, waiting: map[string]bool{"urgent": true}}

	engine, err := NewEngine(t.Context(), h.opts, deps)
	require.NoError(t, err)
	dbos.RegisterWorkflow(engine.ctx, gateQueuer{engine}.queue, dbos.WithWorkflowName("test-queue-gate"))
	require.NoError(t, engine.Launch())
	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	first := queueHostGate(engine, "project-one", "first", "A", "box")

	eventually(t, func() bool { return len(gate.started()) == 1 })

	usual := queueHostGate(engine, "project-two", "usual", "A", "box")

	eventually(t, waitsOnHost(engine, 1))

	urgent := queueHostGate(engine, "project-three", "urgent", "A", "box")

	eventually(t, waitsOnHost(engine, 2))
	close(gate.release)

	for _, ended := range []<-chan gateEnd{first, usual, urgent} {
		require.NoError(t, (<-ended).err)
	}

	assert.Equal(t, []string{"project-one/first", "project-three/urgent", "project-two/usual"}, gate.started())
}

func TestExclusiveHost_ANewHeadStopsTheOldGateOnTheSameHost(t *testing.T) {
	t.Parallel()

	gated := newSupersedeGate("")
	h, engine := supersedeEngine(t, gated, "A")

	old := queueHostGate(engine, testProject, testItem, "A", "box")

	eventually(t, startedCount(gated, 1))
	setHead(h, "B")

	current := queueHostGate(engine, testProject, testItem, "B", "box")

	require.ErrorIs(t, endsAfterTicks(t, h, old).err, errSuperseded)

	// The job of the old gate stops at a tick after its gate canceled it, and a tick of the engine here comes only from the test.
	eventually(t, func() bool {
		h.tick(t)

		return gated.stopped.Load() == 1
	})
	close(gated.release)

	require.NoError(t, (<-current).err)
}

// launchHostEngine starts an engine over the harness with gate as the gate of every project, and the workflow that queues one gate or merge.
func launchHostEngine(t *testing.T, h *harness, gate gate) *Engine {
	t.Helper()

	deps := h.deps()
	deps.Gate = gate

	engine, err := NewEngine(t.Context(), h.opts, deps)
	require.NoError(t, err)
	dbos.RegisterWorkflow(engine.ctx, gateQueuer{engine}.queue, dbos.WithWorkflowName("test-queue-gate"))
	require.NoError(t, engine.Launch())
	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	return engine
}

func TestExclusiveHost_ReportsAHostQueueThatTheStoreRefuses(t *testing.T) {
	t.Parallel()

	engine, err := NewEngine(t.Context(), newHarness(t).opts, Deps{})
	require.NoError(t, err)
	require.NoError(t, engine.Shutdown())

	require.ErrorContains(t, engine.registerHostQueue(), "register the host queue")
}

func TestExclusiveHost_ReportsABuildJobThatTheStoreRefuses(t *testing.T) {
	t.Parallel()

	engine, err := NewEngine(t.Context(), newHarness(t).opts, Deps{})
	require.NoError(t, err)
	require.NoError(t, engine.Shutdown())

	var out ExclusiveOutput

	err = engine.onHost(engine.ctx, ExclusiveInput{Kind: kindGate, Project: testProject, Item: "12", HostKey: hostKeyPrefix + "box"}, &out)
	require.ErrorContains(t, err, "queue the build job of item 12 on host box")
}

func TestExclusiveHost_SaysSoWhenItCannotStopTheBuildJobOfAGate(t *testing.T) {
	t.Parallel()

	engine, err := NewEngine(t.Context(), newHarness(t).opts, Deps{})
	require.NoError(t, err)
	require.NoError(t, engine.Shutdown())

	var logged bytes.Buffer

	engine.logger = newLogger(&logged)
	engine.stopJob(t.Context(), ExclusiveInput{Item: "12"}, "job-1")

	assert.Contains(t, logged.String(), "cannot stop the build job of the gate")
}

func TestExclusiveHost_AReopenReportsABuildJobThatIsGone(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})
	gated := newHostGate()
	deps := h.deps()
	deps.Gate = gated
	deps.Hosts = &upHosts{started: true}

	engine, err := NewEngine(t.Context(), h.opts, deps)
	require.NoError(t, err)
	require.NoError(t, engine.Launch())
	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	file := writeFlow(t, bodyFlow("host_up(name = \"start\", host = \"gpu-b\")\ngate(name = \"check\")\n"))
	ended := make(chan error, 1)

	go func() {
		_, runErr := engine.RunFile(testItem, testProject, file)
		ended <- runErr
	}()

	<-gated.started

	client := h.client(t, testVersion)
	require.NoError(t, client.Cancel(testItem))
	h.tick(t)
	require.Error(t, <-ended)

	itemSteps, err := dbos.GetWorkflowSteps(client.flow, "item-12-r1")
	require.NoError(t, err)

	var jobs []string

	for _, step := range itemSteps {
		if step.ChildWorkflowID == "" {
			continue
		}

		gateSteps, err := dbos.GetWorkflowSteps(client.flow, step.ChildWorkflowID)
		require.NoError(t, err)

		for _, inner := range gateSteps {
			if inner.ChildWorkflowID != "" {
				jobs = append(jobs, inner.ChildWorkflowID)
			}
		}
	}

	require.NotEmpty(t, jobs, "a gate on a host records its build job as a child run")
	require.NoError(t, dbos.DeleteWorkflows(client.flow, jobs))
	require.ErrorIs(t, client.reopenCanceledPhases("item-12-r1"), errNoRow)
}

func TestExclusiveHost_ACancelWithNoEngineCancelsTheBuildJobOfTheGate(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})
	gated := newHostGate()
	deps := h.deps()
	deps.Gate = gated
	deps.Hosts = &upHosts{started: true}

	engine, err := NewEngine(t.Context(), h.opts, deps)
	require.NoError(t, err)
	require.NoError(t, engine.Launch())

	file := writeFlow(t, bodyFlow("host_up(name = \"start\", host = \"gpu-b\")\ngate(name = \"check\")\n"))
	ended := make(chan error, 1)

	go func() {
		_, runErr := engine.RunFile(testItem, testProject, file)
		ended <- runErr
	}()

	<-gated.started
	require.NoError(t, engine.Shutdown())
	require.Error(t, <-ended, "the stop of the engine ends the run with an error")

	client := h.client(t, testVersion)
	jobs, err := dbos.ListWorkflows(client.flow, dbos.WithFilterName(hostJobName))
	require.NoError(t, err)
	require.Len(t, jobs, 1, "the gate on a host queued one build job")
	require.NotEqual(t, dbos.WorkflowStatusCancelled, jobs[0].Status, "a stopped engine leaves the job")

	require.NoError(t, client.Cancel(testItem))

	job, err := dbos.RetrieveWorkflow[ExclusiveOutput](client.flow, jobs[0].ID)
	require.NoError(t, err)

	status, err := job.GetStatus()
	require.NoError(t, err)
	assert.Equal(t, dbos.WorkflowStatusCancelled, status.Status, "the job is a grandchild of the item, so the cancel names it")
}

// hostRecordingGate passes at once for every item but the one it holds, and gives every gate the key of one tree, so a pass of one gate stands in for the next.
type hostRecordingGate struct {
	supersedeGate
	holdItem string
	quick    *atomic.Int32
}

func (hostRecordingGate) Key(context.Context, string, string, string) (GateKey, error) {
	return GateKey{ID: "one-tree", Head: gatedHead, Tree: "tree"}, nil
}

func (g hostRecordingGate) Run(ctx context.Context, project, item, host string, job GateJob) (GateResult, error) {
	if item == g.holdItem {
		return g.supersedeGate.Run(ctx, project, item, host, job)
	}

	g.quick.Add(1)

	return GateResult{Passed: true, Head: gatedHead, Tree: "tree", Steps: commandStep(true)}, nil
}

func TestExclusiveHost_AGateThatReusesAPassNeverWaitsForTheHost(t *testing.T) {
	t.Parallel()

	gated := hostRecordingGate{supersedeGate: newSupersedeGate(""), holdItem: "hold", quick: &atomic.Int32{}}
	h := newHarness(t)
	h.forge.state = review.Standing{Number: testPull, Head: "A"}
	engine := launchHostEngine(t, h, gated)

	require.NoError(t, (<-queueHostGate(engine, "project-two", "early", "A", "box")).err)

	busy := queueHostGate(engine, "project-one", "hold", "A", "box")

	eventually(t, startedCount(gated.supersedeGate, 1))

	late := queueHostGate(engine, "project-two", "late", "A", "box")

	select {
	case end := <-late:
		require.NoError(t, end.err)
	case <-time.After(10 * storePoll):
		t.Fatal("the gate that reuses a pass waits for the host of the gate that runs")
	}

	assert.EqualValues(t, 1, gated.quick.Load(), "the late gate did not run: the earlier pass stands in for it")
	assert.EqualValues(t, 1, gated.started.Load(), "the first gate still holds the host")

	close(gated.release)

	require.NoError(t, (<-busy).err)
}

func TestExclusiveHost_TheHostOfAGateIsRecordedWhenItIsQueued(t *testing.T) {
	t.Parallel()

	gated := newSupersedeGate("")
	h := newHarness(t)
	h.forge.state = review.Standing{Number: testPull, Head: "A"}
	h.facts = func(string) model.Project { return model.Project{BuildHost: "box"} }
	engine := launchHostEngine(t, h, gated)

	close(gated.release)
	require.NoError(t, (<-queueHostGate(engine, "project-one", "1", "A", "")).err)

	rows, err := dbos.ListWorkflows(engine.ctx, dbos.WithFilterName(exclusiveName), dbos.WithFilterLoadInput(true))
	require.NoError(t, err)
	require.Len(t, rows, 1)

	in, ok := gateInputOf(rows[0].Input)
	require.True(t, ok)
	assert.Equal(t, hostKeyPrefix+"box", in.HostKey, "the input of the gate keeps the host that the project named when the gate was queued")
}

func TestExclusiveHost_AReplayRunsOnTheHostTheFirstRunChoseWhenTheProjectNamesAnotherNow(t *testing.T) {
	t.Parallel()

	gated := newSupersedeGate("")
	h := newHarness(t)
	h.forge.state = review.Standing{Number: testPull, Head: "A"}
	h.facts = func(string) model.Project { return model.Project{BuildHost: config.LocalHost} }
	engine := launchHostEngine(t, h, gated)

	close(gated.release)

	in := ExclusiveInput{Kind: kindGate, Project: testProject, Item: "1", Head: "A", HostKey: hostKeyPrefix + "box", Request: CommandRequest{Project: testProject, Item: "1"}}
	handle, err := dbos.RunWorkflow(engine.ctx, engine.exclusive, in, dbos.WithQueue(engine.exclusiveQueue), dbos.WithQueuePartitionKey(testProject),
		dbos.WithWorkflowID("replay-host-gate"))
	require.NoError(t, err)

	_, err = handle.GetResult()
	require.NoError(t, err)

	jobs, err := dbos.ListWorkflows(engine.ctx, dbos.WithFilterName(hostJobName), dbos.WithFilterLoadInput(true))
	require.NoError(t, err)
	require.Len(t, jobs, 1, "the gate runs on the host of its record, not the host of the project file now")

	job, ok := gateInputOf(jobs[0].Input)
	require.True(t, ok)
	assert.Equal(t, hostKeyPrefix+"box", job.HostKey)
}

// waitsOnHost is true when the host queue holds want build jobs that wait for the job that runs.
func waitsOnHost(engine *Engine, want int) func() bool {
	return func() bool {
		rows, err := dbos.ListWorkflows(engine.ctx, dbos.WithFilterName(hostJobName), dbos.WithFilterStatus(dbos.WorkflowStatusEnqueued))

		return err == nil && len(rows) == want
	}
}

func TestExclusiveHost_AMergeWaitsForAGateOfItsProjectThatRunsOnAHost(t *testing.T) {
	t.Parallel()

	gated := newSupersedeGate("")
	h := newHarness(t)
	h.forge.state = review.Standing{Number: testPull, Head: "A"}
	h.merge = &fakeMerge{}
	engine := launchHostEngine(t, h, gated)

	gate := queueHostGate(engine, testProject, "1", "A", "box")

	eventually(t, startedCount(gated, 1))

	merged := make(chan gateEnd, 1)
	id := "queue-host-merge"
	gateEndsMu.Lock()
	gateEnds[id] = merged
	gateEndsMu.Unlock()

	_, err := dbos.RunWorkflow(engine.ctx, gateQueuer{engine}.queue, ExclusiveInput{Kind: kindMerge, Project: testProject, Item: "2", Head: "A"}, dbos.WithWorkflowID(id))
	require.NoError(t, err)

	eventually(t, waitsInTheQueue(engine, 1))

	// The queue polls once for each second, so the merge would have started within three polls had its partition been free.
	mergeAsked := func() bool {
		h.merge.mu.Lock()
		defer h.merge.mu.Unlock()

		return len(h.merge.asked) > 0
	}

	require.Never(t, mergeAsked, 3*storePoll, 100*time.Millisecond, "the merge waits while a gate of its project runs on a host")

	close(gated.release)

	require.NoError(t, (<-gate).err)
	require.NoError(t, (<-merged).err)
}

func TestExclusiveHost_AProjectThatNamesNoHostRunsOnItsDefaultHost(t *testing.T) {
	t.Parallel()

	gated := newSupersedeGate("")
	h := newHarness(t)
	h.forge.state = review.Standing{Number: testPull, Head: "A"}
	h.facts = func(string) model.Project { return model.Project{BuildHost: "box"} }
	engine := launchHostEngine(t, h, gated)

	first := queueHostGate(engine, "project-one", "1", "A", "")

	eventually(t, startedCount(gated, 1))

	second := queueHostGate(engine, "project-two", "2", "A", "")

	eventually(t, waitsOnHost(engine, 1))
	close(gated.release)

	require.NoError(t, (<-first).err)
	require.NoError(t, (<-second).err)
}

func TestExclusiveHost_TheHostOfAGateIsNamedOnlyWhenTheGateRunsThere(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   ExclusiveInput
		want string
	}{
		{"a_project_gate_on_a_host", ExclusiveInput{Kind: kindGate, Project: "p", Request: CommandRequest{BuildHost: "box"}}, "box"},
		{"a_project_gate_with_no_host_takes_the_default", ExclusiveInput{Kind: kindGate, Project: "p"}, "default-box"},
		{"a_project_gate_on_this_machine", ExclusiveInput{Kind: kindGate, Project: "p", Request: CommandRequest{BuildHost: "local"}}, ""},
		{"a_program_gate_on_a_host", ExclusiveInput{Kind: kindGate, Project: "p", Request: CommandRequest{Run: []string{"x"}, Host: "build", BuildHost: "box"}}, "box"},
		{"a_program_gate_on_this_machine", ExclusiveInput{Kind: kindGate, Project: "p", Request: CommandRequest{Run: []string{"x"}, BuildHost: "box"}}, ""},
		{"a_batch_gate_on_a_host", ExclusiveInput{Kind: kindBatchGate, Project: "p", BuildHost: "box"}, "box"},
		{"a_batch_gate_with_a_program", ExclusiveInput{Kind: kindBatchGate, Project: "p", BuildHost: "box", GateRun: []string{"x"}}, ""},
		{"a_merge", ExclusiveInput{Kind: kindMerge, Project: "p", Request: CommandRequest{BuildHost: "box"}}, ""},
	}

	engine := &Engine{deps: Deps{Facts: func(string) model.Project { return model.Project{BuildHost: "default-box"} }}}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, test.want, engine.gateHost(test.in))
		})
	}
}
