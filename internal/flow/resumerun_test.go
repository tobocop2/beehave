package flow

import (
	"testing"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// failedItemWithTwoAttempts returns a client of this build and the harness, after item 81 failed twice: the first attempt is canceled and the second one failed.
func failedItemWithTwoAttempts(t *testing.T) (*harness, *Client) {
	t.Helper()

	return failedWorld(t, []failedRunSpec{{"81", "p", "ERROR", failedInside}}, "81")
}

func TestResumeRun_ReturnsTheIdOfTheNewestAttemptThatItReopened(t *testing.T) {
	t.Parallel()

	_, c := failedItemWithTwoAttempts(t)

	id, err := c.ResumeRun("81", false)
	require.NoError(t, err)

	assert.Equal(t, "item-81-r2", id, "the resume reopens the newest attempt, and not the run that an earlier list named")
}

func TestResumeRun_ReturnsTheIdOfTheRunThatTheMoveToThisBuildReopened(t *testing.T) {
	t.Parallel()

	h, _ := failedItemWithTwoAttempts(t)

	id, err := h.client(t, otherBuild).ResumeRun("81", true)
	require.NoError(t, err)

	assert.Equal(t, "item-81-r2", id)
}

func TestResumeRun_ReturnsNoIdForARunThatItRefuses(t *testing.T) {
	t.Parallel()

	h, c := failedWorld(t, []failedRunSpec{{"82", "p", "SUCCESS", failedInside}})

	for here, client := range map[bool]*Client{false: c, true: h.client(t, otherBuild)} {
		id, err := client.ResumeRun("82", here)

		require.ErrorIs(t, err, ErrWrongState)
		assert.Empty(t, id)
	}
}

func TestResumeRun_AMoveSkipsAnOpenRunThatAnotherBuildChangedAfterTheMoveReadIt(t *testing.T) {
	t.Parallel()

	h, _, other := strandedHarness(t)
	expireLease(h)

	// The hook runs while the move checks the lease, after it read the run and before it takes probe.lock.
	h.beats.hookRenewed(func() {
		storeExec(t, h, "UPDATE workflow_status SET application_version = 'v3' WHERE workflow_uuid = '"+testRun+"'")
	})

	id, err := other.ResumeRun(testItem, true)

	require.ErrorIs(t, err, ErrWrongState, "a run of another build that changed since the read is skipped, not moved")
	require.ErrorContains(t, err, "the move read it as")
	assert.Empty(t, id)

	row, rowErr := other.row(testRun)
	require.NoError(t, rowErr)
	assert.Equal(t, "v3", row.ApplicationVersion, "the run stays with the build that changed it")
	assert.NotEqual(t, dbos.WorkflowStatusCancelled, row.Status, "the move canceled nothing")
}

func TestResumeRun_AMoveSkipsAnOpenRunWhoseStatusAloneChangedAfterTheMoveReadIt(t *testing.T) {
	t.Parallel()

	h, _, other := strandedHarness(t)
	expireLease(h)

	// The hook runs after the move read the run and before it takes probe.lock. It changes the status of the run and nothing else.
	h.beats.hookRenewed(func() {
		storeExec(t, h, "UPDATE workflow_status SET status = 'CANCELLED' WHERE workflow_uuid = '"+testRun+"'")
	})

	before, err := other.row(testRun)
	require.NoError(t, err)

	id, err := other.ResumeRun(testItem, true)

	require.ErrorIs(t, err, ErrWrongState, "a run whose status changed since the read is skipped, not moved")
	require.ErrorContains(t, err, testRun, "the error names the run")
	require.ErrorContains(t, err, "the move read it as")
	assert.Empty(t, id)

	row, rowErr := other.row(testRun)
	require.NoError(t, rowErr)
	assert.Equal(t, before.ApplicationVersion, row.ApplicationVersion, "the move did not reopen the run on this build")
}

func TestRequireUnchanged_ARunThatIsAnotherAttemptOfTheItemIsRefusedWhenStatusAndVersionAreEqual(t *testing.T) {
	t.Parallel()

	_, c := failedItemWithTwoAttempts(t)

	first, err := c.row("item-81-r1")
	require.NoError(t, err)

	newest, err := c.newest("81")
	require.NoError(t, err)
	require.Equal(t, "item-81-r2", newest.Status.ID)
	require.Equal(t, first.Status, newest.Status.Status, "both attempts have the same status")
	require.Equal(t, first.ApplicationVersion, newest.Status.ApplicationVersion, "both attempts have the same version")

	require.ErrorIs(t, c.requireUnchanged("81", run{Status: first}), ErrWrongState, "the newest run is not the run that the move read")
	require.NoError(t, c.requireUnchanged("81", newest))
}

func TestRequireUnchanged_ARereadThatFailsIsReturned(t *testing.T) {
	t.Parallel()

	_, c := failedWorld(t, []failedRunSpec{{"85", "p", "ERROR", failedInside}})

	read, err := c.newest("85")
	require.NoError(t, err)

	// The second read of the item fails: the item id is not valid.
	require.ErrorIs(t, c.requireUnchanged("no way", read), ErrBadItem)
}

func TestResumeRun_AMoveSkipsAFailedRunThatAnotherBuildChangedAfterTheMoveReadIt(t *testing.T) {
	t.Parallel()

	h, _ := failedWorld(t, []failedRunSpec{{"83", "p", "ERROR", failedInside}})
	other := h.client(t, otherBuild)

	// The move has read and accepted the run. Another build changes it before the move takes probe.lock, so the test hands the move the run as it read it.
	read, err := other.newest("83")
	require.NoError(t, err)
	require.NoError(t, other.requireMovable(t.Context(), "83", read))

	storeExec(t, h, "UPDATE workflow_status SET application_version = 'v3' WHERE workflow_uuid = 'item-83-r1'")

	err = other.reopenHere("83", read)

	require.ErrorIs(t, err, ErrWrongState)

	row, rowErr := other.row("item-83-r1")
	require.NoError(t, rowErr)
	assert.Equal(t, "v3", row.ApplicationVersion, "the run stays with the build that changed it")
}
