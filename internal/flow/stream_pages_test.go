package flow

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/workflow"
)

// twoPhaseWorker is a child with two agent calls, so its run has two phases, each with its own stream.
const twoPhaseWorker = `def worker(item):
    agent(model = "m", prompt = "prompts/a.md", name = "a")
    agent(model = "m", prompt = "prompts/a.md", name = "b")

`

func TestStream_ACursorInTheSecondPhaseOfAChildSkipsTheFirstPhase(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", exited)
	rig.h.agents.startSaid = [][]agent.Said{{{Text: "A1"}, {Text: "A2"}}, {{Text: "B1"}, {Text: "B2"}}}

	flow := twoPhaseWorker + "def main(item):\n    runs = spawn(items = [\"21\"], entry = \"worker\", name = \"w\")\n    w = runs.runs[0]\n    gather(runs, name = \"w\")\n" +
		"    all = stream(w, timeout = \"5s\", name = \"all\")\n" +
		"    phase = all.next.rpartition(\"/\")[0]\n" +
		"    mid = stream(w, after = phase + \"/1\", timeout = \"5s\", name = \"mid\")\n" +
		"    last = stream(w, after = all.next, timeout = \"5s\", name = \"last\")\n" +
		"    command(name = \"log\", run = [\"log\", \"|\".join([e.text for e in all.entries]), \"|\".join([e.text for e in mid.entries]), str(len(last.entries)), str(last.ended)])\n"
	_, err := rig.run(t, flow, "Work.\n")

	require.NoError(t, err)
	assert.Equal(t, []string{"log A1|A2|B1|B2 B2 0 True"}, rig.h.commands.ranBy(testItem), "the cursor in the second phase gives only what follows it there, and nothing from the first phase")
}

func TestStatus_ARunThatWaitsInStreamShowsTheChildAndTheDeadlineOfTheWait(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", exited)
	rig.h.agents.gate = make(chan struct{})

	engine := rig.h.engine(t)
	client := rig.h.client(t, testVersion)
	before := time.Now()
	done := runInBackground2(engine, flowFile(t, a853Parent(`stream(w, timeout = "8s", name = "peek")`), "Work.\n"))

	var view RunSteps

	waitOn(t, waiting[struct{}]{what: "the open run to show the child of its stream", run: runOfClient(t, client), last: func() string { return view.Now }, read: func() (struct{}, bool, error) {
		var err error

		view, err = client.Steps(testRun)

		return struct{}{}, err == nil && view.Streaming != "", nil
	}})

	assert.Equal(t, "item-21-r1", view.Streaming)
	assert.Equal(t, "stream:peek#1", view.Now, "the open step is the stream call, not the record of its deadline")
	assert.WithinDuration(t, before.Add(8*time.Second), view.Deadline, 3*time.Second)
	assert.Contains(t, view.Notes(), "waits for the agent output of run item-21-r1 until "+view.Deadline.Format(time.RFC3339))
	assert.Empty(t, view.Receiving, "a stream shows no topic: only a recv call does")

	close(rig.h.agents.gate)
	require.NoError(t, receive(t, done).err)
}

func TestStreamingOf_ReadsOnlyTheDeadlineRecordOfAStreamThatIsLast(t *testing.T) {
	t.Parallel()

	const window = `{"until":"2030-01-02T03:04:05Z","run":"item-21-r1"}`

	until := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	record := func(name string, out any) dbos.StepInfo { return dbos.StepInfo{StepName: name, Output: out} }

	got, waits := streamingOf([]dbos.StepInfo{record("stream:a#1.deadline", window), record("DBOS.sleep", "1")})
	assert.True(t, waits)
	assert.Equal(t, streamWindow{Until: until, Run: "item-21-r1"}, got)

	_, waits = streamingOf([]dbos.StepInfo{record("stream:a#1.deadline", window), record("stream:a#1", "{}")})
	assert.False(t, waits, "a stream that ended waits for nothing")

	_, waits = streamingOf([]dbos.StepInfo{record("stream:a#1.deadline", "1790000000000")})
	assert.False(t, waits, "a deadline record that holds only a time, the shape before the run was added, is no wait and is never read as one")

	_, waits = streamingOf([]dbos.StepInfo{record("recv:a#1.deadline", window)})
	assert.False(t, waits, "the deadline of a recv is no wait of a stream")
}

func TestStream_ARunCanceledBeforeTheDeadlineIsRecordedFailsTheCall(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", exited)
	rig.h.agents.gate = make(chan struct{})

	parked, release := make(chan struct{}), make(chan struct{})
	deps := rig.h.deps()
	var once sync.Once

	deps.Clock.Reading = func() time.Time {
		once.Do(func() {
			close(parked)
			<-release
		})

		return time.Now()
	}

	engine, err := NewEngine(t.Context(), rig.h.opts, deps)
	require.NoError(t, err)
	require.NoError(t, engine.Launch())

	done := startRun(t, engine, flowFile(t, a853Parent(`stream(w, timeout = "8s", name = "peek")`), "Work.\n"))

	receive(t, parked)
	require.NoError(t, dbos.CancelWorkflow(engine.ctx, testRun))
	close(release)

	require.ErrorIs(t, receive(t, done).err, dbos.ErrWorkflowCancelled, "the run was canceled, so the step store refuses the record of the deadline")
	assert.NotContains(t, stepNamesOf(t, engine, testRun), "stream:peek#1.deadline")

	// The child still holds the gate. It ends before the test folder goes.
	close(rig.h.agents.gate)
	require.NoError(t, runHasStatus(t.Context(), engine, "item-21-r1", dbos.WorkflowStatusSuccess))
}

