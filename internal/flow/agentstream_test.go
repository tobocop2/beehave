package flow

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
)

// said is what the fake agent says in the tests below: a message, a message with a token, a tool call.
func said() []agent.Said {
	return []agent.Said{{Text: "LINE-ONE"}, {Text: "KEY sk-ant-abcdefghijklmnop0123"}, {Call: true, Text: "Bash: ls"}}
}

// collected is the entries that a follow emitted. It is safe to read while the follow runs.
type collected struct {
	mu      sync.Mutex
	entries []FollowedEntry
}

func (c *collected) add(entry FollowedEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.entries = append(c.entries, entry)
}

func (c *collected) all() []FollowedEntry {
	c.mu.Lock()
	defer c.mu.Unlock()

	return append([]FollowedEntry(nil), c.entries...)
}

// texts returns the text of each entry.
func (c *collected) texts() []string {
	var texts []string

	for _, entry := range c.all() {
		texts = append(texts, entry.Text)
	}

	return texts
}

func TestAgentStream_AFollowOfARunningPhaseGetsItsEntriesBeforeTheAgentEnds(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.agents.said = said()
	harness.agents.gate = make(chan struct{})
	engine := harness.engine(t)
	client := harness.client(t, testVersion)

	done := runInBackground(engine, testItem)
	waitOn(t, waiting[struct{}]{what: "the agent to run", run: runOfEngine(t, engine), read: func() (struct{}, bool, error) { return struct{}{}, harness.agents.running() == 1, nil }})

	got := &collected{}
	followed := make(chan error, 1)

	go func() { followed <- client.Follow(t.Context(), testItem, got.add) }()

	waitOn(t, waiting[struct{}]{what: "the three entries", run: runOfEngine(t, engine), read: func() (struct{}, bool, error) { return struct{}{}, len(got.all()) == 3, nil }})
	assert.Equal(t, 1, harness.agents.running(), "the follow shows the entries while the agent still runs")

	close(harness.agents.gate)
	require.NoError(t, receive(t, done).err)
	require.NoError(t, receive(t, followed), "the follow ends when the run ends")

	entries := got.all()
	require.Len(t, entries, 3)
	assert.Equal(t, []string{"LINE-ONE", "KEY [redacted]", "Bash: ls"}, got.texts(), "every entry passes the redactor")
	assert.Equal(t, []AgentEntry{{Seq: 1, Try: 1, Kind: EntryText, Text: "LINE-ONE"}, {Seq: 2, Try: 1, Kind: EntryText, Text: "KEY [redacted]"}, {Seq: 3, Try: 1, Kind: EntryCall, Text: "Bash: ls"}},
		[]AgentEntry{entries[0].AgentEntry, entries[1].AgentEntry, entries[2].AgentEntry})
}

func TestAgentStream_AFollowOfAnEndedRunPrintsTheStreamAgainByItemAndByRunId(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.agents.said = said()
	_, err := harness.engine(t).Run(testItem, testProject)
	require.NoError(t, err)

	client := harness.client(t, testVersion)
	byItem, byRun := &collected{}, &collected{}

	require.NoError(t, client.Follow(t.Context(), testItem, byItem.add))
	require.NoError(t, client.Follow(t.Context(), runID(testItem, 1), byRun.add))

	assert.Equal(t, []string{"LINE-ONE", "KEY [redacted]", "Bash: ls"}, byItem.texts())
	assert.Equal(t, byItem.all(), byRun.all(), "a run id and its item give the same stream")
}

func TestAgentStream_AFollowOfAnItemWithNoRunSaysSo(t *testing.T) {
	t.Parallel()

	client := newHarness(t).client(t, testVersion)

	err := client.Follow(t.Context(), "fix-none", func(FollowedEntry) {})

	require.ErrorIs(t, err, errNoSuchRun)
	assert.Contains(t, err.Error(), `"fix-none" has no run`)
}

