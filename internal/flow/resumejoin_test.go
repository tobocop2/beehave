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

	return resumeRunOf(t, engine, client, testItem)
}

// resumeRunOf is resumeRun for the item named.
func resumeRunOf(t *testing.T, engine *Engine, client *Client, item string) dbos.WorkflowHandle[ItemResult] {
	t.Helper()

	last, found, err := latestRun(viewOf(engine.ctx, engine.dir), item)
	require.NoError(t, err)
	require.True(t, found, "no run to resume")

	require.NoError(t, client.Resume(item))

	handle, err := dbos.RetrieveWorkflow[ItemResult](engine.ctx, last.Status.ID)
	require.NoError(t, err)

	return handle
}

// resumeAndJoin resumes the latest run of the test item and waits for that run's own result by its id.
func resumeAndJoin(t *testing.T, engine *Engine, client *Client) (ItemResult, error) {
	t.Helper()

	result, err := resultOfHandle(t, resumeRun(t, engine, client))
	printStateIfTheTestFails(t, engine, err, func() string { return "" })

	return result, err
}
