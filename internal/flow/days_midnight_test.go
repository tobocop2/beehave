package flow

import (
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/agent"
)

// daysFromTheRun reads the per-day figures of the window that opens on the local day of the run in the store, so the run is in the window at any wall clock.
func daysFromTheRun(t *testing.T, client *Client) ([]DayFigures, error) {
	t.Helper()

	rows, err := dbos.ListWorkflows(client.flow, dbos.WithFilterName(itemName))
	require.NoError(t, err)
	require.Len(t, rows, 1)

	return client.Days(DaysSince(rows[0].CreatedAt, 1))
}

// totalOfDays adds the days. A run that spans midnight shows its start on one day and its end and its spend on the next.
func totalOfDays(days []DayFigures) DayFigures {
	var total DayFigures

	for _, day := range days {
		total.Started += day.Started
		total.Done += day.Done
		total.Failed += day.Failed
		total.Canceled += day.Canceled
		total.Spent = total.Spent.Plus(day.Spent)
	}

	return total
}

func TestClientDays_TheRunOfADayIsNotInTheWindowOfTheNextDay(t *testing.T) {
	t.Parallel()

	h := limitedHarness(t, limitReset)
	inTurn(h, limited(time.Time{}, 0.7))

	_, err := h.engine(t).Run(testItem, testProject)
	require.ErrorContains(t, err, "gives a reset time")

	client := h.client(t, testVersion)

	rows, err := dbos.ListWorkflows(client.flow, dbos.WithFilterName(itemName))
	require.NoError(t, err)
	require.Len(t, rows, 1)

	justAfterMidnight := DaysSince(rows[0].CompletedAt, 1).AddDate(0, 0, 1).Add(time.Minute)

	today, err := client.Days(DaysSince(justAfterMidnight, 1))
	require.NoError(t, err)
	assert.Empty(t, today, "the window of one day starts at the midnight before now, so the run of the day before is out of it")

	lastTwo, err := client.Days(DaysSince(justAfterMidnight, 2))
	require.NoError(t, err)
	assert.InDelta(t, 0.7, totalOfDays(lastTwo).Spent.Cost, 1e-9, "the window of two days holds it")
}

func TestTotalOfDays_AddsTheFiguresOfEveryDay(t *testing.T) {
	t.Parallel()

	days := []DayFigures{
		{Day: "2026-11-02", Started: 1, Spent: agent.Spend{Cost: 0.25}},
		{Day: "2026-11-03", Done: 1, Failed: 2, Canceled: 3, Spent: agent.Spend{Cost: 0.5}},
	}

	assert.Equal(t, DayFigures{Started: 1, Done: 1, Failed: 2, Canceled: 3, Spent: agent.Spend{Cost: 0.75}}, totalOfDays(days))
}
