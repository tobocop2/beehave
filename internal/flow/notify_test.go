package flow

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/model"
)

var notifyProgram = []string{"notifier", "--title", "beehest"}

func notifyFacts(argv []string) func(string) model.Project {
	return func(project string) model.Project { return model.Project{Name: project, Notify: argv} }
}

func notifyRequests(h *harness) []CommandRequest {
	h.commands.mu.Lock()
	defer h.commands.mu.Unlock()

	var out []CommandRequest

	for _, request := range h.commands.requests {
		if len(request.Run) > 0 && request.Run[0] == notifyProgram[0] {
			out = append(out, request)
		}
	}

	return out
}

func notified(t *testing.T, h *harness, engine *Engine) waiting[struct{}] {
	t.Helper()

	return waiting[struct{}]{what: "the notify command to run", run: runOfEngine(t, engine), read: func() (struct{}, bool, error) {
		return struct{}{}, len(notifyRequests(h)) > 0, nil
	}}
}

func openedWait(t *testing.T, h *harness, flowText string) (*Client, <-chan runOutcome) {
	t.Helper()

	h.flowIs(t, flowText)

	engine := h.engine(t)
	client := h.client(t, testVersion)
	done := runInBackground(engine, testItem)

	waitOn(t, notified(t, h, engine))

	return client, done
}

const notifyPlainWait = "def main(item):\n    wait(name = \"ask\", question = \"Ship it; $(x) 'now'?\", timeout = \"1h\")\n"

func TestNotify_APlainWaitRunsTheCommandOnceWithTheItemAndTheQuestion(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.facts = notifyFacts(notifyProgram)
	client, done := openedWait(t, h, notifyPlainWait)

	require.NoError(t, client.Reply(testItem, Reply{Text: "go"}))
	require.NoError(t, receive(t, done).err)

	got := notifyRequests(h)
	require.Len(t, got, 1)
	require.Equal(t, append(append([]string{}, notifyProgram...), testItem, "Ship it; $(x) 'now'?"), got[0].Run, "the list, then the item, then the question, each one argument")
	require.Equal(t, testProject, got[0].Project)
	require.False(t, got[0].Gate, "the notify runs outside the gate sandbox")
}

func TestNotify_AWaitWithPeopleRunsTheCommandOnce(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.facts = notifyFacts(notifyProgram)
	client, done := openedWait(t, h, "def main(item):\n    wait(name = \"ask\", question = \"Who?\", timeout = \"1h\", to = [\"alice\"])\n")

	require.NoError(t, client.Reply(testItem, Reply{From: "alice", Text: "me"}))
	require.NoError(t, receive(t, done).err)

	got := notifyRequests(h)
	require.Len(t, got, 1)
	require.Equal(t, append(append([]string{}, notifyProgram...), testItem, "Who?"), got[0].Run)
}

func TestNotify_AWaitOfAProjectWithNoNotifyRunsNoCommand(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.facts = func(project string) model.Project { return model.Project{Name: project} }
	h.flowIs(t, notifyPlainWait)

	engine := h.engine(t)
	client := h.client(t, testVersion)
	done := runInBackground(engine, testItem)

	waitOn(t, ownerAsksWithADeadline(t, client))
	require.NoError(t, client.Reply(testItem, Reply{Text: "go"}))
	require.NoError(t, receive(t, done).err)
	require.Empty(t, h.commands.ran())
}

func TestNotify_AResumedWaitRunsTheCommandZeroMoreTimes(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.facts = notifyFacts(notifyProgram)
	file := writeFlow(t, formatWaitFlow("1h"))

	stopCtx, stop := context.WithCancel(t.Context())
	first, err := NewEngine(stopCtx, h.opts, h.deps())
	require.NoError(t, err)
	require.NoError(t, first.Launch())

	ended := make(chan error, 1)

	go func() {
		_, runErr := first.RunFile(testItem, testProject, file, modeOff())
		ended <- runErr
	}()

	waitOn(t, notified(t, h, first))
	waitOn(t, inASleep(t, first))
	stop()
	require.NoError(t, first.Shutdown())
	require.Error(t, receive(t, ended))
	require.Len(t, notifyRequests(h), 1, "the first run notified once")

	encoded := base64.StdEncoding.EncodeToString([]byte(strconv.FormatInt(time.Now().Add(-time.Second).UnixMilli(), 10)))
	_, err = sqlOf(t, h).ExecContext(t.Context(), "UPDATE operation_outputs SET output = ? WHERE workflow_uuid = ? AND function_name = 'DBOS.sleep'", encoded, testRun)
	require.NoError(t, err)

	next := h.engine(t)
	resumed := make(chan error, 1)

	go func() {
		_, runErr := next.RunFile(testItem, testProject, file, modeOff())
		resumed <- runErr
	}()

	require.ErrorContains(t, waitOn(t, waiting[error]{what: "the resumed wait to end", ch: resumed}), "TIMED OUT")
	require.Len(t, notifyRequests(h), 1, "the resumed run ran the command zero more times")
}

