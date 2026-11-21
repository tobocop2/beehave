package flow

import (
	"encoding/json"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
)

func TestWaitDeadline_ReadsTheDeadlineOfTheLastReceiveRecordOnlyForAWaitWithATimeout(t *testing.T) {
	t.Parallel()

	start := time.Now()
	hour := start.Add(time.Hour).UnixMilli()
	window := start.Add(receiveWindow).UnixMilli()
	sleep := func(output any) []dbos.StepInfo {
		return []dbos.StepInfo{{StepName: "login#1"}, {StepName: sleepStep, StartedAt: start, Output: output}}
	}

	assert.Equal(t, time.UnixMilli(hour), waitDeadline(sleep(strconv.FormatInt(hour, 10)), "q"), "a timed wait")
	assert.Equal(t, time.UnixMilli(hour), waitDeadline(sleep(hour), "q"), "a number output")
	assert.True(t, waitDeadline(sleep(strconv.FormatInt(window, 10)), "q").IsZero(), "a wait with no timeout receives in windows")
	assert.True(t, waitDeadline(sleep(strconv.FormatInt(hour, 10)), "").IsZero(), "a sleep of a run with no question")
	assert.True(t, waitDeadline(sleep("soon"), "q").IsZero(), "an output that is not a time")
	assert.True(t, waitDeadline(nil, "q").IsZero(), "a run with no record")
	assert.True(t, waitDeadline([]dbos.StepInfo{{StepName: sleepStep, Output: "1"}, {StepName: "DBOS.recv"}}, "q").IsZero(), "an answered receive")
}

func TestWaitDeadline_ADeadlineOneMinuteBelowTheReceiveWindowIsNoDeadlineAndOneMillisecondLessIsOne(t *testing.T) {
	t.Parallel()

	start := time.UnixMilli(1_700_000_000_000)
	sleep := func(at time.Time) []dbos.StepInfo {
		return []dbos.StepInfo{{StepName: sleepStep, StartedAt: start, Output: strconv.FormatInt(at.UnixMilli(), 10)}}
	}

	edge := start.Add(receiveWindow - time.Minute)

	assert.True(t, waitDeadline(sleep(edge), "q").IsZero(), "exactly at the edge counts as a window of a wait with no timeout")
	assert.Equal(t, edge.Add(-time.Millisecond), waitDeadline(sleep(edge.Add(-time.Millisecond)), "q"), "just under the edge is a deadline")
}

func TestTimeLeft_NamesTheLargestUnitsOfTheTimeToTheDeadline(t *testing.T) {
	t.Parallel()

	for left, want := range map[time.Duration]string{
		-time.Second:                    "deadline passed",
		0:                               "deadline passed",
		45 * time.Second:                "45s left",
		12*time.Minute + 30*time.Second: "12m left",
		90 * time.Minute:                "1h30m left",
		50 * time.Hour:                  "2d2h left",
	} {
		assert.Equal(t, want, TimeLeft(left), left.String())
	}
}

func TestRunProgress_NotesAtAddsTheTimeLeftToTheDeadlineAndNotesLeavesItOut(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	progress := RunProgress{Deadline: now.Add(time.Hour)}

	assert.Equal(t, []string{"answer by 2026-11-03T11:00:00Z, 1h0m left"}, progress.NotesAt(now))
	assert.Equal(t, []string{"answer by 2026-11-03T11:00:00Z"}, progress.Notes())
}

func TestRunProgress_NotesNameTheGateWithNoHeadAndTheLabel(t *testing.T) {
	t.Parallel()

	progress := RunProgress{Gate: &GateSeen{Passed: true}, Label: "review 1/3"}

	assert.Equal(t, []string{"gate pass", "review 1/3"}, progress.Notes())
	assert.Equal(t, []string{"spent $1.5"}, RunProgress{Spent: agent.Spend{Cost: 1.5}}.Notes())
	assert.Equal(t, []string{"gate fail at abc"}, RunProgress{Gate: &GateSeen{Head: "abc"}}.Notes())
}

const twoAgentsFlow = "def main(item):\n" +
	"    agent(model = \"m\", prompt = \"prompts/a.md\", name = \"a\")\n" +
	"    agent(model = \"m\", prompt = \"prompts/a.md\", name = \"b\")\n"

func onlyRun(t *testing.T, client *Client) RunInfo {
	t.Helper()

	infos, err := client.Status()
	require.NoError(t, err)
	require.Len(t, infos, 1)

	return infos[0]
}

