package flow

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const joinFlow = "command(run = [\"c\"])\n"

// rowsOf lists the runs of the item that the store holds.
func rowsOf(t *testing.T, client *Client, item string) []dbos.WorkflowStatus {
	t.Helper()

	rows, err := dbos.ListWorkflows(client.flow, dbos.WithFilterWorkflowIDPrefix(runPrefix+item+attemptMarker), dbos.WithFilterName(itemName))
	require.NoError(t, err)

	return rows
}

func TestJoinRun_AnEngineThatHoldsTheStateRunsTheEnqueuedItem(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.engine(t)
	client := harness.client(t, testVersion)

	result, err := client.JoinRun(t.Context(), testItem, testProject, writeFlow(t, bodyFlow(joinFlow)))

	require.NoError(t, err)
	assert.Equal(t, testItem, result.Item)

	rows := rowsOf(t, client, testItem)
	require.Len(t, rows, 1)
	assert.Equal(t, runID(testItem, 1), rows[0].ID)
	assert.Equal(t, itemsQueueName, rows[0].QueueName, "the run entered the items queue")
	assert.Equal(t, dbos.WorkflowStatusSuccess, rows[0].Status)
}

func TestJoinRun_TwoJoinsOfOneItemMakeOneRun(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.engine(t)
	client := harness.client(t, testVersion)
	file := writeFlow(t, bodyFlow("wait(name = \"ask\", question = \"What now?\")\n"))

	in, err := pinnedInput(testItem, testProject, file)
	require.NoError(t, err)

	// The run waits for an answer, so it is open while both joins plan. The second join cannot see a finished run and start attempt 2.
	first, err := client.planAndHandle(in)
	require.NoError(t, err)

	second, err := client.planAndHandle(in)
	require.NoError(t, err)

	require.Equal(t, first.GetWorkflowID(), second.GetWorkflowID(), "an open run is joined, so no second attempt starts")

	eventually(t, func() bool {
		infos, statusErr := client.Status()

		return statusErr == nil && len(infos) == 1 && infos[0].Question != ""
	})
	require.NoError(t, client.Answer(testItem, "go on"))

	firstResult, err := first.GetResult()
	require.NoError(t, err)

	secondResult, err := second.GetResult()
	require.NoError(t, err)

	assert.Equal(t, firstResult, secondResult)
	assert.Len(t, rowsOf(t, client, testItem), 1)
}

func TestJoinRun_AStatusThatCannotBeReadStopsTheWait(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	client, err := OpenClient(t.Context(), harness.opts, ReleaseDeps{})
	require.NoError(t, err)

	file := writeFlow(t, bodyFlow("wait(name = \"ask\", question = \"What now?\")\n"))

	in, err := pinnedInput(testItem, testProject, file)
	require.NoError(t, err)

	handle, err := client.planAndHandle(in)
	require.NoError(t, err)
	require.NoError(t, client.Close())

	watch := engineWatch{dir: client.dir, item: testItem, grace: time.Hour}
	_, done, err := watch.poll(handle)

	assert.False(t, done)
	require.ErrorContains(t, err, "read the status of the run")
}

// A serial test: with no parallel test running, no other goroutine waits for a result.
//
//nolint:paralleltest // The test reads the stacks of every goroutine in the process, so a parallel test of this package would put its own GetResult waiter in the stacks.
func TestJoinRun_ACancelledCallerLeavesNoPollerBehind(t *testing.T) {
	harness := newHarness(t)
	harness.engine(t)
	client := harness.client(t, testVersion)
	file := writeFlow(t, bodyFlow("wait(name = \"ask\", question = \"What now?\")\n"))

	ctx, stop := context.WithCancel(t.Context())

	done := make(chan error, 1)

	go func() {
		_, err := client.JoinRun(ctx, testItem, testProject, file)
		done <- err
	}()

	eventually(t, func() bool { return len(rowsOf(t, client, testItem)) == 1 })
	stop()

	require.ErrorIs(t, <-done, context.Canceled)

	eventually(t, func() bool {
		stacks := make([]byte, 1<<20)
		stacks = stacks[:runtime.Stack(stacks, true)]

		return !bytes.Contains(stacks, []byte("GetResult"))
	})
}

func TestJoinRun_TheCheckReadsTheVersionOfTheLiveEngineNotTheNewestRegistered(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)

	newer := harness.opts
	newer.Version = "v2"

	other, err := NewEngine(t.Context(), newer, harness.deps())
	require.NoError(t, err)
	require.NoError(t, other.Launch())
	require.NoError(t, other.Shutdown())

	harness.engine(t)
	client := harness.client(t, testVersion)

	// The stopped v2 build becomes the newest registered version again, and the live engine does not run it.
	require.NoError(t, dbos.SetLatestApplicationVersion(client.flow, applicationVersion("v2")))

	latest, err := dbos.GetLatestApplicationVersion(client.flow)
	require.NoError(t, err)
	require.Equal(t, applicationVersion("v2"), latest.Name)
	require.NoError(t, client.sameBuildAsEngine(), "the live engine runs %s, the newest registered is %s", testVersion, latest.Name)
}

