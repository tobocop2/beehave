package flow

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/workflow"
)

// supervisorRig runs examples/supervisor/workflow.star, copied with its prompt, over the fake collaborators. The first start of the fake agent says two lines and then holds.
// After the test closes the gate it says a forbidden call and goes on until its context ends.
func supervisorRig(t *testing.T) (*agentRig, string) {
	t.Helper()

	folder := filepath.Join(t.TempDir(), "supervisor")
	require.NoError(t, os.CopyFS(folder, os.DirFS(filepath.Join("..", "..", "examples", "supervisor"))))

	rig := newAgentRig(t)
	rig.reply("", exited)
	rig.h.opts.Env = map[string]string{"TASK": "21", "WATCH": "60s"}
	rig.h.agents.gate = make(chan struct{})
	rig.h.agents.startSaid = [][]agent.Said{{{Text: "LINE-ONE"}, {Text: "LINE-TWO"}}, {}}
	rig.h.agents.lastWords = []agent.Said{{Call: true, Text: "Bash: rm -rf x"}}

	return rig, filepath.Join(folder, "workflow.star")
}

func TestSupervisorExample_ACrashInsideStreamDeliversEachEntryOnceAndCancelsOnce(t *testing.T) {
	t.Parallel()

	rig, file := supervisorRig(t)
	h := rig.h

	deps := h.deps()
	stopCtx, stop := context.WithCancel(t.Context())
	first, err := NewEngine(stopCtx, h.opts, deps)
	require.NoError(t, err)
	require.NoError(t, first.Launch())

	go func() { _, _ = first.RunFile(testItem, testProject, file, modeOff()) }() //nolint:errcheck // the stop ends the run with an error

	// The supervisor logged the two lines and waits inside the third read: its deadline is the last record.
	waitOn(t, condition(func() bool {
		names := stepNamesOf(t, first, testRun)
		if len(names) == 0 {
			return false
		}

		last := names[len(names)-1]

		return len(h.commands.ranBy(testItem)) == 2 && strings.HasPrefix(last, "stream:") && strings.HasSuffix(last, deadlineSuffix)
	}))
	stop()
	require.NoError(t, first.Shutdown())

	second := h.engine(t)
	done := runInBackground2(second, file)

	waitOn(t, condition(func() bool { return len(h.agents.seen()) == 2 }))
	close(h.agents.gate)

	pace := time.NewTicker(20 * time.Millisecond)
	defer pace.Stop()

	var ended runOutcome

	for waiting := true; waiting; {
		select {
		case ended = <-done:
			waiting = false
		case <-pace.C:
			h.tick(t)
		}
	}

	require.NoError(t, ended.err)
	assert.Equal(t, []string{
		"echo saw text LINE-ONE", "echo saw text LINE-TWO", "echo saw call Bash: rm -rf x",
		"echo forbidden command Bash: rm -rf x", "echo entries 3 canceled True",
	}, h.commands.ranBy(testItem), "each entry is logged once across the crash")

	names := stepNamesOf(t, second, testRun)
	assert.Equal(t, 1, strings.Count(strings.Join(names, " "), "cancel:stop#1 "), "cancel ran once: %v", names)
	assert.Equal(t, dbos.WorkflowStatusCancelled, statusOf(t, second, "item-21-r1"), "the worker ended canceled")
}

// runtimeStream runs a parent that has spawned the worker and then runs body, and returns the error of the run.
func runtimeStream(t *testing.T, body string) error {
	t.Helper()

	rig := newAgentRig(t)
	rig.reply("", exited)

	_, err := rig.run(t, a853Parent(body), "Work.\n")

	return err
}

func TestStream_ACallThatBreaksARuleAtRunTimeIsRefusedWithTheRule(t *testing.T) {
	t.Parallel()

	cases := map[string]struct{ body, want string }{
		"cursor":  {"c = \"later\"\nstream(w, after = c, timeout = \"1s\")", "not a cursor that stream returned"},
		"timeout": {"z = \"0s\"\nstream(w, timeout = z)", "must be above zero"},
		"run":     {"stream(3, timeout = \"1s\")", "run must be one of the runs that spawn returned"},
		"keyword": {"k = {\"bogus\": 1}\nstream(w, timeout = \"1s\", **k)", "bogus"},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			require.ErrorContains(t, runtimeStream(t, c.body), c.want)
		})
	}
}

