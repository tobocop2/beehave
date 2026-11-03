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
)

func TestWaitDeadline_ReadsTheDeadlineOfTheLastReceiveRecordOnlyForAWaitWithATimeout(t *testing.T) {
	t.Parallel()

	start := time.Now()
	hour := start.Add(time.Hour).UnixMilli()
	window := start.Add(ownerWaitLimit).UnixMilli()
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

func TestStatus_ARoundsCapThatTheRunComputesIsCheckedByTheRun(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"0", `"5"`, "1 << 40", "1 << 70"} {
		rig := newAgentRig(t)
		rig.reply("", exited)

		_, err := rig.run(t, "def main(item):\n    n = "+value+"\n    agent(model = \"m\", prompt = \"prompts/a.md\", rounds = n, name = \"a\")\n", "x")

		require.ErrorContains(t, err, "rounds must be a whole number of 1 or more", value)
		assert.Empty(t, rig.h.agents.seen(), value)
	}
}

func TestRunProgress_NotesNameTheGateWithNoHeadAndRoundsWithNoCap(t *testing.T) {
	t.Parallel()

	progress := RunProgress{Gate: &GateSeen{Passed: true}, Fix: Rounds{Used: 2}, Review: Rounds{Used: 1, Cap: 3}}

	assert.Equal(t, []string{"gate pass", "fix 2", "review 1 of 3"}, progress.Notes())
	assert.Equal(t, []string{"spent $1.5"}, RunProgress{Spent: 1.5}.Notes())
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

	assert.InDelta(t, 2.5, info.Spent, 0)
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

	assert.InDelta(t, 0.75, info.Spent, 0)
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
	eventually(t, func() bool { return harness.agents.running() == 1 })

	info := onlyRun(t, client)
	assert.Equal(t, "agent:work#1", info.Now)
	assert.Contains(t, info.Notes(), "now agent:work#1")

	release()
	require.NoError(t, (<-done).err)

	assert.Empty(t, onlyRun(t, client).Now, "a finished run runs no step")
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
	waitForTheOwner(t, harness)

	info := onlyRun(t, client)
	assert.Equal(t, "What now?", info.Question)
	assert.WithinDuration(t, before.Add(time.Hour), info.Deadline, time.Minute)
	assert.Contains(t, info.Notes(), "answer by "+info.Deadline.Format(time.RFC3339))

	view, err := client.Steps(testRun)
	require.NoError(t, err)
	assert.Equal(t, info.Deadline, view.Deadline, "the run view shows the same deadline")

	require.NoError(t, client.Answer(testItem, "go on"))
	eventually(t, func() bool { return harness.agents.running() == 1 })

	info = onlyRun(t, client)
	assert.Equal(t, runRunning, info.State, "the run is still open")
	assert.Empty(t, info.Question)
	assert.True(t, info.Deadline.IsZero(), "an answered wait has no deadline while the run is open")

	view, err = client.Steps(testRun)
	require.NoError(t, err)
	assert.True(t, view.Deadline.IsZero())

	release()
	require.NoError(t, (<-done).err)
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
	waitForTheOwner(t, harness)

	info := onlyRun(t, client)
	assert.True(t, info.Deadline.IsZero())

	require.NoError(t, client.Answer(testItem, "go on"))
	require.NoError(t, (<-done).err)
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

const roundsFlow = "def main(item):\n" +
	"    agent(model = \"m\", prompt = \"prompts/a.md\", lane = \"review\", rounds = 6, name = \"review\")\n" +
	"    agent(model = \"m\", prompt = \"prompts/a.md\", lane = \"rework\", rounds = 5, name = \"fix\")\n" +
	"    agent(model = \"m\", prompt = \"prompts/a.md\", lane = \"review\", rounds = 6, name = \"review\")\n"

func TestStatus_ARunShowsTheFixAndReviewRoundsUsedAgainstTheirCaps(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", exited)

	_, err := rig.run(t, roundsFlow, "x")
	require.NoError(t, err)

	info := onlyRun(t, rig.h.client(t, testVersion))

	assert.Equal(t, Rounds{Used: 1, Cap: 5}, info.Fix)
	assert.Equal(t, Rounds{Used: 2, Cap: 6}, info.Review)
	assert.Contains(t, info.Notes(), "fix 1 of 5")
	assert.Contains(t, info.Notes(), "review 2 of 6")
}

func TestStatus_AnAgentCallWithNoRoundsCapShowsNoRounds(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", exited)

	_, err := rig.run(t, twoAgentsFlow, "x")
	require.NoError(t, err)

	info := onlyRun(t, rig.h.client(t, testVersion))

	assert.Equal(t, Rounds{}, info.Fix)
	assert.Equal(t, Rounds{}, info.Review)
}

func TestStatus_ARoundsCapBelowOneIsRefused(t *testing.T) {
	t.Parallel()

	for _, extra := range []string{`rounds = 0, `, `rounds = -2, `, `rounds = "5", `} {
		rig := newAgentRig(t)
		rig.reply("", exited)

		_, err := rig.run(t, agentFlow(extra), "x")

		require.Error(t, err, extra)
		assert.Contains(t, err.Error(), "rounds", extra)
		assert.Empty(t, rig.h.agents.seen(), extra)
	}
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
		Spent float64 `json:"spent_usd"`
		Cap   float64 `json:"cap_usd"`
		Gate  struct {
			Passed bool `json:"passed"`
		} `json:"gate"`
	}

	require.NoError(t, json.Unmarshal(text, &fields))

	assert.InDelta(t, 1.25, fields.Spent, 0)
	assert.InDelta(t, 20.0, fields.Cap, 0)
	assert.True(t, fields.Gate.Passed)
}
