package flow

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// failedRunSpec is one run of a failed-runs world: the status and end time that the store holds for it after the real run.
type failedRunSpec struct {
	item, project, status string
	ended                 time.Time
}

var (
	failedSince  = time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	failedUntil  = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	failedInside = failedSince.Add(time.Hour)
)

// failedWorld runs each item once with a failing agent and then sets the status, the end time and the project that the spec names, with the SQL of the store.
// Each item in again gets a second attempt first: its failed run is canceled and the item runs and fails once more.
func failedWorld(t *testing.T, specs []failedRunSpec, again ...string) (*harness, *Client) {
	t.Helper()

	h := newHarness(t)
	engine := h.engine(t)
	h.agents.err = errFake

	for _, spec := range specs {
		_, err := engine.Run(spec.item, spec.project)
		require.Error(t, err, "the real run of item %s fails", spec.item)
	}

	require.NoError(t, engine.Shutdown())

	for _, item := range again {
		require.NoError(t, h.client(t, testVersion).Cancel(item))

		second := h.engine(t)
		_, err := second.Run(item, "p")
		require.Error(t, err, "the second attempt of item %s fails", item)
		require.NoError(t, second.Shutdown())
	}

	for _, spec := range specs {
		storeExec(t, h, fmt.Sprintf("UPDATE workflow_status SET status = '%s', completed_at = %d WHERE workflow_uuid = 'item-%s-r1'", spec.status, spec.ended.UnixMilli(), spec.item))
	}

	return h, h.client(t, testVersion)
}

func failedIDs(runs []FailedRun) []string {
	ids := make([]string, 0, len(runs))
	for _, r := range runs {
		ids = append(ids, r.ID)
	}

	return ids
}

func TestFailedRuns_PicksFailedRunsInTheClosedRange(t *testing.T) {
	t.Parallel()

	_, c := failedWorld(t, []failedRunSpec{
		{"21", "p", "ERROR", failedInside},
		{"22", "p", "ERROR", failedSince.Add(-time.Second)},
		{"23", "p", "ERROR", failedUntil.Add(time.Second)},
		{"24", "p", "ERROR", failedSince},
		{"25", "p", "ERROR", failedUntil},
	})

	got, err := c.FailedRuns(FailedFilter{Since: failedSince, Until: failedUntil})
	require.NoError(t, err)
	assert.Equal(t, []string{"item-24-r1", "item-21-r1", "item-25-r1"}, failedIDs(got), "both ends are inside the range, and the order is the end time")
	assert.Equal(t, "21", got[1].Item)
	assert.Equal(t, failedInside, got[1].Ended)

	all, err := c.FailedRuns(FailedFilter{})
	require.NoError(t, err)
	assert.Len(t, all, 5, "no bound picks every failed run")

	from, err := c.FailedRuns(FailedFilter{Since: failedUntil})
	require.NoError(t, err)
	assert.Equal(t, []string{"item-25-r1", "item-23-r1"}, failedIDs(from), "a start alone has no upper bound")

	to, err := c.FailedRuns(FailedFilter{Until: failedSince})
	require.NoError(t, err)
	assert.Equal(t, []string{"item-22-r1", "item-24-r1"}, failedIDs(to), "an end alone has no lower bound")
}

func TestFailedRuns_LeavesOutEndedOpenCancelledAndOlderAttempts(t *testing.T) {
	t.Parallel()

	_, c := failedWorld(t, []failedRunSpec{
		{"31", "p", "ERROR", failedInside},
		{"32", "p", "SUCCESS", failedInside},
		{"33", "p", "PENDING", failedInside},
		{"34", "p", "CANCELLED", failedInside}, //nolint:misspell // the DBOS status literal
		{"35", "p", "ERROR", failedInside},
	}, "35")

	got, err := c.FailedRuns(FailedFilter{})
	require.NoError(t, err)
	assert.Equal(t, []string{"item-31-r1", "item-35-r2"}, failedIDs(got), "35-r1 failed too, but resume acts on the newest attempt of an item, which is 35-r2")
}

