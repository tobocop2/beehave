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
		_, runErr := engine.RunFile(testItem, testProject, file, modeOff())
		ended <- runErr
	}()

	waitOn(t, waiting[struct{}]{what: "the wait to record its deadline", run: runOfEngine(t, engine), read: func() (struct{}, bool, error) {
		steps, stepsErr := dbos.GetWorkflowSteps(engine.ctx, testRun)

		return struct{}{}, stepsErr == nil && hasStep(steps, "DBOS.sleep"), nil
	}})

	stop()
	require.NoError(t, engine.Shutdown())
	require.Error(t, receive(t, ended), "the stopped engine ends the run")
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
		_, runErr := next.RunFile(testItem, testProject, file, modeOff())
		done <- runErr
	}()

	return waitOn(t, waiting[error]{what: "the resumed wait to end at the recorded deadline", ch: done})
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

func TestWait_AResumeOfARunWhoseWaitTimedOutWaitsAgainUntilAFreshDeadline(t *testing.T) {
	t.Parallel()

	answeredAfterAResume(t, "", Reply{Text: "go on"})
}

func TestWait_AResumeOfARunWhoseWaitOfPeopleHeardNoVoiceWaitsAgainUntilAFreshDeadline(t *testing.T) {
	t.Parallel()

	answeredAfterAResume(t, `, to = ["ana", "bo"], quorum = 2`, Reply{From: "ana", Text: "go on"}, Reply{From: "bo", Text: "go on"})
}

func TestWait_AResumeOfARunWhoseWaitWithChoicesGotNoAnswerWaitsAgainUntilAFreshDeadline(t *testing.T) {
	t.Parallel()

	answeredAfterAResume(t, `, choices = ["go on", "stop"]`, Reply{Text: "go on"})
}

// answeredAfterAResume runs a wait with a 3 s timeout and the keywords given, lets it time out, resumes the run and sends the replies to the reopened wait.
// The run must end well: the reopened wait records a fresh deadline and does not replay the one that passed.
func answeredAfterAResume(t *testing.T, keywords string, replies ...Reply) {
	t.Helper()

	h := newHarness(t)
	// The agent step comes first, so the resume does not go back to the start mark of the run.
	h.flowIs(t, "def main(item):\n    agent(name = \"work\", prompt = \"prompts/work.md\", model = \"m\")\n"+
		"    r = wait(name = \"ask\", question = \"What now?\", timeout = \"3s\""+keywords+")\n    if r.timed_out:\n        fail(\"TIMED OUT\")\n")
	engine := h.engine(t)
	client := h.client(t, testVersion)

	_, err := engine.Run(testItem, testProject)
	require.ErrorContains(t, err, "TIMED OUT")

	// The resume drops the receive that timed out with its sleep. The answer goes out once the reopened wait has started its receive again.
	handle := resumeRun(t, engine, client)
	waitOn(t, inASleep(t, engine))
	waitOn(t, ownerAsks(t, h))
	for _, reply := range replies {
		require.NoError(t, client.Reply(testItem, reply), "the resumed run waits for an answer again")
	}

	_, err = resultOfHandle(t, handle)
	require.NoError(t, err, "the answer reaches the resumed wait before its fresh deadline")
	require.Len(t, h.agents.seen(), 1, "the steps before the wait keep their results")
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
		result, err := engine.RunFile(testItem, testProject, file, modeOff())
		done <- runOutcome{result: result, err: err}
	}()

	waitOn(t, ownerAsks(t, h))
	require.NoError(t, client.Reply(testItem, Reply{Text: "go on"}))
	require.ErrorContains(t, receive(t, done).err, "release")

	dropTimedOutFromTheRecord(t, h)

	h.life.mu.Lock()
	h.life.failing["release"] = false
	h.life.mu.Unlock()

	_, err := resumeAndJoin(t, engine, client)

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