func TestAgentStream_AFollowStopsWhenItsContextEnds(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.agents.said = said()
	harness.agents.gate = make(chan struct{})
	engine := harness.engine(t)
	client := harness.client(t, testVersion)

	done := runInBackground(engine, testItem)
	waitOn(t, waiting[struct{}]{what: "the agent to run", run: runOfEngine(t, engine), read: func() (struct{}, bool, error) { return struct{}{}, harness.agents.running() == 1, nil }})

	ctx, stop := context.WithCancel(t.Context())
	got := &collected{}
	followed := make(chan error, 1)

	go func() { followed <- client.Follow(ctx, testItem, got.add) }()

	waitOn(t, waiting[struct{}]{what: "the entries", run: runOfEngine(t, engine), read: func() (struct{}, bool, error) { return struct{}{}, len(got.all()) == 3, nil }})
	stop()
	require.ErrorIs(t, receive(t, followed), context.Canceled)

	close(harness.agents.gate)
	require.NoError(t, receive(t, done).err)
}

func TestAgentStream_AStartAfterALostRunAppendsUnderTheNextTry(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.agents.said = said()
	nameSession(harness, "s-lost")
	loseAgent(t, harness, 33*time.Minute)
	nameSession(harness, "")

	_, err := harness.engine(t).Run(testItem, testProject)
	require.NoError(t, err)

	got := &collected{}
	require.NoError(t, harness.client(t, testVersion).Follow(t.Context(), testItem, got.add))

	var tries, seqs []int

	for _, entry := range got.all() {
		tries, seqs = append(tries, entry.Try), append(seqs, entry.Seq)
	}

	assert.Equal(t, []int{1, 1, 1, 2, 2, 2}, tries, "the entries of the start that came back carry the next try")
	assert.Equal(t, []int{1, 2, 3, 4, 5, 6}, seqs, "the numbers go on across the tries")
}