func TestFailedRuns_AMaxRecoveryRunIsFailed(t *testing.T) {
	t.Parallel()

	_, c := failedWorld(t, []failedRunSpec{{"41", "p", "MAX_RECOVERY_ATTEMPTS_EXCEEDED", failedInside}})

	got, err := c.FailedRuns(FailedFilter{})
	require.NoError(t, err)
	assert.Equal(t, []string{"item-41-r1"}, failedIDs(got))
}

func TestFailedRuns_ProjectAndLimitNarrowTheList(t *testing.T) {
	t.Parallel()

	_, c := failedWorld(t, []failedRunSpec{
		{"51", "p", "ERROR", failedInside.Add(2 * time.Minute)},
		{"52", "q", "ERROR", failedInside.Add(time.Minute)},
		{"53", "p", "ERROR", failedInside},
	})

	got, err := c.FailedRuns(FailedFilter{Project: "q"})
	require.NoError(t, err)
	assert.Equal(t, []string{"item-52-r1"}, failedIDs(got))

	got, err = c.FailedRuns(FailedFilter{Limit: 2})
	require.NoError(t, err)
	assert.Equal(t, []string{"item-53-r1", "item-52-r1"}, failedIDs(got), "the limit keeps the runs that ended first")

	got, err = c.FailedRuns(FailedFilter{Project: "p", Limit: 1})
	require.NoError(t, err)
	assert.Equal(t, []string{"item-53-r1"}, failedIDs(got), "the project narrows before the limit")

	_, err = c.FailedRuns(FailedFilter{Limit: -1})
	require.ErrorIs(t, err, errBadFailedFilter)
	assert.Contains(t, err.Error(), "limit")

	_, err = c.FailedRuns(FailedFilter{Since: failedUntil, Until: failedSince})
	require.ErrorIs(t, err, errBadFailedFilter)
	assert.Contains(t, err.Error(), "Swap")
}

// claimState reads every run row and the claim calls, so that a read that changes any of them shows.
func claimState(t *testing.T, h *harness) string {
	t.Helper()

	rows, err := sqlOf(t, h).QueryContext(t.Context(), "SELECT workflow_uuid, status, COALESCE(completed_at, 0), updated_at, COALESCE(application_version, '') FROM workflow_status ORDER BY workflow_uuid")
	require.NoError(t, err)

	defer func() { require.NoError(t, rows.Close()) }()

	var out strings.Builder

	for rows.Next() {
		var id, status, version string

		var completed, updated int64

		require.NoError(t, rows.Scan(&id, &status, &completed, &updated, &version))
		fmt.Fprintf(&out, "%s %s %d %d %s\n", id, status, completed, updated, version)
	}

	require.NoError(t, rows.Err())

	h.life.mu.Lock()
	defer h.life.mu.Unlock()

	fmt.Fprintf(&out, "claims %v released %v", h.life.takes, h.life.released)

	return out.String()
}

func TestFailedRuns_ADryRunWritesNothing(t *testing.T) {
	t.Parallel()

	h, c := failedWorld(t, []failedRunSpec{{"61", "p", "ERROR", failedInside}, {"62", "p", "ERROR", failedInside}})

	before := claimState(t, h)
	got, err := c.FailedRuns(FailedFilter{})
	require.NoError(t, err)
	require.Len(t, got, 2)

	assert.Equal(t, before, claimState(t, h), "the list changes no run row and takes no claim")
}

func TestFailedRuns_ReportsAStoreThatIsGone(t *testing.T) {
	t.Parallel()

	client, err := OpenClient(t.Context(), newHarness(t).opts, ReleaseDeps{})
	require.NoError(t, err)
	require.NoError(t, client.Close())

	_, err = client.FailedRuns(FailedFilter{})
	require.ErrorContains(t, err, "list the failed runs")
}

func TestFailedRuns_ReportsARunWhoseInputIsNotReadable(t *testing.T) {
	t.Parallel()

	h, c := failedWorld(t, []failedRunSpec{{"71", "p", "ERROR", failedInside}})
	storeExec(t, h, "UPDATE workflow_input SET inputs = '{' WHERE workflow_uuid = 'item-71-r1'")

	_, err := c.FailedRuns(FailedFilter{})
	require.ErrorContains(t, err, "read the input of run item-71-r1")
}