func TestStream_AChildThatTheStoreLosesFailsTheReadAndNoLaterStepRuns(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", exited)
	rig.h.agents.gate = make(chan struct{})

	parked, release := make(chan struct{}), make(chan struct{})
	deps := rig.h.deps()

	var once sync.Once

	deps.Clock.Reading = func() time.Time {
		once.Do(func() {
			close(parked)
			<-release
		})

		return time.Now()
	}

	engine, err := NewEngine(t.Context(), rig.h.opts, deps)
	require.NoError(t, err)
	require.NoError(t, engine.Launch())

	done := startRun(t, engine, flowFile(t, a853Parent("stream(w, timeout = \"8s\", name = \"peek\")\ncommand(name = \"after\", run = [\"after\"])"), "Work.\n"))

	// The parent is inside the record of the deadline. The store loses the child before the read step looks for it.
	receive(t, parked)
	storeExec(t, rig.h, `DELETE FROM workflow_status WHERE workflow_uuid = 'item-21-r1'`)
	close(release)

	err = receive(t, done).err
	close(rig.h.agents.gate)

	require.ErrorIs(t, err, errNoSuchRun, "the read step reports the lost child")
	require.ErrorContains(t, err, `"item-21-r1"`)
	assert.Empty(t, rig.h.commands.ranBy(testItem), "the command after stream never runs: the call returns the error of its read")
}

func TestStream_AnUnreadablePhaseStreamIsAnError(t *testing.T) {
	t.Parallel()

	page := streamRead{Entries: []AgentEntry{}}
	partial, err := fillPage(storeView{}, &page, []string{"item-21-r1-phase"}, workflow.StreamCursor{})

	require.Error(t, err)
	assert.False(t, partial)
	assert.Empty(t, page.Entries)
}

// textPage runs a child whose agent says one entry of each given size, and returns the line that the parent logs about the first read.
func textPage(t *testing.T, sizes ...int) []string {
	t.Helper()

	rig := newAgentRig(t)
	rig.reply("", exited)

	for _, size := range sizes {
		rig.h.agents.said = append(rig.h.agents.said, agent.Said{Text: strings.Repeat("x", size)})
	}

	body := "gather(runs, name = \"w\")\na = stream(w, timeout = \"5s\", name = \"a\")\n" + `command(name = "log", run = ["log", str(len(a.entries)), str(a.ended)])`
	_, err := rig.run(t, a853Parent(body), "Work.\n")
	require.NoError(t, err)

	return rig.h.commands.ranBy(testItem)
}

func TestStream_TextOfExactlyTheValueLimitFitsInOnePage(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{"log 2 True"}, textPage(t, valueLimit/2, valueLimit/2), "two entries that add up to 64 KiB fit, and the child has ended")
}

func TestStream_TextOneByteOverTheValueLimitEndsThePageBeforeTheLastEntry(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{"log 1 False"}, textPage(t, valueLimit/2, valueLimit/2+1), "the second entry is one byte over 64 KiB and waits for the next call")
}

// grandchildFlow has a worker that spawns a worker of its own, and a main that spawns the first and reads the stream of the second with body.
func grandchildFlow(body string) string {
	return `def grand(item):
    command(name = "inner", run = ["inner"])

def worker(item):
    sub = spawn(items = ["22"], entry = "grand", name = "g")
    gather(sub, name = "g")

def main(item):
    runs = spawn(items = ["21"], entry = "worker", name = "w")
    gather(runs, name = "w")
    ` + strings.ReplaceAll(strings.TrimSpace(body), "\n", "\n    ") + "\n"
}

func TestStream_ARunThatTheChildSpawnedIsRefusedAsNoChildOfThisRun(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", exited)

	_, err := rig.run(t, grandchildFlow(`stream(struct(item = "22", run = "item-22-r1"), timeout = "1s", name = "peek")`), "Work.\n")

	require.ErrorContains(t, err, "run item-22-r1 is not a child of this run")
	assert.Equal(t, []string{"inner"}, rig.h.commands.ranBy("22"), "the grandchild ran, so the refusal is about the relation and not about a missing run")
}

func TestStream_ARunOfAnotherItemThatExistsIsRefusedAsNoChildOfThisRun(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", exited)

	_, err := rig.run(t, grandchildFlow(`stream(struct(item = "21", run = "item-`+testItem+`-r1"), timeout = "1s", name = "peek")`), "Work.\n")

	require.ErrorContains(t, err, "is not a child of this run")
}