func TestStream_ACursorInAPhaseThatTheRunDoesNotHaveStartsAtTheFirstEntry(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", exited)
	rig.h.agents.said = said()

	_, err := rig.run(t, a853Parent("gather(runs, name = \"w\")\ns = stream(w, after = \"item-9-r1-phase/2\", timeout = \"5s\", name = \"peek\")\n"+a853Log), "Work.\n")

	require.NoError(t, err)
	assert.Equal(t, []string{"log 3 LINE-ONE|KEY [redacted]|Bash: ls True"}, rig.h.commands.ranBy(testItem), "a cursor of another attempt gives the whole stream of this one")
}

func TestStream_AnEntryCountOverThePageCapGivesTheFirstPageWithTheChildNotEnded(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", exited)

	for i := range streamPageEntries + 5 {
		rig.h.agents.said = append(rig.h.agents.said, agent.Said{Text: "e" + string(rune('a'+i%26))})
	}

	body := "gather(runs, name = \"w\")\na = stream(w, timeout = \"5s\", name = \"a\")\nb = stream(w, after = a.next, timeout = \"5s\", name = \"b\")\n" +
		`command(name = "log", run = ["log", str(len(a.entries)), str(a.ended), str(len(b.entries)), str(b.ended), str(b.entries[0].seq), str(b.entries[0].attempt)])`
	_, err := rig.run(t, a853Parent(body), "Work.\n")

	require.NoError(t, err)
	assert.Equal(t, []string{"log 200 False 5 True 201 1"}, rig.h.commands.ranBy(testItem), "the page ends at the cap, and the next call goes on from the cursor")
}

func TestStream_TextOverTheValueLimitEndsThePageBeforeTheEntryThatPassesIt(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", exited)

	big := strings.Repeat("x", valueLimit/2-1)
	rig.h.agents.said = []agent.Said{{Text: big}, {Text: big}, {Text: big}}

	body := "gather(runs, name = \"w\")\na = stream(w, timeout = \"5s\", name = \"a\")\n" + `command(name = "log", run = ["log", str(len(a.entries)), str(a.ended)])`
	_, err := rig.run(t, a853Parent(body), "Work.\n")

	require.NoError(t, err)
	assert.Equal(t, []string{"log 2 False"}, rig.h.commands.ranBy(testItem), "two entries fit in 64 KiB and the third waits for the next call")
}

func TestStreamWait_ARunThatDoesNotExistAndACanceledWaitAreErrors(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", exited)
	rig.h.agents.gate = make(chan struct{})

	engine := rig.h.engine(t)
	done := runInBackground2(engine, flowFile(t, a853Parent("gather(runs, name = \"w\")"), "Work.\n"))

	_, err := engine.streamWait(t.Context(), "item-none-r1", workflow.StreamCursor{}, time.Now().Add(time.Minute))
	require.ErrorIs(t, err, errNoSuchRun)

	waitOn(t, condition(func() bool { return len(rig.h.agents.seen()) == 1 }))

	gone, cancel := context.WithCancel(t.Context())
	cancel()

	_, err = engine.streamWait(gone, "item-21-r1", workflow.StreamCursor{}, time.Now().Add(time.Minute))
	require.ErrorIs(t, err, context.Canceled, "an open child with no entry waits, and the wait ends with its context")

	close(rig.h.agents.gate)
	require.NoError(t, receive(t, done).err)
}

func TestStreamAfter_AClosedStoreIsAnErrorAtTheLook(t *testing.T) {
	t.Parallel()

	client := newHarness(t).client(t, testVersion)
	require.NoError(t, dbos.Shutdown(client.flow, time.Second))

	_, err := streamAfter(client.flow, "item-21-r1", workflow.StreamCursor{})
	require.Error(t, err)
}

func TestReadSnapshot_AClientWithNoStoreCannotReadAPhase(t *testing.T) {
	t.Parallel()

	_, err := readSnapshot(nil, "phase")
	require.Error(t, err)
}
