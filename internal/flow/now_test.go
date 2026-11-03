package flow

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/require"
)

const nowFlow = "def main(item):\n    t = now()\n    wait(name = \"ask\", question = \"go?\")\n    fail(\"AT \" + t)\n"

// readingOf returns the time in the "AT <time>" text of a run error.
func readingOf(t *testing.T, err error) time.Time {
	t.Helper()

	require.Error(t, err)

	_, text, found := strings.Cut(err.Error(), "AT ")
	require.True(t, found, err.Error())

	reading, parseErr := time.Parse(time.RFC3339, strings.Fields(text)[0])
	require.NoError(t, parseErr)
	require.Equal(t, time.UTC, reading.Location())

	return reading
}

func TestNow_ReturnsTheWallClockAsRFC3339InUTC(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.flowIs(t, "def main(item):\n    fail(\"AT \" + now())\n")

	before := time.Now().Truncate(time.Second)
	outcome := <-runInBackground(h.engine(t), testItem)
	after := time.Now()

	reading := readingOf(t, outcome.err)

	require.False(t, reading.Before(before))
	require.False(t, reading.After(after))
}

func TestNow_TheRunRefusesAnArgument(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.flowIs(t, "def main(item):\n    now(\"utc\")\n")

	outcome := <-runInBackground(h.engine(t), testItem)

	require.ErrorContains(t, outcome.err, "now")
}

// A crash after now() and before the answer, then a resume more than a second later: the replay returns the first reading.
func TestNow_AReplayKeepsTheFirstReading(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	file := writeFlow(t, nowFlow)

	h.migrated(t)

	stopCtx, stop := context.WithCancel(t.Context())
	first, err := NewEngine(stopCtx, h.opts, h.deps())
	require.NoError(t, err)
	require.NoError(t, first.Launch())

	ended := make(chan error, 1)

	go func() {
		_, runErr := first.RunFile(testItem, testProject, file)
		ended <- runErr
	}()

	waitForTheOwner(t, h)

	steps, err := dbos.GetWorkflowSteps(first.ctx, testRun)
	require.NoError(t, err)
	require.True(t, hasStep(steps, "now#1"), "now() is a recorded step")

	recorded := time.Now()

	stop()
	require.NoError(t, first.Shutdown())
	require.Error(t, <-ended)

	time.Sleep(1100 * time.Millisecond)

	next := h.engine(t)
	done := make(chan runOutcome, 1)

	go func() {
		result, runErr := next.RunFile(testItem, testProject, file)
		done <- runOutcome{result: result, err: runErr}
	}()

	require.NoError(t, h.client(t, testVersion).Answer(testItem, "yes"))

	reading := readingOf(t, (<-done).err)

	require.False(t, reading.After(recorded), "the replay returned a reading from after the crash")
}
