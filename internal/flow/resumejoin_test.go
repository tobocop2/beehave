package flow

import (
	"testing"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/require"
)

// resumeRun resumes the latest run of the test item and returns the handle of that run, found by its own id. Resume reopens the run under its
// own id and the queue starts it at once. A RunFile or Run call after the resumed run has ended starts a second attempt, so a test that
// collects the result that way can check a fresh run and not the replay it names. The id is read before the resume: the resume keeps it.
func resumeRun(t *testing.T, engine *Engine, client *Client) dbos.WorkflowHandle[ItemResult] {
	t.Helper()

	last, found, err := latestRun(engine.ctx, testItem)
	require.NoError(t, err)
	require.True(t, found, "no run to resume")

	require.NoError(t, client.Resume(testItem))

	handle, err := dbos.RetrieveWorkflow[ItemResult](engine.ctx, last.Status.ID)
	require.NoError(t, err)

	return handle
}

// joinRun waits for the result of the resumed run under a deadline.
func joinRun(handle dbos.WorkflowHandle[ItemResult]) (ItemResult, error) {
	return handle.GetResult(dbos.WithHandleTimeout(eventTimeout), dbos.WithHandlePollingInterval(storePoll))
}

// resumeAndJoin resumes the latest run of the test item and waits for that run's own result by its id.
func resumeAndJoin(t *testing.T, engine *Engine, client *Client) (ItemResult, error) {
	t.Helper()

	return joinRun(resumeRun(t, engine, client))
}
