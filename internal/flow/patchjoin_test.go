package flow

import (
	"os"
	"sync"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// otherBuildRun leaves an open run of the old build with an expired lease and returns the harness and the workflow file.
func otherBuildRun(t *testing.T) (*harness, string) {
	t.Helper()

	h := newHarness(t)
	seen := &probes{}

	h.opts.Modules = probeModules(oldProbe, seen)
	file := writeFlow(t, probeFlow)
	interrupted(t, h, file)
	expireLease(h)

	h.opts.Modules = probeModules(newProbeBehindPatch, seen)
	h.opts.Version = otherBuild

	return h, file
}

// notCanceled reads the run of the item and requires that it is open.
func notCanceled(t *testing.T, c *Client) run {
	t.Helper()

	last, err := c.newest(testItem)
	require.NoError(t, err)
	require.False(t, last.terminal(), "the run is still open: %s", last.Status.Status)

	return last
}

func TestJoin_ARunWhoseWorkflowFileChangedIsRefusedUnderRunAndIsNotCanceled(t *testing.T) {
	t.Parallel()

	h, file := otherBuildRun(t)
	require.NoError(t, os.WriteFile(file, []byte(probeFlow+"# changed\n"), 0o600))

	engine := h.engine(t)
	client := h.client(t, otherBuild)

	_, err := runFileWithin(t, engine, file)

	require.ErrorContains(t, err, "changed since the run started")
	assert.Equal(t, testVersion, buildVersion(notCanceled(t, client).Status.ApplicationVersion), "the run stays on the old build")
}

func TestJoin_ARunWhoseWorkflowFileChangedIsRefusedUnderServeAndIsNotCanceled(t *testing.T) {
	t.Parallel()

	h, file := otherBuildRun(t)
	require.NoError(t, os.WriteFile(file, []byte(probeFlow+"# changed\n"), 0o600))
	h.engine(t)

	client := h.client(t, otherBuild)

	_, err := client.JoinRun(t.Context(), testItem, testProject, file)

	require.ErrorContains(t, err, "changed since the run started")
	assert.Equal(t, testVersion, buildVersion(notCanceled(t, client).Status.ApplicationVersion), "the run stays on the old build")
}

func TestJoin_ASecondMoverThatReadAnOldPlanFindsTheRunOnThisBuildAndMovesNothing(t *testing.T) {
	t.Parallel()

	h, _ := otherBuildRun(t)
	first, second := h.client(t, otherBuild), h.client(t, otherBuild)
	plan := attemptPlan{Join: testRun, Move: true}

	require.NoError(t, first.moveIfOtherBuild(t.Context(), plan, testItem))
	assert.Equal(t, first.version, notCanceled(t, first).Status.ApplicationVersion)

	h.beats.setRenewed(time.Now()) // the lease is live now, as the claim of the first mover is

	require.NoError(t, second.moveIfOtherBuild(t.Context(), plan, testItem), "the run is on this build, so the second mover has nothing to move and must not say that another build still runs it")
	assert.Equal(t, first.version, notCanceled(t, second).Status.ApplicationVersion)
}

func TestJoin_TwoMoversAtOnceMoveTheRunOnceAndBothFindItOnThisBuild(t *testing.T) {
	t.Parallel()

	h, _ := otherBuildRun(t)
	clients := []*Client{h.client(t, otherBuild), h.client(t, otherBuild)}
	plan := attemptPlan{Join: testRun, Move: true}
	errs := make([]error, len(clients))

	var wg sync.WaitGroup

	for i, c := range clients {
		wg.Go(func() { errs[i] = c.moveIfOtherBuild(t.Context(), plan, testItem) })
	}

	wg.Wait()

	require.NoError(t, errs[0])
	require.NoError(t, errs[1])

	last := notCanceled(t, clients[0])
	assert.Equal(t, clients[0].version, last.Status.ApplicationVersion)
	assert.NotEqual(t, dbos.WorkflowStatusCancelled, last.Status.Status)
}

func TestJoin_AMoveWhoseCancelFailsReturnsTheFailure(t *testing.T) {
	t.Parallel()

	h, _ := otherBuildRun(t)
	client := h.client(t, otherBuild)
	h.life.failing["release"] = true

	err := client.moveIfOtherBuild(t.Context(), attemptPlan{Join: testRun, Move: true}, testItem)

	require.Error(t, err)
	require.NotContains(t, err.Error(), "beehave resume", "the cancel failed before any reopen, so the error does not offer a retry of the reopen")
}

func TestJoin_AMoveWhoseClaimFailsSaysThatTheRunIsCanceledAndHowToRetry(t *testing.T) {
	t.Parallel()

	h, _ := otherBuildRun(t)
	client := h.client(t, otherBuild)
	h.life.failing["claim"] = true

	err := client.moveIfOtherBuild(t.Context(), attemptPlan{Join: testRun, Move: true}, testItem)

	require.ErrorContains(t, err, "is now canceled")
	require.ErrorContains(t, err, "beehave resume "+testItem+" --on-this-build again")
}

func TestJoin_ARunThatEndedBeforeTheMoveIsNotReopened(t *testing.T) {
	t.Parallel()

	h, _ := otherBuildRun(t)
	client := h.client(t, otherBuild)
	plan := attemptPlan{Join: testRun, Move: true}

	require.NoError(t, h.client(t, testVersion).Cancel(testItem))

	err := client.moveIfOtherBuild(t.Context(), plan, testItem)

	require.ErrorIs(t, err, errWrongState)
	require.ErrorContains(t, err, "ended before it could move")

	last, newestErr := client.newest(testItem)
	require.NoError(t, newestErr)
	assert.Equal(t, dbos.WorkflowStatusCancelled, last.Status.Status, "the run the owner canceled stays canceled")
}