func TestNotify_ACommandThatExitsNonZeroDoesNotFailTheWait(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.facts = notifyFacts(notifyProgram)
	h.commands.reply = func(CommandRequest) (CommandOutput, error) {
		return CommandOutput{Stderr: "first line\nboom\n", RC: 3}, nil
	}

	var logged bytes.Buffer

	h.flowIs(t, notifyPlainWait)

	engine := h.engine(t)
	engine.logger = newLogger(&logged, runRedactor(nil))
	client := h.client(t, testVersion)
	done := runInBackground(engine, testItem)

	waitOn(t, notified(t, h, engine))
	require.NoError(t, client.Reply(testItem, Reply{Text: "go"}))
	require.NoError(t, receive(t, done).err)
	require.Contains(t, logged.String(), "the notify command failed")
	require.Contains(t, logged.String(), "boom")
	require.NotContains(t, logged.String(), "first line", "the log holds the last line of stderr")
}

func TestNotify_ACommandThatCannotStartDoesNotFailTheWait(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.facts = notifyFacts(notifyProgram)
	h.commands.reply = func(CommandRequest) (CommandOutput, error) { return CommandOutput{}, errors.New("no such program") }
	client, done := openedWait(t, h, notifyPlainWait)

	require.NoError(t, client.Reply(testItem, Reply{Text: "go"}))
	require.NoError(t, receive(t, done).err)
}

func TestNotify_TheRequestCarriesTheBoundOfThirtySecondsAndNoShell(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.facts = notifyFacts(notifyProgram)
	client, done := openedWait(t, h, notifyPlainWait)

	require.NoError(t, client.Reply(testItem, Reply{Text: "go"}))
	require.NoError(t, receive(t, done).err)

	got := notifyRequests(h)
	require.Len(t, got, 1)
	require.Equal(t, 30*time.Second, got[0].Timeout)
	require.NotContains(t, strings.Join(got[0].Run, " "), "sh -c")
}

func TestNotify_TheQuestionPassesThroughTheRedactor(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.facts = notifyFacts(notifyProgram)
	client, done := openedWait(t, h, "def main(item):\n    wait(name = \"ask\", question = \"key sk-ant-abcdefghijklmnop?\", timeout = \"1h\")\n")

	require.NoError(t, client.Reply(testItem, Reply{Text: "go"}))
	require.NoError(t, receive(t, done).err)

	got := notifyRequests(h)
	require.Len(t, got, 1)
	require.NotContains(t, strings.Join(got[0].Run, " "), "abcdefghijklmnop")
}

func TestNotify_TheWaitOfAMergeForTheOwnerRunsTheCommandOnceWithTheRunItem(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.h.facts = notifyFacts(notifyProgram)
	rig.h.forge.closing = [][]int{{12}, {12, 13}}
	engine := rig.engine(t)
	rig.fixScopeOf(t, engine, "1")
	done := rig.runMergeAs(t, engine, "9", "1")

	question := questionOf(t, rig.h, "9")
	waitOn(t, notified(t, rig.h, engine))
	require.NoError(t, rig.h.client(t, testVersion).Reply("9", Reply{Text: "keep #13"}))
	require.NoError(t, receive(t, done).err)

	got := notifyRequests(rig.h)
	require.Len(t, got, 1, "the wait of the merge notified once")
	require.Equal(t, append(append([]string{}, notifyProgram...), "9", question), got[0].Run, "the notice names the run whose wait the owner answers")
	require.Equal(t, testProject, got[0].Project, "the notice runs in the project of the run")
}