func TestJoinRun_AnOpenRunIsJoinedAndNotEnqueuedAgain(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	engine := harness.engine(t)
	client := harness.client(t, testVersion)
	file := writeFlow(t, bodyFlow("wait(name = \"ask\", question = \"What now?\")\n"))

	started := make(chan error, 1)

	go func() {
		_, err := engine.RunFile(testItem, testProject, file)
		started <- err
	}()

	eventually(t, func() bool { return len(rowsOf(t, client, testItem)) == 1 })

	joined := make(chan error, 1)

	go func() {
		_, err := client.JoinRun(t.Context(), testItem, testProject, file)
		joined <- err
	}()

	eventually(t, func() bool {
		infos, err := client.Status()

		return err == nil && len(infos) == 1 && infos[0].Question != ""
	})
	require.NoError(t, client.Answer(testItem, "go on"))

	require.NoError(t, <-started)
	require.NoError(t, <-joined)

	rows := rowsOf(t, client, testItem)
	require.Len(t, rows, 1)
	assert.Empty(t, rows[0].QueueName, "the run that the engine started stays out of the queue")
}

func TestJoinRun_AFailedRunIsRefusedWithTheTextOfRun(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.agents.err = errFake
	engine := harness.engine(t)
	client := harness.client(t, testVersion)

	_, err := engine.Run(testItem, testProject)
	require.ErrorContains(t, err, "fake failure")

	_, want := engine.Run(testItem, testProject)
	_, err = client.JoinRun(t.Context(), testItem, testProject, writeFlow(t, defaultFlow))

	require.ErrorIs(t, err, errRunFailed)
	require.EqualError(t, err, want.Error())
	assert.Len(t, rowsOf(t, client, testItem), 1)
}

func TestJoinRun_ABuildThatTheEngineDoesNotRunIsRefusedAndEnqueuesNothing(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.engine(t)
	client := harness.client(t, "v2")

	_, err := client.JoinRun(t.Context(), testItem, testProject, writeFlow(t, bodyFlow(joinFlow)))

	require.ErrorIs(t, err, errOtherBuild)
	require.ErrorContains(t, err, "restart beehave serve")
	assert.Empty(t, rowsOf(t, client, testItem))
}

func TestJoinRun_ABadItemAndAMissingFileAreRefusedBeforeAnyRowIsWritten(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.engine(t)
	client := harness.client(t, testVersion)

	_, err := client.JoinRun(t.Context(), "not an item", testProject, writeFlow(t, defaultFlow))
	require.ErrorIs(t, err, ErrBadItem)

	_, err = client.JoinRun(t.Context(), testItem, "", writeFlow(t, defaultFlow))
	require.ErrorIs(t, err, errNoProject)

	_, err = client.JoinRun(t.Context(), testItem, testProject, "/no/such/workflow.star")
	require.Error(t, err)
	assert.Empty(t, rowsOf(t, client, testItem))
}

func TestJoinRun_AnOpenRunOfAnotherProjectIsRefused(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	engine := harness.engine(t)
	client := harness.client(t, testVersion)
	file := writeFlow(t, bodyFlow("wait(name = \"ask\", question = \"What now?\")\n"))

	ended := make(chan error, 1)

	go func() {
		_, err := engine.RunFile(testItem, testProject, file)
		ended <- err
	}()

	eventually(t, func() bool { return len(rowsOf(t, client, testItem)) == 1 })

	_, err := client.JoinRun(t.Context(), testItem, "other", file)

	require.ErrorIs(t, err, errProjectConflict)
}

func TestJoinRun_AnEngineThatStopsLeavesTheRunQueuedAndTheWaiterSaysSo(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	engine := harness.engine(t)
	client := harness.client(t, testVersion)
	client.give.Clock.Poll = time.Millisecond
	client.give.Clock.Gone = 50 * time.Millisecond

	// Stop the engine's workers from taking the run: hold it with a full queue before the engine goes away.
	require.NoError(t, engine.Shutdown())

	_, err := client.JoinRun(t.Context(), testItem, testProject, writeFlow(t, bodyFlow(joinFlow)))

	require.ErrorIs(t, err, ErrEngineGone)
	require.ErrorContains(t, err, "start beehave serve or run beehave run 12 again")

	rows := rowsOf(t, client, testItem)
	require.Len(t, rows, 1)
	assert.Equal(t, dbos.WorkflowStatusEnqueued, rows[0].Status)
}

