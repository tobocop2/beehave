package flow

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/require"
)

// interruptedAfterTheDeadlineIsRecorded runs the file until the wait has recorded its deadline, then stops the engine as a crash would.
func interruptedAfterTheDeadlineIsRecorded(t *testing.T, h *harness, file string) {
	t.Helper()

	stopCtx, stop := context.WithCancel(t.Context())
	engine, err := NewEngine(stopCtx, h.opts, h.deps())
	require.NoError(t, err)
	require.NoError(t, engine.Launch())

	ended := make(chan error, 1)

	go func() {
		_, runErr := engine.RunFile(testItem, testProject, file)
		ended <- runErr
	}()

	eventually(t, func() bool {
		steps, stepsErr := dbos.GetWorkflowSteps(engine.ctx, testRun)

		return stepsErr == nil && hasStep(steps, "DBOS.sleep")
	})

	stop()
	require.NoError(t, engine.Shutdown())
	require.Error(t, <-ended, "the stopped engine ends the run")
}

func hasStep(steps []dbos.StepInfo, name string) bool {
	for _, step := range steps {
		if step.StepName == name {
			return true
		}
	}

	return false
}

// resumedWithDeadlineAt crashes a run that waits an hour, moves the deadline that DBOS recorded to offset from now, and starts a new engine.
// A wait that took the whole hour again would fail the bound, so the outcome shows that the recorded deadline decides.
func resumedWithDeadlineAt(t *testing.T, offset time.Duration) error {
	t.Helper()

	h := newHarness(t)
	file := writeFlow(t, formatWaitFlow("1h"))
	interruptedAfterTheDeadlineIsRecorded(t, h, file)

	encoded := base64.StdEncoding.EncodeToString([]byte(strconv.FormatInt(time.Now().Add(offset).UnixMilli(), 10)))
	_, err := sqlOf(t, h).ExecContext(t.Context(), "UPDATE operation_outputs SET output = ? WHERE workflow_uuid = ? AND function_name = 'DBOS.sleep'", encoded, testRun)
	require.NoError(t, err)

	next := h.engine(t)
	done := make(chan error, 1)

	go func() {
		_, runErr := next.RunFile(testItem, testProject, file)
		done <- runErr
	}()

	select {
	case runErr := <-done:
		return runErr
	case <-time.After(5 * time.Minute):
		require.FailNow(t, "the resumed wait did not end at the recorded deadline")
	}

	return nil
}

func formatWaitFlow(timeout string) string {
	return "def main(item):\n    r = wait(name = \"ask\", question = \"What now?\", timeout = \"" + timeout + "\")\n    if r.timed_out:\n        fail(\"TIMED OUT\")\n"
}

func TestWait_AResumeAfterTheDeadlineTimesOutAtOnce(t *testing.T) {
	t.Parallel()

	require.ErrorContains(t, resumedWithDeadlineAt(t, -time.Second), "TIMED OUT")
}

func TestWait_AResumeBeforeTheDeadlineWaitsOnlyForTheTimeLeft(t *testing.T) {
	t.Parallel()

	require.ErrorContains(t, resumedWithDeadlineAt(t, 5*time.Second), "TIMED OUT")
}

func TestWait_ARecordedWaitWithoutTimedOutReplays(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	file := writeFlow(t, "def main(item):\n    r = wait(name = \"ask\", question = \"What now?\")\n    if r.answer != \"go on\":\n        fail(\"WRONG ANSWER\")\n")
	h.life.failing["release"] = true
	engine := h.engine(t)
	client := h.client(t, testVersion)

	done := make(chan runOutcome, 1)

	go func() {
		result, err := engine.RunFile(testItem, testProject, file)
		done <- runOutcome{result: result, err: err}
	}()

	waitForTheOwner(t, h)
	require.NoError(t, client.Answer(testItem, "go on"))
	require.ErrorContains(t, (<-done).err, "release")

	dropTimedOutFromTheRecord(t, h)

	h.life.mu.Lock()
	h.life.failing["release"] = false
	h.life.mu.Unlock()

	require.NoError(t, client.Resume(testItem))

	_, err := engine.RunFile(testItem, testProject, file)

	require.NoError(t, err)
}

// dropTimedOutFromTheRecord rewrites the stored wait step as a build before timed_out recorded it.
func dropTimedOutFromTheRecord(t *testing.T, h *harness) {
	t.Helper()

	db := sqlOf(t, h)

	var stored string

	require.NoError(t, db.QueryRowContext(t.Context(), "SELECT output FROM operation_outputs WHERE workflow_uuid = ? AND function_name = 'wait:ask#1'", testRun).Scan(&stored))

	raw, err := base64.StdEncoding.DecodeString(stored)
	require.NoError(t, err)

	var record struct {
		Outputs map[string]any `json:"outputs"`
	}

	require.NoError(t, json.Unmarshal(raw, &record))
	require.Contains(t, record.Outputs, "timed_out")
	delete(record.Outputs, "timed_out")

	rewritten, err := json.Marshal(record)
	require.NoError(t, err)

	_, err = db.ExecContext(t.Context(), "UPDATE operation_outputs SET output = ? WHERE workflow_uuid = ? AND function_name = 'wait:ask#1'", base64.StdEncoding.EncodeToString(rewritten), testRun)
	require.NoError(t, err)
}
