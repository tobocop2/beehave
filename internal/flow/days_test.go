package flow

import (
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/agent"
)

// dayAt is a time on a given day of November 2026 in the zone of the test.
func dayAt(zone *time.Location, day, hour, minute int) time.Time {
	return time.Date(2026, 11, day, hour, minute, 0, 0, zone)
}

func TestTallyDays_RunsOnTwoDaysAreCountedOnTheirOwnDay(t *testing.T) {
	t.Parallel()

	since := dayAt(time.UTC, 2, 0, 0)
	runs := []DayRun{
		{Started: dayAt(time.UTC, 2, 9, 0), Ended: dayAt(time.UTC, 2, 10, 0), State: runDone, Spent: agent.Spend{Cost: 1}},
		{Started: dayAt(time.UTC, 3, 9, 0), Ended: dayAt(time.UTC, 3, 9, 30), State: runFailed, Spent: agent.Spend{Cost: 0.5}},
		{Started: dayAt(time.UTC, 3, 11, 0), Ended: dayAt(time.UTC, 3, 11, 5), State: runCancelled},
		{Started: dayAt(time.UTC, 3, 12, 0), State: runRunning},
	}

	assert.Equal(t, []DayFigures{
		{Day: "2026-11-02", Started: 1, Done: 1, Spent: agent.Spend{Cost: 1}},
		{Day: "2026-11-03", Started: 3, Failed: 1, Canceled: 1, Spent: agent.Spend{Cost: 0.5}},
	}, TallyDays(runs, since))
}

func TestTallyDays_ARunThatSpansMidnightCountsOnItsStartDayAndItsEndDay(t *testing.T) {
	t.Parallel()

	since := dayAt(time.UTC, 2, 0, 0)
	runs := []DayRun{{Started: dayAt(time.UTC, 2, 23, 50), Ended: dayAt(time.UTC, 3, 0, 10), State: runDone, Spent: agent.Spend{Cost: 2}}}

	assert.Equal(t, []DayFigures{
		{Day: "2026-11-02", Started: 1},
		{Day: "2026-11-03", Done: 1, Spent: agent.Spend{Cost: 2}},
	}, TallyDays(runs, since), "the spend counts once, on the end day")
}

func TestTallyDays_AnOpenRunSpendsOnItsStartDay(t *testing.T) {
	t.Parallel()

	since := dayAt(time.UTC, 2, 0, 0)
	runs := []DayRun{{Started: dayAt(time.UTC, 2, 23, 50), State: runRunning, Spent: agent.Spend{Cost: 3}}}

	assert.Equal(t, []DayFigures{{Day: "2026-11-02", Started: 1, Spent: agent.Spend{Cost: 3}}}, TallyDays(runs, since))
}

func TestTallyDays_TheEstimateAndUnknownMarksCarryIntoTheDaySum(t *testing.T) {
	t.Parallel()

	since := dayAt(time.UTC, 3, 0, 0)
	runs := []DayRun{
		{Started: dayAt(time.UTC, 3, 1, 0), Ended: dayAt(time.UTC, 3, 2, 0), State: runDone, Spent: agent.Spend{Cost: 1}},
		{Started: dayAt(time.UTC, 3, 3, 0), Ended: dayAt(time.UTC, 3, 4, 0), State: runDone, Spent: agent.Spend{Cost: 0.5, Estimated: true}},
		{Started: dayAt(time.UTC, 3, 5, 0), Ended: dayAt(time.UTC, 3, 6, 0), State: runDone, Spent: agent.Spend{Unknown: true}},
	}

	days := TallyDays(runs, since)

	require.Len(t, days, 1)
	assert.Equal(t, agent.Spend{Cost: 1.5, Estimated: true, Unknown: true}, days[0].Spent)
	assert.Equal(t, "2026-11-03\tstarted 3\tdone 3, failed 0, canceled 0\tspent $1.5 (estimate, some cost unknown)", days[0].Text())
}

func TestTallyDays_ADayThatOnlyHasUnknownCostSaysSo(t *testing.T) {
	t.Parallel()

	days := TallyDays([]DayRun{{Started: dayAt(time.UTC, 3, 1, 0), Ended: dayAt(time.UTC, 3, 2, 0), State: runDone, Spent: agent.Spend{Unknown: true}}}, dayAt(time.UTC, 3, 0, 0))

	require.Len(t, days, 1)
	assert.Equal(t, "2026-11-03\tstarted 1\tdone 1, failed 0, canceled 0\tcost unknown", days[0].Text())
}

