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
	h.migrated(t)

	deps := h.deps()
	deps.Clock.Reading, deps.Clock.OwnerWait = clk.now, 10*time.Millisecond

	first, err := NewEngine(t.Context(), h.opts, deps)
	require.NoError(t, err)
	require.NoError(t, first.Launch())
	t.Cleanup(func() { require.NoError(t, first.Shutdown()) })

	client := h.client(t, testVersion)
	done := runInBackground(first, testItem)

	eventually(t, func() bool { a := askedNow(t, client); return a != nil && !a.NextRemind.IsZero() })
	clk.advance(35 * time.Minute)
	eventually(t, func() bool { return remindedIs(t, client, 1) })

	require.NoError(t, first.Shutdown())
	require.Error(t, (<-done).err, "the stopped engine ends the run and leaves it open")

	// The second reminder falls due while no engine runs.
	clk.advance(20 * time.Minute)

	second := engineOn(t, h, deps)
	handle, err := dbos.RetrieveWorkflow[ItemResult](second.ctx, testRun)
	require.NoError(t, err, "the launch of the second engine recovers the open run")

	eventually(t, func() bool { return remindedIs(t, client, 2) })

	require.NoError(t, client.AnswerAs(testItem, "ana", "yes"))

	_, err = joinRun(handle)
	require.NoError(t, err)

	assert.Equal(t, []string{remindStepPrefix + "1", remindStepPrefix + "2"}, remindSteps(t, client))
	assert.Equal(t, 1, countIn(t, h, "reminder 1 of 2"), "a reminder sent before the stop is not sent again")
	assert.Equal(t, 1, countIn(t, h, "reminder 2 of 2"), "a reminder owed at the restart is sent once")
}
