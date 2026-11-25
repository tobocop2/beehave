package flow

// The acceptance test of #853: a recorded step stream(run, after, timeout) that returns the redacted agent stream entries of a run that this run spawned.
// The oracle is the table of rulings/853.md, taken from the issue and from the owner ruling of 2026-11-21. It reads no code of the fix.
// Run: go test ./internal/flow -run 'TestAcceptance853' -count=1 -timeout 300s

import (
	"strings"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/workflow"
)

// a853Worker is the child. It runs one agent call, and the fake agent says LINE-ONE, a line with a token, and a tool call.
const a853Worker = `def worker(item):
    agent(model = "m", prompt = "prompts/a.md", name = "a")

`

// a853Parent spawns the worker, and then runs body, one statement for each line.
func a853Parent(body string) string {
	return a853Worker + "def main(item):\n    runs = spawn(items = [\"21\"], entry = \"worker\", name = \"w\")\n    w = runs.runs[0]\n    " + strings.ReplaceAll(strings.TrimSpace(body), "\n", "\n    ") + "\n"
}

// a853Log is the command that writes what the parent read, as one line.
const a853Log = `command(name = "log", run = ["log", str(len(s.entries)), "|".join([e.text for e in s.entries]), str(s.ended)])`

// The control: the same parent and worker, with no stream call, run at the base. It shows that a red row below is the missing builtin and not the harness.
func TestAcceptance853_KeepTheWorkerRunsAndTheParentGathersItAtTheBase(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", exited)
	rig.h.agents.said = said()

	_, err := rig.run(t, a853Parent("gather(runs, name = \"w\")\ncommand(name = \"log\", run = [\"log\", \"gathered\"])"), "Work.\n")

	require.NoError(t, err)
	assert.Equal(t, []string{"log gathered"}, rig.h.commands.ranBy(testItem))
	assert.Len(t, rig.h.agents.seen(), 1, "the child ran its agent once")
}

func TestAcceptance853_StreamGivesTheRedactedEntriesOfAnEndedChildInOrder(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", exited)
	rig.h.agents.said = said()

	_, err := rig.run(t, a853Parent("gather(runs, name = \"w\")\ns = stream(w, after = \"\", timeout = \"5s\", name = \"peek\")\n"+a853Log), "Work.\n")

	require.NoError(t, err)
	assert.Equal(t, []string{"log 3 LINE-ONE|KEY [redacted]|Bash: ls True"}, rig.h.commands.ranBy(testItem), "the three entries, redacted as status --follow redacts them, and ended")
}

func TestAcceptance853_ACursorOfTheLastCallGivesNothingNewAndDoesNotWaitForAnEndedChild(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", exited)
	rig.h.agents.said = said()

	body := "gather(runs, name = \"w\")\nfirst = stream(w, after = \"\", timeout = \"5s\", name = \"one\")\n" +
		"s = stream(w, after = first.next, timeout = \"60s\", name = \"two\")\n" +
		`command(name = "log", run = ["log", str(len(s.entries)), str(s.ended), str(s.next == first.next), str(first.next != "")])`

	started := time.Now()
	_, err := rig.run(t, a853Parent(body), "Work.\n")

	require.NoError(t, err)
	assert.Equal(t, []string{"log 0 True True True"}, rig.h.commands.ranBy(testItem), "no new entry, the child ended, the cursor stays")
	assert.Less(t, time.Since(started), 30*time.Second, "an ended child ends the wait at once, not at the timeout")
}

func TestAcceptance853_WithNoNewEntryTheStepWaitsTheTimeoutAndReturnsNothingWhileTheChildWorks(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", exited)
	rig.h.agents.gate = make(chan struct{})

	engine := rig.h.engine(t)
	done := runInBackground2(engine, flowFile(t, a853Parent("s = stream(w, after = \"\", timeout = \"1s\", name = \"peek\")\n"+a853Log+"\ngather(runs, name = \"w\")"), "Work.\n"))

	// The read waits the timeout of 1 s. A parent that ends before it logs, or a log that is late, fails the test within 30 s and not at the guard of the harness.
	deadline := time.After(30 * time.Second)
	logged := time.NewTicker(pollEvery)

	defer logged.Stop()

	var parent *runOutcome

	for len(rig.h.commands.ranBy(testItem)) == 0 && parent == nil {
		select {
		case ended := <-done:
			parent = &ended
		case <-deadline:
			t.Fatal("the parent did not log the empty read within 30 s")
		case <-logged.C:
		}
	}

	require.Nil(t, parent, "the parent ended before it logged the read")
	assert.Equal(t, []string{"log 0  False"}, rig.h.commands.ranBy(testItem), "no entry, and the child has not ended")

	close(rig.h.agents.gate)
	require.NoError(t, receive(t, done).err)
}