func TestAgentStream_AReplayOfARecordedPhaseWritesNoEntryAndStartsNoAgent(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", exited)
	rig.h.agents.said = said()

	file := writeFlow(t, replayFlow)
	prompt := filepath.Join(filepath.Dir(file), "prompts", "a.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(prompt), 0o750))
	require.NoError(t, os.WriteFile(prompt, []byte("Write to {{ .output_file }}\n"), 0o600))

	interrupted(t, rig.h, file)

	next := rig.h.engine(t)
	client := rig.h.client(t, testVersion)
	done := make(chan runOutcome, 1)

	go func() {
		result, err := next.RunFile(testItem, testProject, file, modeOff())
		done <- runOutcome{result: result, err: err}
	}()

	waitOn(t, waiting[struct{}]{what: "the open run to take the answer", run: runOfClient(t, client), read: func() (struct{}, bool, error) {
		return struct{}{}, client.Reply(testItem, Reply{Text: "yes"}) == nil, nil
	}})
	require.NoError(t, receive(t, done).err)

	got := &collected{}
	require.NoError(t, client.Follow(t.Context(), testItem, got.add))

	assert.Len(t, rig.h.agents.seen(), 1, "the replay started no agent")
	assert.Equal(t, []string{"LINE-ONE", "KEY [redacted]", "Bash: ls"}, got.texts(), "the replay wrote no second set of entries")
}

// recordedStreamer is a streamer whose writes go to a list. It writes nothing to the store.
func recordedStreamer(failAt int) (*streamer, *[]AgentEntry, *[]error) {
	var (
		written []AgentEntry
		warned  []error
	)

	write := func(entry AgentEntry) error {
		if failAt > 0 && len(written)+1 == failAt {
			return errors.New("the store is down")
		}

		written = append(written, entry)

		return nil
	}

	return newStreamer(write, agent.Redactor(), func(err error) { warned = append(warned, err) }), &written, &warned
}

func TestStreamer_WritesTheEntriesInOrderWithTheKindAndTheNumber(t *testing.T) {
	t.Parallel()

	s, written, _ := recordedStreamer(0)

	go s.run()

	for _, one := range said() {
		s.say(one)
	}

	s.finish()

	assert.Equal(t, []AgentEntry{{Seq: 1, Try: 1, Kind: EntryText, Text: "LINE-ONE"}, {Seq: 2, Try: 1, Kind: EntryText, Text: "KEY [redacted]"}, {Seq: 3, Try: 1, Kind: EntryCall, Text: "Bash: ls"}}, *written)
}

func TestStreamer_CutsTheStreamAtTheCapWithOneEntryAndDropsTheRest(t *testing.T) {
	t.Parallel()

	s, written, _ := recordedStreamer(0)

	go s.run()

	s.say(agent.Said{Text: strings.Repeat("a", maxStreamBytes-10)})
	s.say(agent.Said{Text: strings.Repeat("b", 11)})
	s.say(agent.Said{Text: "after the cut"})
	s.finish()

	require.Len(t, *written, 2, "the text under the cap, then one entry that says the stream was cut")
	assert.Equal(t, AgentEntry{Seq: 2, Try: 1, Kind: EntryText, Text: streamCutText}, (*written)[1])
}

func TestStreamer_ATryThatFollowsAnEarlierOneTakesTheNextTryTheNextNumberAndTheSizeOfTheText(t *testing.T) {
	t.Parallel()

	s, written, _ := recordedStreamer(0)
	s.resume([]AgentEntry{{Seq: 1, Try: 1, Text: strings.Repeat("a", maxStreamBytes-5)}, {Seq: 2, Try: 1, Text: "x"}})

	go s.run()

	s.say(agent.Said{Text: "123456"})
	s.finish()

	assert.Equal(t, []AgentEntry{{Seq: 3, Try: 2, Kind: EntryText, Text: streamCutText}}, *written, "the earlier text counts against the cap")
}

func TestStreamer_AnEarlierCutKeepsTheStreamCut(t *testing.T) {
	t.Parallel()

	s, written, _ := recordedStreamer(0)
	s.resume([]AgentEntry{{Seq: 1, Try: 1, Text: streamCutText}})

	go s.run()

	s.say(agent.Said{Text: "more"})
	s.finish()

	assert.Empty(t, *written)
}

func TestStreamer_AFailedWriteIsLoggedOnceAndTheLaterEntriesAreDropped(t *testing.T) {
	t.Parallel()

	s, written, warned := recordedStreamer(2)

	go s.run()

	for _, one := range said() {
		s.say(one)
	}

	s.finish()

	assert.Len(t, *written, 1, "the stream holds what came before the failure")
	assert.Len(t, *warned, 1, "the failure is logged once")
}

func TestOpenStream_AStepContextThatIsNoWorkflowStepLogsTheReadAndKeepsTheAgentGoing(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	engine := harness.engine(t)

	s := engine.openStream(context.Background(), PhaseInput{Item: testItem, Call: "agent:a#1"})
	s.say(agent.Said{Text: "one"})
	s.say(agent.Said{Text: "two"})
	s.finish()

	log, err := os.ReadFile(filepath.Join(harness.opts.StateDir, logFileName))
	require.NoError(t, err)
	assert.Equal(t, 2, strings.Count(string(log), "cannot keep the output of the agent in its stream"), "one line for the read and one for the first failed write")
}

func TestFollow_AClosedStoreIsAnErrorAtEachLook(t *testing.T) {
	t.Parallel()

	client := newHarness(t).client(t, testVersion)
	require.NoError(t, dbos.Shutdown(client.flow, time.Second))

	require.Error(t, client.Follow(t.Context(), "fix-a", func(FollowedEntry) {}), "the target is looked up first")

	_, err := client.followOnce(t.Context(), "item-fix-a-r1", map[string]bool{}, func(FollowedEntry) {})
	require.Error(t, err, "the look at the run")

	_, err = phasesOf(client.store(), "item-fix-a-r1")
	require.Error(t, err)

	asked := ""
	require.Error(t, client.announce("item-fix-a-r1", &asked, func(FollowedEntry) {}), "the read of the question")
}

func TestFollow_AClientWithNoStoreCannotReadAPhase(t *testing.T) {
	t.Parallel()

	require.Error(t, (&Client{}).readPhase(t.Context(), "phase", func(FollowedEntry) {}))
}

func TestDeliver_AReadThatFailedIsTheErrorAndAnEntryGoesToEmit(t *testing.T) {
	t.Parallel()

	var got []FollowedEntry

	emit := func(entry FollowedEntry) { got = append(got, entry) }

	require.ErrorIs(t, deliver("p1", dbos.StreamValue[AgentEntry]{Err: errNoStep}, emit), errNoStep)
	require.NoError(t, deliver("p1", dbos.StreamValue[AgentEntry]{Value: AgentEntry{Seq: 1, Text: "hi"}}, emit))
	assert.Equal(t, []FollowedEntry{{Phase: "p1", AgentEntry: AgentEntry{Seq: 1, Text: "hi"}}}, got)
}

func TestPause_EndsWithTheErrorOfAContextThatEndedAndWaitsOtherwise(t *testing.T) {
	t.Parallel()

	ctx, stop := context.WithCancel(t.Context())
	stop()

	require.ErrorIs(t, pause(ctx, time.Hour), context.Canceled)
	require.NoError(t, pause(t.Context(), time.Millisecond))
}

const twoPhaseFlow = `def main(item):
    a = agent(model = "m", prompt = "prompts/a.md", name = "a")
    b = agent(model = "m", prompt = "prompts/a.md", name = "b")
`

func TestAgentStream_AFollowOfARunWithTwoAgentCallsPrintsTheirPhasesInOrder(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", exited)
	rig.h.agents.said = said()

	_, err := rig.run(t, twoPhaseFlow, "Work.\n")
	require.NoError(t, err)

	got := &collected{}
	require.NoError(t, rig.h.client(t, testVersion).Follow(t.Context(), testItem, got.add))

	entries := got.all()
	require.Len(t, entries, 6)
	assert.Equal(t, entries[0].Phase, entries[2].Phase)
	assert.Equal(t, entries[3].Phase, entries[5].Phase)
	assert.NotEqual(t, entries[0].Phase, entries[3].Phase, "two calls, two phases")
	assert.Equal(t, []int{1, 2, 3, 1, 2, 3}, []int{entries[0].Seq, entries[1].Seq, entries[2].Seq, entries[3].Seq, entries[4].Seq, entries[5].Seq}, "each phase counts its own entries")
}

func TestStreamer_RedactsTheTargetOfAToolCallBeforeItIsWritten(t *testing.T) {
	t.Parallel()

	s, written, _ := recordedStreamer(0)

	go s.run()

	s.say(agent.Said{Call: true, Text: "Bash: curl -H sk-ant-abcdefghijklmnop0123 https://example.test"})
	s.finish()

	assert.Equal(t, []AgentEntry{{Seq: 1, Try: 1, Kind: EntryCall, Text: "Bash: curl -H [redacted] https://example.test"}}, *written)
}

func TestStreamer_KeepsASecretThatIsNotShapedLikeAnAnthropicKey(t *testing.T) {
	t.Parallel()

	s, written, _ := recordedStreamer(0)

	go s.run()

	const call = "Bash: export AWS_ACCESS_KEY_ID=AKIAIOSFODNN7EXAMPLE" //nolint:gosec // the documented example key of AWS, planted on purpose

	s.say(agent.Said{Call: true, Text: call})
	s.finish()

	assert.Equal(t, call, (*written)[0].Text, "the redactor of the engine has no list of the secrets of the run, and the help text says so")
}

func TestFollow_ARunThatEndedGivesAPhaseThatTheFollowHasNotReadBeforeItIsDone(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.agents.said = said()
	_, err := harness.engine(t).Run(testItem, testProject)
	require.NoError(t, err)

	client, got, read := harness.client(t, testVersion), &collected{}, map[string]bool{}

	done, err := client.followOnce(t.Context(), runID(testItem, 1), read, got.add)
	require.NoError(t, err)
	assert.False(t, done, "the run had ended, but this look found a phase that was not read")
	assert.Len(t, got.all(), 3)

	done, err = client.followOnce(t.Context(), runID(testItem, 1), read, got.add)
	require.NoError(t, err)
	assert.True(t, done, "the next look finds no new phase")
	assert.Len(t, got.all(), 3, "a phase is read once")
}

func TestAgentStream_AFollowOfARunThatWaitsForAnAnswerSaysSoOnceAndEndsWithTheRun(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", exited)

	done := runInBackground2(rig.h.engine(t), flowFile(t, replayFlow, "Work.\n"))
	client, got, followed := rig.h.client(t, testVersion), &collected{}, make(chan error, 1)

	go func() {
		err := errNoSuchRun
		for errors.Is(err, errNoSuchRun) {
			err = client.Follow(t.Context(), testItem, got.add)
			time.Sleep(10 * time.Millisecond)
		}

		followed <- err
	}()

	waits := func() []string {
		var texts []string

		for _, entry := range got.all() {
			if entry.Kind == EntryWait {
				texts = append(texts, entry.Text)
			}
		}

		return texts
	}

	waitOn(t, condition(func() bool { return len(waits()) > 0 }))
	time.Sleep(2500 * time.Millisecond)
	assert.Equal(t, []string{"go?"}, waits(), "the line comes once, however long the run waits")

	require.NoError(t, client.Reply(testItem, Reply{Text: "yes"}))
	require.NoError(t, receive(t, done).err)
	require.NoError(t, receive(t, followed))
}
