package flow

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
)

const finishLimit = 30 * time.Second

// expireLease makes the lease of the claim in the store end, as it does when the engine that held it is gone.
func expireLease(h *harness) {
	h.beats.setRenewed(time.Now().Add(-model.ClaimLease - time.Minute))
}

func TestClient_ResumeOnThisBuildMovesTheWholeTreeOfAFailedRun(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.agents.err = errFake
	engine := harness.engine(t)

	_, err := engine.Run(testItem, testProject)
	require.Error(t, err)
	require.NoError(t, engine.Shutdown())

	harness.agents.mu.Lock()
	harness.agents.err = nil
	harness.agents.mu.Unlock()

	other := harness.client(t, otherBuild)
	require.NoError(t, other.ResumeOnThisBuild(testItem))

	rows, err := dbos.ListWorkflows(other.flow)
	require.NoError(t, err)
	require.Greater(t, len(rows), 1, "the failed run has a phase")

	for _, row := range rows {
		if row.Name == itemName || row.Name == phaseName {
			assert.Equal(t, other.version, row.ApplicationVersion, "%s moves with the run", row.ID)
		}
	}

	harness.opts.Version = otherBuild
	done := runInBackground(harness.engine(t), testItem)

	select {
	case out := <-done:
		require.NoError(t, out.err)
	case <-time.After(finishLimit):
		t.Fatal("the moved run did not finish on the new build")
	}
}

func TestClient_ResumeOnThisBuildRefusesARunWhoseLeaseIsLive(t *testing.T) {
	t.Parallel()

	harness, _, other := strandedHarness(t)
	harness.beats.setRenewed(time.Now())

	err := other.ResumeOnThisBuild(testItem)

	require.ErrorIs(t, err, errStranded)
	require.ErrorContains(t, err, "claim lease of")
	require.ErrorContains(t, err, "ends at")
	require.ErrorContains(t, err, "Stop that build first")

	row, rowErr := other.row(testRun)
	require.NoError(t, rowErr)
	assert.Equal(t, testVersion, buildVersion(row.ApplicationVersion), "a run with a live holder stays with its build")
	assert.NotEqual(t, dbos.WorkflowStatusCancelled, row.Status, "a run with a live holder is not canceled")
}

func TestClient_ResumeOnThisBuildRefusesARunOfAnotherStepListVersion(t *testing.T) {
	t.Parallel()

	harness, _, _ := strandedHarness(t)
	db, err := sql.Open("sqlite", filepath.Join(harness.opts.StateDir, databaseFile)+"?_pragma=busy_timeout(30000)")
	require.NoError(t, err)

	t.Cleanup(func() { require.NoError(t, db.Close()) })

	_, err = db.ExecContext(t.Context(), "UPDATE workflow_status SET application_version = ? WHERE workflow_uuid = ?", testVersion+"+star12", testRun)
	require.NoError(t, err)

	expireLease(harness)

	client := harness.client(t, testVersion)
	require.NotEqual(t, "star12", stepListOf(client.version))

	err = client.ResumeOnThisBuild(testItem)

	require.ErrorIs(t, err, errStranded)
	require.ErrorContains(t, err, "star12")
	require.ErrorContains(t, err, stepListOf(client.version))
	require.ErrorContains(t, err, "Start the item again")

	infos, infoErr := client.Status()
	require.NoError(t, infoErr)
	assert.True(t, infos[0].OlderSteps, "the run is the same build with an older step list")
	assert.Equal(t, testVersion, infos[0].Version)
}

// sqlOf opens the state database of the harness for a test that changes a stored row.
func sqlOf(t *testing.T, h *harness) *sql.DB {
	t.Helper()

	db, err := sql.Open("sqlite", filepath.Join(h.opts.StateDir, databaseFile)+"?_pragma=busy_timeout(30000)")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	return db
}

func mustNewest(t *testing.T, c *Client) run {
	t.Helper()

	last, err := c.newest(testItem)
	require.NoError(t, err)

	return last
}

func TestClient_LeaseOfARunThatRecordedNoClaimIsEmpty(t *testing.T) {
	t.Parallel()

	harness, _, other := strandedHarness(t)
	_, err := sqlOf(t, harness).ExecContext(t.Context(), "UPDATE operation_outputs SET function_name = 'other' WHERE workflow_uuid = ? AND function_name = ?", testRun, string(model.StepClaim))
	require.NoError(t, err)

	owner, ends, leaseErr := other.leaseOf(mustNewest(t, other))

	require.NoError(t, leaseErr)
	assert.Empty(t, owner)
	assert.True(t, ends.IsZero())
}

func TestClient_ResumeOnThisBuildReportsAClaimLeaseThatCannotBeRead(t *testing.T) {
	t.Parallel()

	harness, _, other := strandedHarness(t)
	harness.beats.failRenewed(errFake)

	require.ErrorContains(t, other.ResumeOnThisBuild(testItem), "read the claim lease of "+testRun)
}

func TestClient_LeaseOfARunWhoseInputCannotBeReadFails(t *testing.T) {
	t.Parallel()

	_, _, other := strandedHarness(t)
	last := mustNewest(t, other)
	last.Status.Input = "not json"

	_, _, err := other.leaseOf(last)

	require.ErrorContains(t, err, "read the input of run "+testRun)
}

func TestClient_ReopenAgainRewindsACanceledRunOntoThisBuild(t *testing.T) {
	t.Parallel()

	_, _, other := strandedHarness(t)
	require.NoError(t, other.cancelRun(testItem, mustNewest(t, other)))

	row, err := other.row(testRun)
	require.NoError(t, err)
	require.NoError(t, other.reopenAgain(row, dbos.WithRewindApplicationVersion(other.version)))

	row, err = other.row(testRun)
	require.NoError(t, err)
	assert.Equal(t, other.version, row.ApplicationVersion)
}

func TestVersionParts_SplitTheBuildFromTheStepList(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "v1", buildVersion("v1+star12"))
	assert.Equal(t, "star12", stepListOf("v1+star12"))
	assert.Equal(t, "v1", buildVersion("v1"))
	assert.Empty(t, stepListOf("v1"))
	assert.Equal(t, "v1+rc1", buildVersion("v1+rc1+star13"))
}