func TestTallyDays_TheDayIsTheLocalDayOfTheWindow(t *testing.T) {
	t.Parallel()

	zone := time.FixedZone("plus9", 9*60*60)
	since := dayAt(zone, 3, 0, 0)

	// 20:00 UTC on the 2nd is 05:00 on the 3rd in the zone.
	runs := []DayRun{{Started: dayAt(time.UTC, 2, 20, 0), Ended: dayAt(time.UTC, 2, 21, 0), State: runDone}}

	assert.Equal(t, []DayFigures{{Day: "2026-11-03", Started: 1, Done: 1}}, TallyDays(runs, since))
}

func TestTallyDays_AStartBeforeTheWindowIsNotCountedButItsEndIs(t *testing.T) {
	t.Parallel()

	since := dayAt(time.UTC, 3, 0, 0)
	runs := []DayRun{{Started: dayAt(time.UTC, 2, 23, 0), Ended: dayAt(time.UTC, 3, 1, 0), State: runDone, Spent: agent.Spend{Cost: 1}}}

	assert.Equal(t, []DayFigures{{Day: "2026-11-03", Done: 1, Spent: agent.Spend{Cost: 1}}}, TallyDays(runs, since))
}

func TestTallyDays_AnEmptyWindowHasNoDay(t *testing.T) {
	t.Parallel()

	assert.Empty(t, TallyDays(nil, dayAt(time.UTC, 3, 0, 0)))
}

func TestTallyDays_AnEndedRunWithNoOutcomeStateCountsNoOutcome(t *testing.T) {
	t.Parallel()

	runs := []DayRun{{Started: dayAt(time.UTC, 3, 1, 0), Ended: dayAt(time.UTC, 3, 2, 0), State: runStopped}}

	assert.Equal(t, []DayFigures{{Day: "2026-11-03", Started: 1}}, TallyDays(runs, dayAt(time.UTC, 3, 0, 0)))
}

func TestClientDays_ReportsAStoreThatIsGoneAndARunWhoseStepsCannotBeRead(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	client, err := OpenClient(t.Context(), harness.opts, ReleaseDeps{})
	require.NoError(t, err)
	require.NoError(t, client.Close())

	_, err = client.Days(dayAt(time.UTC, 3, 0, 0))
	require.Error(t, err)

	_, err = client.dayRun(dbos.WorkflowStatus{ID: "gone"})
	require.Error(t, err)
}

func TestClientDays_ARunThatStartedBeforeTheWindowAndEndedInItCountsOnItsEndDay(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	failed := estimated(0.75)
	failed.ExitCode = 1
	rig.reply("", failed)

	_, err := rig.run(t, twoAgentsFlow, "x")
	require.Error(t, err)

	client := rig.h.client(t, testVersion)

	rows, err := dbos.ListWorkflows(client.flow, dbos.WithFilterName(itemName))
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.True(t, rows[0].CreatedAt.Before(rows[0].CompletedAt), "the run takes time, so a window can open between its start and its end")

	days, err := client.Days(rows[0].CompletedAt)
	require.NoError(t, err)
	require.Len(t, days, 1)
	assert.Zero(t, days[0].Started, "the start is before the window")
	assert.Equal(t, 1, days[0].Failed)
	assert.InDelta(t, 0.75, days[0].Spent.Cost, 0)
}

func TestDaysSince_TheWindowStartsAtLocalMidnightOfTheFirstDay(t *testing.T) {
	t.Parallel()

	now := dayAt(time.UTC, 3, 10, 30)

	assert.Equal(t, dayAt(time.UTC, 3, 0, 0), DaysSince(now, 1))
	assert.Equal(t, dayAt(time.UTC, 1, 0, 0), DaysSince(now, 3))
}

func TestClientDays_ReadsTheRunsOfTheWindowWithTheirSpend(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	failed := estimated(0.75)
	failed.ExitCode = 1
	rig.reply("", failed)

	_, err := rig.run(t, twoAgentsFlow, "x")
	require.Error(t, err)

	client := rig.h.client(t, testVersion)
	now := time.Now()

	days, err := client.Days(DaysSince(now, 1))
	require.NoError(t, err)
	require.Len(t, days, 1)
	assert.Equal(t, 1, days[0].Started)
	assert.Equal(t, 1, days[0].Failed)
	assert.InDelta(t, 0.75, days[0].Spent.Cost, 0)
	assert.True(t, days[0].Spent.Estimated)

	later, err := client.Days(now.Add(time.Hour))
	require.NoError(t, err)
	assert.Empty(t, later, "a window after the run reads no run")
}