func TestJoinRun_ACancelledCallerStopsWaitingAndTheRunStays(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.engine(t)
	client := harness.client(t, testVersion)
	file := writeFlow(t, bodyFlow("wait(name = \"ask\", question = \"What now?\")\n"))

	ctx, stop := context.WithCancel(t.Context())

	done := make(chan error, 1)

	go func() {
		_, err := client.JoinRun(ctx, testItem, testProject, file)
		done <- err
	}()

	eventually(t, func() bool { return len(rowsOf(t, client, testItem)) == 1 })
	stop()

	require.ErrorIs(t, <-done, context.Canceled)
	assert.Len(t, rowsOf(t, client, testItem), 1)
}

func TestEngineRuns_TheClientSeesTheLockOfALiveEngine(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	client := harness.client(t, testVersion)

	running, err := client.EngineRuns()
	require.NoError(t, err)
	assert.False(t, running)

	engine := harness.engine(t)

	running, err = client.EngineRuns()
	require.NoError(t, err)
	assert.True(t, running)

	require.NoError(t, engine.Shutdown())
}

func TestPlanOf_TheSamePlanForEveryStateOfTheLatestRun(t *testing.T) {
	t.Parallel()

	in := ItemInput{Item: testItem, Project: testProject}
	version := applicationVersion(testVersion)
	input := `{"item":"12","project":"` + testProject + `"}`

	open := func(status dbos.WorkflowStatusType, stored string) run {
		return run{Attempt: 2, Status: dbos.WorkflowStatus{ID: runID(testItem, 2), Status: status, ApplicationVersion: stored, Input: input}}
	}

	tests := []struct {
		name    string
		last    run
		found   bool
		resumed bool
		want    attemptPlan
		wantErr error
	}{
		{name: "never ran", want: attemptPlan{Attempt: 1}},
		{name: "finished", last: open(dbos.WorkflowStatusSuccess, version), found: true, want: attemptPlan{Attempt: 3}},
		{name: "canceled", last: open(dbos.WorkflowStatusCancelled, version), found: true, want: attemptPlan{Attempt: 3}},
		{name: "failed", last: open(dbos.WorkflowStatusError, version), found: true, wantErr: errRunFailed},
		{name: "open", last: open(dbos.WorkflowStatusPending, version), found: true, want: attemptPlan{Join: runID(testItem, 2)}},
		{name: "queued", last: open(dbos.WorkflowStatusEnqueued, version), found: true, want: attemptPlan{Join: runID(testItem, 2)}},
		{name: "ended since the launch", last: open(dbos.WorkflowStatusSuccess, version), found: true, resumed: true, want: attemptPlan{Join: runID(testItem, 2)}},
		{name: "open on another build", last: open(dbos.WorkflowStatusPending, "v0+"+stepsVersion), found: true, wantErr: errStranded},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got, err := planOf(test.last, test.found, in, version, test.resumed)

			require.ErrorIs(t, err, test.wantErr)
			assert.Equal(t, test.want, got)
		})
	}

	_, err := planOf(open(dbos.WorkflowStatusPending, version), true, ItemInput{Item: testItem, Project: "other"}, version, false)
	require.ErrorIs(t, err, errProjectConflict)

	bad := open(dbos.WorkflowStatusPending, version)
	bad.Status.Input = "not json"
	_, err = planOf(bad, true, in, version, false)
	require.ErrorContains(t, err, "read the input of run")
}

func TestJoinRun_AStoreThatIsGoneStopsEveryStepOfTheJoin(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	closed, err := OpenClient(t.Context(), harness.opts, ReleaseDeps{})
	require.NoError(t, err)
	require.NoError(t, closed.Close())

	in := ItemInput{Item: testItem, Project: testProject}

	require.ErrorContains(t, closed.sameBuildAsEngine(), "read the version of the engine")

	_, err = closed.planAndHandle(in)
	require.ErrorContains(t, err, "list the runs of item 12")

	_, err = closed.handleOf(attemptPlan{Attempt: 1}, in)
	require.ErrorContains(t, err, "queue the run of item 12")
}

func TestOutcome_AFailedRunIsNamedAfterTheItem(t *testing.T) {
	t.Parallel()

	_, err := outcome{err: errFake}.unwrap(testItem)

	require.ErrorIs(t, err, errFake)
	require.ErrorContains(t, err, "item 12")
}

func TestEngineWatch_AStateFolderThatCannotBeReadStopsTheWait(t *testing.T) {
	t.Parallel()

	file := filepath.Join(t.TempDir(), "not-a-folder")
	require.NoError(t, os.WriteFile(file, nil, 0o600))

	watch := engineWatch{dir: file, item: testItem, grace: time.Hour}

	require.ErrorContains(t, watch.check(), probeLockName)
}
