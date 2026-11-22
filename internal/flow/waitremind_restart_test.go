package flow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countIn is how many times text appears in the log file of the state folder.
func countIn(t *testing.T, h *harness, text string) int {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(h.opts.StateDir, logFileName))
	require.NoError(t, err)

	return strings.Count(string(data), text)
}

func TestWaitRemind_ARestartNeitherRepeatsASentReminderNorLosesAnOwedOne(t *testing.T) {
	t.Parallel()

	clk := newRemindClock(time.Now())
	h := newHarness(t)
	h.flowIs(t, remindFlow)
	deps := h.deps()
	deps.Clock.Reading, deps.Clock.OwnerWait = clk.now, 10*time.Millisecond

	first, err := NewEngine(t.Context(), h.opts, deps)
	require.NoError(t, err)
	require.NoError(t, first.Launch())
	t.Cleanup(func() { require.NoError(t, first.Shutdown()) })

	client := h.client(t, testVersion)
	done := runInBackground(first, testItem)

	waitOn(t, waiting[struct{}]{what: "the run to show its next reminder", run: runOfEngine(t, first), read: func() (struct{}, bool, error) {
		a := askedNow(t, client)

		return struct{}{}, a != nil && !a.NextRemind.IsZero(), nil
	}})
	clk.advance(35 * time.Minute)
	waitOn(t, waiting[struct{}]{what: "the run to send one reminder", run: runOfEngine(t, first), read: func() (struct{}, bool, error) { return struct{}{}, remindedIs(t, client, 1), nil }})

	require.NoError(t, first.Shutdown())
	require.Error(t, receive(t, done).err, "the stopped engine ends the run and leaves it open")

	// The second reminder falls due while no engine runs.
	clk.advance(20 * time.Minute)

	second := engineOn(t, h, deps)
	handle, err := dbos.RetrieveWorkflow[ItemResult](second.ctx, testRun)
	require.NoError(t, err, "the launch of the second engine recovers the open run")

	waitOn(t, waiting[struct{}]{what: "the run to send two reminders", run: runOfEngine(t, second), read: func() (struct{}, bool, error) { return struct{}{}, remindedIs(t, client, 2), nil }})

	require.NoError(t, client.Reply(testItem, Reply{From: "ana", Text: "yes"}))

	_, err = resultOfHandle(t, handle)
	require.NoError(t, err)

	assert.Equal(t, []string{remindStepPrefix + "1", remindStepPrefix + "2"}, remindSteps(t, client))
	assert.Equal(t, 1, countIn(t, h, "reminder 1 of 2"), "a reminder sent before the stop is not sent again")
	assert.Equal(t, 1, countIn(t, h, "reminder 2 of 2"), "a reminder owed at the restart is sent once")
}
