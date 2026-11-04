package flow

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var scheduleNow = time.Date(2026, 11, 3, 10, 30, 0, 0, time.UTC)

func TestScheduleInfo_ShowsTheLastTickInUTCAndFiresWhileAnEngineRuns(t *testing.T) {
	t.Parallel()

	last := scheduleNow.Add(-20 * time.Minute).In(time.FixedZone("x", 3600))
	got := scheduleInfo(dbos.WorkflowSchedule{ScheduleName: "review-poll", WorkflowName: "review_poll", Schedule: "@every 1h", LastFiredAt: &last}, true)

	require.NotNil(t, got.LastTick)
	assert.Equal(t, last.UTC(), *got.LastTick)
	assert.Equal(t, time.UTC, got.LastTick.Location())
	assert.True(t, got.Firing)
	assert.False(t, got.Paused)
	assert.Equal(t, "review-poll", got.Name)
	assert.Equal(t, "review_poll", got.Workflow)
	assert.Equal(t, "@every 1h", got.Schedule)
}

func TestScheduleInfo_AScheduleThatNeverFiredHasNoLastTick(t *testing.T) {
	t.Parallel()

	assert.Nil(t, scheduleInfo(dbos.WorkflowSchedule{Schedule: "@every 30m"}, true).LastTick)
}

func TestScheduleInfo_NothingFiresWhileNoEngineRuns(t *testing.T) {
	t.Parallel()

	assert.False(t, scheduleInfo(dbos.WorkflowSchedule{Schedule: "@every 1h"}, false).Firing)
}

func TestScheduleInfo_APausedScheduleDoesNotFire(t *testing.T) {
	t.Parallel()

	got := scheduleInfo(dbos.WorkflowSchedule{Schedule: "@every 1h", Status: dbos.ScheduleStatusPaused}, true)

	assert.True(t, got.Paused)
	assert.False(t, got.Firing)
}

func TestClient_SchedulesListsTheReviewPollAndSaysWhetherAnEngineFiresIt(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.opts.ReviewPoll = time.Hour
	harness.poll = fakePoll{}
	engine := launchedWith(t, harness, func(*Deps) {})
	client := harness.client(t, testVersion)

	got, err := client.Schedules()
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, pollScheduleName, got[0].Name)
	assert.Equal(t, pollName, got[0].Workflow)
	assert.Nil(t, got[0].LastTick, "no tick fired yet")
	assert.True(t, got[0].Firing, "an engine runs")

	require.NoError(t, engine.Shutdown())

	got, err = client.Schedules()
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.False(t, got[0].Firing, "no engine runs")
}

func TestClient_SchedulesIsEmptyWhenNoLaunchMadeOne(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)

	got, err := harness.client(t, testVersion).Schedules()

	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestClient_SchedulesReportsAnEngineLockThatCannotOpen(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	client := harness.client(t, testVersion)
	require.NoError(t, os.MkdirAll(filepath.Join(harness.opts.StateDir, engineLockName), 0o700))

	_, err := client.Schedules()

	require.ErrorContains(t, err, engineLockName)
}

func TestClient_SchedulesReportsAStoreThatIsGone(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	client, err := OpenClient(t.Context(), harness.opts, ReleaseDeps{})
	require.NoError(t, err)
	require.NoError(t, client.Close())

	_, err = client.Schedules()

	require.Error(t, err)
}