func TestStatus_ARunShowsTheCostSpentSoFarAgainstTheRunCap(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.h.opts.MaxRunBudget = 20
	rig.reply("", costing(1.25))

	_, err := rig.run(t, twoAgentsFlow, "x")
	require.NoError(t, err)

	info := onlyRun(t, rig.h.client(t, testVersion))

	assert.InDelta(t, 2.5, info.Spent.Cost, 0)
	assert.InDelta(t, 20.0, info.Cap, 0)
	assert.Contains(t, info.Notes(), "spent $2.5 of $20")
}

func TestStatus_TheCostOfARunThatFailedCountsAsSpent(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	failed := costing(0.75)
	failed.ExitCode = 1
	rig.reply("", failed)

	_, err := rig.run(t, twoAgentsFlow, "x")
	require.Error(t, err)

	info := onlyRun(t, rig.h.client(t, testVersion))

	assert.InDelta(t, 0.75, info.Spent.Cost, 0)
	assert.Contains(t, info.Notes(), "spent $0.75")
}

func TestStatus_ARunWithNoCostShowsNoCostNote(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", exited)

	_, err := rig.run(t, twoAgentsFlow, "x")
	require.NoError(t, err)

	assert.Empty(t, onlyRun(t, rig.h.client(t, testVersion)).Notes())
}

func TestStatus_AnOpenRunNamesTheStepThatRunsNow(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.agents.gate = make(chan struct{})
	release := sync.OnceFunc(func() { close(harness.agents.gate) })
	t.Cleanup(release)
	engine := harness.engine(t)
	client := harness.client(t, testVersion)

	done := runInBackground(engine, testItem)
	waitOn(t, waiting[struct{}]{what: "the agent to run", run: runOfEngine(t, engine), read: func() (struct{}, bool, error) { return struct{}{}, harness.agents.running() == 1, nil }})

	info := onlyRun(t, client)
	assert.Equal(t, "agent:work#1", info.Now)
	assert.Contains(t, info.Notes(), "now agent:work#1")

	release()
	require.NoError(t, receive(t, done).err)

	assert.Empty(t, onlyRun(t, client).Now, "a finished run runs no step")
}

// ownerAsksWithADeadline is the fact that the open run shows its question and the deadline of its wait. The run sets the question before the receive records its deadline, so a wait on the question alone can read the run in between.
func ownerAsksWithADeadline(t *testing.T, client *Client) waiting[struct{}] {
	t.Helper()

	read := newStatusRead(client)

	return waiting[struct{}]{what: "the open run to show a question and a deadline", run: runOfClient(t, client), last: read.last, read: func() (struct{}, bool, error) {
		infos := read.next()

		return struct{}{}, read.err == nil && len(infos) == 1 && infos[0].Question != "" && !infos[0].Deadline.IsZero(), nil
	}}
}

func TestStatus_ARunThatWaitsWithADeadlineShowsTheQuestionAndTheDeadline(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.agents.gate = make(chan struct{})
	release := sync.OnceFunc(func() { close(harness.agents.gate) })
	t.Cleanup(release)
	harness.flowIs(t, "def main(item):\n    wait(name = \"ask\", question = \"What now?\", timeout = \"1h\")\n    agent(name = \"work\", prompt = \"prompts/work.md\", model = \"m\")\n")
	engine := harness.engine(t)
	client := harness.client(t, testVersion)

	before := time.Now()
	done := runInBackground(engine, testItem)
	waitOn(t, ownerAsksWithADeadline(t, client))

	info := onlyRun(t, client)
	assert.Equal(t, "What now?", info.Question)
	assert.WithinDuration(t, before.Add(time.Hour), info.Deadline, time.Minute)
	assert.Contains(t, info.Notes(), "answer by "+info.Deadline.Format(time.RFC3339))

	view, err := client.Steps(testRun)
	require.NoError(t, err)
	assert.Equal(t, info.Deadline, view.Deadline, "the run view shows the same deadline")

	require.NoError(t, client.Reply(testItem, Reply{Text: "go on"}))
	waitOn(t, waiting[struct{}]{what: "the agent of the answered run to start", run: runOfClient(t, client), read: func() (struct{}, bool, error) {
		return struct{}{}, harness.agents.running() == 1, nil
	}})

	info = onlyRun(t, client)
	assert.Equal(t, runRunning, info.State, "the run is still open")
	assert.Empty(t, info.Question)
	assert.True(t, info.Deadline.IsZero(), "an answered wait has no deadline while the run is open")

	view, err = client.Steps(testRun)
	require.NoError(t, err)
	assert.True(t, view.Deadline.IsZero())

	release()
	require.NoError(t, receive(t, done).err)
}

