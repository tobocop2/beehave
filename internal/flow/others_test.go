package flow

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeOthers answers with the lines of the other orchestrators, or with an error, and records the asks.
type fakeOthers struct {
	mu    sync.Mutex
	lines []string
	err   error
	asked []string
}

func (f *fakeOthers) Others(_ context.Context, project, owner string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.asked = append(f.asked, project+"/"+owner)

	return f.lines, f.err
}

func TestRun_ListsTheOtherOrchestratorsAtTheStartAndStatusShowsThem(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.flowIs(t, waitFlow)
	harness.others = &fakeOthers{lines: []string{"alice: 3, 4"}}

	engine := harness.engine(t)
	client := harness.client(t, testVersion)

	done := runInBackground(engine, testItem)
	waitForTheOwner(t, harness)

	infos, err := client.Status()
	require.NoError(t, err)
	assert.Equal(t, []string{"alice: 3, 4"}, infos[0].Others, "status names the other orchestrators that hold items")
	require.Len(t, harness.others.asked, 1)
	assert.Contains(t, harness.others.asked[0], testProject+"/")

	require.NoError(t, client.Cancel(testItem))
	harness.tick(t)
	<-done
}

func TestRun_WithNoOtherOrchestratorShowsNoneAndRuns(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.others = &fakeOthers{}

	outcome := <-runInBackground(harness.engine(t), testItem)

	require.NoError(t, outcome.err)
	assert.Len(t, harness.others.asked, 1)
}

// stuckOthers sets a question and a list of other orchestrators that is not text, then waits for the owner.
func stuckOthers(ctx dbos.Context, _ string) (string, error) {
	if err := dbos.SetEvent(ctx, questionKey, "wait"); err != nil {
		return "", err
	}

	if err := dbos.SetEvent(ctx, othersKey, 42); err != nil {
		return "", err
	}

	return dbos.Recv[string](ctx, ownerTopic, time.Minute)
}

func TestClient_StatusReportsOtherOrchestratorsThatAreNotText(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	state, err := openStore(harness.opts)
	require.NoError(t, err)

	t.Cleanup(func() { _ = state.log.Close() })

	flowCtx, err := dbos.NewContext(t.Context(), dbos.Config{
		AppName: appName, DatabaseURL: state.url, ApplicationVersion: applicationVersion(testVersion), ExecutorID: executorName,
	})
	require.NoError(t, err)

	dbos.RegisterWorkflow(flowCtx, stuckOthers, dbos.WithWorkflowName(itemName))
	require.NoError(t, dbos.Launch(flowCtx))

	_, err = dbos.RunWorkflow(flowCtx, stuckOthers, stuckItemName, dbos.WithWorkflowID(stuckItemID))
	require.NoError(t, err)

	client := harness.client(t, testVersion)

	eventually(t, func() bool {
		_, err := client.Status()

		return err != nil
	})

	_, err = client.Status()
	require.ErrorContains(t, err, "read the other orchestrators of "+stuckItemID)
	require.NoError(t, dbos.CancelWorkflow(flowCtx, stuckItemID))
	require.NoError(t, dbos.Shutdown(flowCtx, shutdownTimeout))
}

func TestRun_FailsWhenTheOtherOrchestratorsCannotBeListed(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.others = &fakeOthers{err: errFake}

	outcome := <-runInBackground(harness.engine(t), testItem)

	require.ErrorIs(t, outcome.err, errFake, "a failed read is not read as nobody else runs")
}