// resumedAfterTheConfigChanges opens a wait of an hour under the facts of the first process, stops the engine in the sleep of the wait, moves the recorded deadline into the past and resumes the run under the facts of the second process.
// It returns the notify requests that the resume sent and the outcome of the resumed run.
func resumedAfterTheConfigChanges(t *testing.T, opened, resumed func(string) model.Project) (int, error) {
	t.Helper()

	h := newHarness(t)
	h.facts = opened
	file := writeFlow(t, formatWaitFlow("1h"))

	stopCtx, stop := context.WithCancel(t.Context())
	first, err := NewEngine(stopCtx, h.opts, h.deps())
	require.NoError(t, err)
	require.NoError(t, first.Launch())

	ended := make(chan error, 1)

	go func() {
		_, runErr := first.RunFile(testItem, testProject, file, modeOff())
		ended <- runErr
	}()

	waitOn(t, inASleep(t, first))
	stop()
	require.NoError(t, first.Shutdown())
	require.Error(t, receive(t, ended))

	before := len(notifyRequests(h))

	encoded := base64.StdEncoding.EncodeToString([]byte(strconv.FormatInt(time.Now().Add(-time.Second).UnixMilli(), 10)))
	_, err = sqlOf(t, h).ExecContext(t.Context(), "UPDATE operation_outputs SET output = ? WHERE workflow_uuid = ? AND function_name = 'DBOS.sleep'", encoded, testRun)
	require.NoError(t, err)

	h.facts = resumed

	next := h.engine(t)
	done := make(chan error, 1)

	go func() {
		_, runErr := next.RunFile(testItem, testProject, file, modeOff())
		done <- runErr
	}()

	got := waitOn(t, waiting[error]{what: "the resumed wait to end", ch: done})

	return len(notifyRequests(h)) - before, got
}

func TestNotify_AResumeAfterTheNotifyKeyIsRemovedReplaysTheWaitCleanly(t *testing.T) {
	t.Parallel()

	more, got := resumedAfterTheConfigChanges(t, notifyFacts(notifyProgram), func(project string) model.Project { return model.Project{Name: project} })

	require.ErrorContains(t, got, "TIMED OUT", "the resumed wait ends on its recorded deadline, not on a step that the new config does not know")
	require.NotContains(t, got.Error(), "UnexpectedStep")
	require.Zero(t, more, "the resume reads the record and runs no command")
}

func TestNotify_AResumeAfterTheNotifyKeyIsAddedReplaysTheWaitCleanly(t *testing.T) {
	t.Parallel()

	more, got := resumedAfterTheConfigChanges(t, func(project string) model.Project { return model.Project{Name: project} }, notifyFacts(notifyProgram))

	require.ErrorContains(t, got, "TIMED OUT", "the resumed wait ends on its recorded deadline, not on a step that the old config did not record")
	require.NotContains(t, got.Error(), "UnexpectedStep")
	require.Zero(t, more, "the wait opened with no notify, so the resume sends no late notice")
}

func TestNotify_AReminderOfAnOpenWaitSendsNoNotice(t *testing.T) {
	t.Parallel()

	clk := newRemindClock(time.Now())
	h := newHarness(t)
	h.facts = notifyFacts(notifyProgram)
	h.flowIs(t, remindFlow)

	engine := remindEngine(t, h, clk)
	client := h.client(t, testVersion)
	done := runInBackground(engine, testItem)

	waitOn(t, notified(t, h, engine))
	waitOn(t, waiting[struct{}]{what: "the run to show its next reminder", run: runOfEngine(t, engine), read: func() (struct{}, bool, error) {
		a := askedNow(t, client)

		return struct{}{}, a != nil && !a.NextRemind.IsZero(), nil
	}})
	clk.advance(35 * time.Minute)
	waitOn(t, waiting[struct{}]{what: "the run to send one reminder", run: runOfEngine(t, engine), read: func() (struct{}, bool, error) { return struct{}{}, remindedIs(t, client, 1), nil }})

	require.NoError(t, client.Reply(testItem, Reply{From: "ana", Text: "yes"}))
	require.NoError(t, receive(t, done).err)
	require.Len(t, notifyRequests(h), 1, "the open of the wait sent one notice, and the reminder sent none")
}

func TestNotify_TheDryRunFactsOfAProjectHoldNoNotifyCommand(t *testing.T) {
	t.Parallel()

	require.Empty(t, dryDeps(t.TempDir(), "head", "12", dryChoice{}).Facts(testProject).Notify, "a dry run notifies nobody")
}
