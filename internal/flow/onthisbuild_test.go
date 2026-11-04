package flow

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const otherBuild = "v2"

// strandedHarness starts a run on the build testVersion and leaves it open at the question of the owner. The returned client is another build.
func strandedHarness(t *testing.T) (*harness, *Engine, *Client) {
	t.Helper()

	harness := newHarness(t)
	harness.flowIs(t, waitFlow)
	engine := harness.engine(t)

	runInBackground(engine, testItem)
	waitForTheOwner(t, harness)

	return harness, engine, harness.client(t, otherBuild)
}

func TestClient_ResumeOnThisBuildMovesAnOpenRunOfAnotherBuild(t *testing.T) {
	t.Parallel()

	harness, _, other := strandedHarness(t)

	err := other.Resume(testItem)
	require.ErrorIs(t, err, errStranded)
	require.ErrorContains(t, err, "beehave resume 12 --on-this-build", "the refusal says that the flag exists")

	expireLease(harness) // the holder of the run is gone: its lease in the store has ended

	require.NoError(t, other.ResumeOnThisBuild(testItem))

	row, err := other.row(testRun)
	require.NoError(t, err)
	assert.Equal(t, other.version, row.ApplicationVersion, "the same run id is now stored under this build")

	infos, err := other.Status()
	require.NoError(t, err)
	assert.False(t, infos[0].Stranded)
}

func TestClient_ResumeOnThisBuildMovesACancelledRunOfAnotherBuild(t *testing.T) {
	t.Parallel()

	harness, _, other := strandedHarness(t)
	require.NoError(t, harness.client(t, testVersion).Cancel(testItem))

	require.NoError(t, other.ResumeOnThisBuild(testItem))

	row, err := other.row(testRun)
	require.NoError(t, err)
	assert.Equal(t, other.version, row.ApplicationVersion)
}

func TestClient_ResumeOnThisBuildRefusesAChangedWorkflowFileAndNamesIt(t *testing.T) {
	t.Parallel()

	harness, _, other := strandedHarness(t)
	harness.flowIs(t, waitFlow+"\n# edited\n")

	err := other.ResumeOnThisBuild(testItem)

	require.ErrorContains(t, err, defaultFlowName)
	require.ErrorContains(t, err, "changed since the run started")

	row, rowErr := other.row(testRun)
	require.NoError(t, rowErr)
	assert.Equal(t, testVersion, buildVersion(row.ApplicationVersion), "a refused run stays with the build that made it")
	assert.NotEqual(t, dbos.WorkflowStatusCancelled, row.Status, "a refused run is not canceled")
}

func TestClient_ResumeOnThisBuildOnTheBuildThatMadeTheRunIsAnOrdinaryResume(t *testing.T) {
	t.Parallel()

	harness, _, _ := strandedHarness(t)
	same := harness.client(t, testVersion)
	require.NoError(t, same.Cancel(testItem))

	require.NoError(t, same.ResumeOnThisBuild(testItem))
}

func TestClient_APlainResumeOrRewindKeepsTheBuildOfTheRun(t *testing.T) {
	t.Parallel()

	harness, _, other := strandedHarness(t)
	require.NoError(t, harness.client(t, testVersion).Cancel(testItem))

	require.ErrorIs(t, other.Resume(testItem), errStranded, "a plain resume of a run of another build still refuses")

	require.NoError(t, other.rewind(testRun, 0), "a rewind that no pin check came before")

	row, err := other.row(testRun)
	require.NoError(t, err)
	assert.Equal(t, testVersion, buildVersion(row.ApplicationVersion), "the run stays with the build that made it")
}

func TestClient_AResumedRunRestartsUnderItsPinnedBytesAndFailsOnAChangedFile(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.flowIs(t, waitFlow)
	engine := harness.engine(t)
	client := harness.client(t, testVersion)

	runInBackground(engine, testItem)
	waitForTheOwner(t, harness)
	require.NoError(t, client.Cancel(testItem))

	file := filepath.Join(filepath.Dir(harness.opts.StateDir), defaultFlowName)
	require.NoError(t, os.WriteFile(file, []byte(waitFlow+"\n# edited\n"), 0o600))
	require.NoError(t, client.Resume(testItem), "resume reopens the run and does not read the file")

	handle, err := dbos.RetrieveWorkflow[ItemResult](engine.ctx, testRun)
	require.NoError(t, err)

	_, err = handle.GetResult()
	require.ErrorContains(t, err, "changed since the run started", "the replay refuses a workflow file other than the pinned one")
}