func TestAcceptance853_ARunThatThisRunDidNotSpawnIsAnError(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", exited)

	flow := a853Worker + "def main(item):\n    stream(struct(item = \"21\", run = \"item-21-r1\"), after = \"\", timeout = \"1s\", name = \"peek\")\n"
	_, err := rig.run(t, flow, "Work.\n")

	require.ErrorContains(t, err, "run item-21-r1 is not a child of this run")
}

func TestAcceptance853_TheCallIsOneRecordedStepWithItsDeadlineAndAReplayReturnsTheRecordedEntries(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", exited)
	rig.h.agents.said = said()

	engine := rig.h.engine(t)
	_, err := engine.RunFile(testItem, testProject, flowFile(t, a853Parent("gather(runs, name = \"w\")\ns = stream(w, after = \"\", timeout = \"5s\", name = \"peek\")\n"+a853Log), "Work.\n"), modeOff())
	require.NoError(t, err)

	steps, err := dbos.GetWorkflowSteps(engine.ctx, testRun)
	require.NoError(t, err)

	var names []string

	at := -1

	for _, s := range steps {
		names = append(names, s.StepName)

		if s.StepName == "stream:peek#1" {
			at = s.StepID
		}
	}

	require.GreaterOrEqual(t, at, 0, "one counted step records the call: %v", names)
	assert.Contains(t, names, "stream:peek#1.deadline", "the deadline is recorded once, before the read")

	// The store lost the stream. A replay of the recorded step returns the entries of the first read.
	_, err = sqlOf(t, rig.h).ExecContext(t.Context(), "DELETE FROM streams WHERE key = 'agent'")
	require.NoError(t, err)

	forked, err := dbos.ForkWorkflow[ItemResult](engine.ctx, dbos.ForkWorkflowInput{OriginalWorkflowID: testRun, StartStep: uint(at + 1)})
	require.NoError(t, err)

	_, err = forked.GetResult()
	require.NoError(t, err)

	assert.Equal(t, []string{"log 3 LINE-ONE|KEY [redacted]|Bash: ls True", "log 3 LINE-ONE|KEY [redacted]|Bash: ls True"}, rig.h.commands.ranBy(testItem),
		"the replay returns the recorded entries and does not read the stream again")
}

func TestAcceptance853_TheStepBelongsToTheCoreModuleFromVersion18(t *testing.T) {
	t.Parallel()

	module, known := moduleOfKind("stream")

	assert.True(t, known, "a resumed run must find the kind in a module")
	assert.Equal(t, moduleCore, module)
	assert.True(t, recordedHere("stream:peek#1"), "recordedHere refuses a step of no module, and a resumed run cannot replay")
	assert.True(t, recordedHere("stream:peek#1.deadline"))
	assert.GreaterOrEqual(t, coreVersion, 18, "the stream step came with version 18 of the core module")
}

func TestAcceptance853_TheCheckKnowsTheBuiltinItsArgumentsAndItsResultFields(t *testing.T) {
	t.Parallel()

	rows := []struct {
		id, body string
		clean    bool
	}{
		{"ok-all-arguments", `s = stream(w, after = "", timeout = "5s", name = "p")`, true},
		{"ok-after-default", `s = stream(w, timeout = "5s")`, true},
		{"ok-fields", `s = stream(w, timeout = "5s")
x = [s.entries, s.next, s.ended, [e.seq for e in s.entries], [e.attempt for e in s.entries], [e.kind for e in s.entries], [e.text for e in s.entries]]`, true},
		{"no-timeout", `s = stream(w)`, false},
		{"zero-timeout", `s = stream(w, timeout = "0s")`, false},
		{"after-not-text", `s = stream(w, after = 3, timeout = "5s")`, false},
		{"no-run", `s = stream(timeout = "5s")`, false},
		{"unknown-field", `s = stream(w, timeout = "5s")
x = s.bogus`, false},
		{"unknown-argument", `s = stream(w, timeout = "5s", limit = 3)`, false},
	}

	for _, row := range rows {
		t.Run(row.id, func(t *testing.T) {
			t.Parallel()

			found, _ := workflow.Check(writeFlow(t, a853Parent(row.body)))

			if row.clean {
				assert.Empty(t, found, "the check accepts the call")
			} else {
				assert.NotEmpty(t, found, "the check refuses the call")
			}
		})
	}
}