func TestRunInfo_WaitingIsAnOpenRunWithAQuestion(t *testing.T) {
	t.Parallel()

	assert.True(t, RunInfo{State: runRunning, Question: "Q?"}.Waiting())
	assert.True(t, RunInfo{State: runQueued, Question: "Q?"}.Waiting())
	assert.False(t, RunInfo{State: runRunning}.Waiting(), "no question")
	assert.False(t, RunInfo{State: runDone, Question: "Q?"}.Waiting(), "a finished run")
}

func TestStatus_ARunThatWaitsWithNoDeadlineShowsNone(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.flowIs(t, "def main(item):\n    r = wait(name = \"ask\", question = \"What now?\")\n    if r.answer != \"go on\":\n        fail(\"WRONG ANSWER\")\n")
	engine := harness.engine(t)
	client := harness.client(t, testVersion)

	done := runInBackground(engine, testItem)
	waitOn(t, ownerAsks(t, harness))
	waitOn(t, inASleep(t, engine))

	info := onlyRun(t, client)
	assert.True(t, info.Deadline.IsZero())

	require.NoError(t, client.Reply(testItem, Reply{Text: "go on"}))
	require.NoError(t, receive(t, done).err)
}

func TestStatus_ARunShowsTheLastGateResultAndTheHeadItJudged(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply(`{"verdict":"approve"}`, exited)
	rig.h.commands.reply = lintRCs(3)
	rig.h.gate.results = []GateResult{{Passed: true, Head: "abc1234"}}

	_, err := rig.run(t, stepsFlow, "Write to {{ .output_file }}\n")
	require.NoError(t, err)

	info := onlyRun(t, rig.h.client(t, testVersion))

	require.NotNil(t, info.Gate)
	assert.True(t, info.Gate.Passed)
	assert.Equal(t, "abc1234", info.Gate.Head)
	assert.Contains(t, info.Notes(), "gate pass at abc1234")
}

func TestStatus_ARunWithNoGateShowsNoGate(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", exited)

	_, err := rig.run(t, twoAgentsFlow, "x")
	require.NoError(t, err)

	assert.Nil(t, onlyRun(t, rig.h.client(t, testVersion)).Gate)
}

const labelsFlow = "def main(item):\n" +
	"    agent(model = \"m\", prompt = \"prompts/a.md\", label = \"review 1/6\", name = \"review\")\n" +
	"    agent(model = \"m\", prompt = \"prompts/a.md\", label = \"fix 1/5\", name = \"fix\")\n" +
	"    agent(model = \"m\", prompt = \"prompts/a.md\", name = \"plain\")\n"

func TestStatus_ARunShowsTheLabelOfTheLatestLabelledAgentStep(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", exited)

	_, err := rig.run(t, labelsFlow, "x")
	require.NoError(t, err)

	info := onlyRun(t, rig.h.client(t, testVersion))

	assert.Equal(t, "fix 1/5", info.Label, "the call with no label leaves the label of the step before it")
	assert.Contains(t, info.Notes(), "fix 1/5")
}

func TestStatus_AnAgentCallWithNoLabelShowsNoLabel(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", exited)

	_, err := rig.run(t, twoAgentsFlow, "x")
	require.NoError(t, err)

	info := onlyRun(t, rig.h.client(t, testVersion))

	assert.Empty(t, info.Label)
}

func TestStatus_ALabelThatIsNotTextIsRefused(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", exited)

	_, err := rig.run(t, agentFlow(`label = 5, `), "x")

	require.ErrorContains(t, err, "label")
	assert.Empty(t, rig.h.agents.seen())
}

func TestStatus_TheJSONViewOfARunCarriesTheSameFigures(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.h.opts.MaxRunBudget = 20
	rig.reply(`{"verdict":"approve"}`, costing(1.25))
	rig.h.commands.reply = lintRCs(3)

	_, err := rig.run(t, stepsFlow, "Write to {{ .output_file }}\n")
	require.NoError(t, err)

	view, err := rig.h.client(t, testVersion).Steps(testRun)
	require.NoError(t, err)

	text, err := json.Marshal(view)
	require.NoError(t, err)

	var fields struct {
		Spent struct {
			Cost float64 `json:"cost_usd"`
		} `json:"spent"`
		Cap  float64 `json:"cap_usd"`
		Gate struct {
			Passed bool `json:"passed"`
		} `json:"gate"`
	}

	require.NoError(t, json.Unmarshal(text, &fields))

	assert.InDelta(t, 1.25, fields.Spent.Cost, 0)
	assert.InDelta(t, 20.0, fields.Cap, 0)
	assert.True(t, fields.Gate.Passed)
}
