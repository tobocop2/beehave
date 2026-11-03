package flow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/agent"
)

// lostRun starts the agent step of the default workflow on an engine, stops the engine while the agent runs, and makes the claim store
// report a last renewal of the claim lease ran after the start. It returns the harness, whose agent gate is closed.
func lostRun(t *testing.T, ran time.Duration) *harness {
	t.Helper()

	harness := newHarness(t)
	loseAgent(t, harness, ran)

	return harness
}

// loseAgent starts an engine on the harness, waits until its agent is in flight, stops the engine while the agent runs,
// and makes the claim store report a last renewal of the claim lease ran after that start. A zero ran makes it report no renewal.
func loseAgent(t *testing.T, harness *harness, ran time.Duration) {
	t.Helper()

	harness.agents.gate = make(chan struct{})
	harness.migrated(t)

	engine, err := NewEngine(t.Context(), harness.opts, harness.deps())
	require.NoError(t, err)
	require.NoError(t, engine.Launch())

	_ = runInBackground(engine, testItem)
	eventually(t, func() bool { return harness.agents.running() == 1 })

	// The start mark is recorded before the agent starts, so a time after the agent started is after the mark.
	renewed := time.Time{}
	if ran != 0 {
		renewed = time.Now().Add(ran)
	}

	harness.beats.setRenewed(renewed)

	shutdownOpeningGate(t, engine, harness.agents.gate)
}

func TestPhase_AFirstStartOfAnAgentStepGetsNoNote(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	engine := harness.engine(t)

	_, err := engine.Run(testItem, testProject)
	require.NoError(t, err)

	requests := harness.agents.seen()
	require.Len(t, requests, 1)
	assert.Equal(t, "Work on the item.\n", requests[0].Prompt, "a first start runs the prompt as written")
}

func TestPhase_ASecondStartOfAnAgentStepGetsTheNoteOnce(t *testing.T) {
	t.Parallel()

	harness := lostRun(t, 33*time.Minute)

	next := harness.engine(t)
	_, err := next.Run(testItem, testProject)
	require.NoError(t, err)

	requests := harness.agents.seen()
	require.Len(t, requests, 2)
	assert.Equal(t, "Work on the item.\n", requests[0].Prompt)
	assert.Contains(t, requests[1].Prompt, "Work on the item.\n")
	assert.Contains(t, requests[1].Prompt, "This step ran before and was lost after 33 minutes.")
	assert.Contains(t, requests[1].Prompt, "git status")
	assert.Contains(t, requests[1].Prompt, "git diff")
	assert.Equal(t, 1, countOfText(requests[1].Prompt, "This step ran before"), "the note is added once")
}

func TestPhase_AFailedAgentStepThatResumesGetsNoNote(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.agents.result = func(AgentRequest) agent.Result {
		return agent.Result{Outcome: agent.OutcomeExited, ExitCode: 3, Note: "bad exit"}
	}
	engine := harness.engine(t)
	client := harness.client(t, testVersion)

	_, err := engine.Run(testItem, testProject)
	require.Error(t, err)

	harness.agents.mu.Lock()
	harness.agents.result = nil
	harness.agents.mu.Unlock()

	_, err = resumeAndJoin(t, engine, client)
	require.NoError(t, err)

	requests := harness.agents.seen()
	require.Len(t, requests, 2)
	assert.Equal(t, "Work on the item.\n", requests[1].Prompt, "a step that failed with an exit was not lost, so it gets no note")
}

func TestPhase_ALostStepIsLoggedWithItsStartAndItsRunTime(t *testing.T) {
	t.Parallel()

	harness := lostRun(t, 33*time.Minute)

	next := harness.engine(t)
	_, err := next.Run(testItem, testProject)
	require.NoError(t, err)

	log, err := os.ReadFile(filepath.Join(harness.opts.StateDir, logFileName))
	require.NoError(t, err)
	assert.Contains(t, string(log), "the agent step was lost")
	assert.Contains(t, string(log), "item="+testItem)
	assert.Contains(t, string(log), "ran=33m")
	assert.Contains(t, string(log), "the process ended with no record")
}

func TestClient_TheLostStepIsListedWithItsRecordAsTheOutput(t *testing.T) {
	t.Parallel()

	harness := lostRun(t, 33*time.Minute)

	next := harness.engine(t)
	_, err := next.Run(testItem, testProject)
	require.NoError(t, err)

	client := harness.client(t, testVersion)
	phases, err := dbos.ListWorkflows(client.flow, dbos.WithFilterName(phaseName))
	require.NoError(t, err)
	require.NotEmpty(t, phases)

	var found []dbos.StepInfo

	for _, phase := range phases {
		steps, err := dbos.GetWorkflowSteps(client.flow, phase.ID, dbos.WithStepsLoadOutput(true))
		require.NoError(t, err)

		for _, step := range steps {
			if step.StepName == lostStepPrefix+"1" {
				found = append(found, step)
			}
		}
	}

	require.Len(t, found, 1)
	t.Logf("output type of a step on the client: %T", found[0].Output)

	raw, isText := found[0].Output.(string)
	require.True(t, isText, "the client returns the stored JSON text, got %T", found[0].Output)

	var record LostStep

	require.NoError(t, json.Unmarshal([]byte(raw), &record))
	assert.Equal(t, 33, int(record.Ran().Minutes()))
	assert.Equal(t, noRecord, record.Cause)
}

func TestLostNote_NamesTheMinutesAndSaysUnderOneMinuteForAShortRun(t *testing.T) {
	t.Parallel()

	assert.Contains(t, LostStep{Started: time.Unix(0, 0), Lost: time.Unix(0, 0).Add(90 * time.Second)}.note(), "after 1 minute.")
	assert.Contains(t, LostStep{Started: time.Unix(0, 0), Lost: time.Unix(0, 0).Add(5 * time.Second)}.note(), "after less than a minute.")
	assert.Equal(t, time.Duration(0), LostStep{Started: time.Unix(100, 0), Lost: time.Unix(0, 0)}.Ran(), "a loss before the start counts as no time")
	assert.Equal(t, time.Duration(0), LostStep{Started: time.Unix(100, 0)}.Ran(), "a loss with no time counts as no time")
	assert.False(t, LostStep{Started: time.Unix(100, 0)}.Known())
	assert.Contains(t, LostStep{Started: time.Unix(100, 0)}.note(), "ran before for an unknown time and was lost.")
}

func TestPhase_TheLossTimeIsTheLastRenewalOfTheClaimLeaseInTheStore(t *testing.T) {
	t.Parallel()

	harness := lostRun(t, 7*time.Minute)

	_, err := harness.engine(t).Run(testItem, testProject)
	require.NoError(t, err)

	requests := harness.agents.seen()
	require.Len(t, requests, 2)
	assert.Contains(t, requests[1].Prompt, "was lost after 7 minutes.", "the run time comes from the renewal that the store reports")
}

func TestPhase_AClaimWithNoLeaseGivesAnUnknownRunTimeInTheNoteTheRecordAndTheLog(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	loseAgent(t, harness, 0)

	_, err := harness.engine(t).Run(testItem, testProject)
	require.NoError(t, err)

	requests := harness.agents.seen()
	require.Len(t, requests, 2)
	assert.Contains(t, requests[1].Prompt, "ran before for an unknown time and was lost")

	lost := lostSteps(t, harness.client(t, testVersion))
	require.Len(t, lost, 1)

	log, err := os.ReadFile(filepath.Join(harness.opts.StateDir, logFileName))
	require.NoError(t, err)
	assert.Contains(t, string(log), "ran=unknown")
}

func TestPhase_ThePromptGetsTheNoteOfTheLatestLossAndNotOneNoteForEachStart(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	loseAgent(t, harness, 3*time.Minute)
	loseAgent(t, harness, 9*time.Minute)

	_, err := harness.engine(t).Run(testItem, testProject)
	require.NoError(t, err)

	requests := harness.agents.seen()
	require.Len(t, requests, 3)
	assert.Equal(t, "Work on the item.\n", requests[0].Prompt)
	assert.Equal(t, 1, countOfText(requests[1].Prompt, "This step ran before"))
	assert.Equal(t, 1, countOfText(requests[2].Prompt, "This step ran before"), "the third start gets one note, not one for each earlier start")
	assert.Contains(t, requests[2].Prompt, "was lost after 9 minutes.")
}

func TestPhase_ACanceledAgentStepThatResumesInTheSameEngineGetsNoNoteAndNoLostRecord(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.agents.gate = make(chan struct{})
	engine := harness.engine(t)
	client := harness.client(t, testVersion)

	done := runInBackground(engine, testItem)
	eventually(t, func() bool { return harness.agents.running() == 1 })
	require.NoError(t, client.Cancel(testItem))
	harness.tick(t)
	<-done

	close(harness.agents.gate)
	_, err := resumeAndJoin(t, engine, client)
	require.NoError(t, err)

	requests := harness.agents.seen()
	require.Len(t, requests, 2)
	assert.Equal(t, "Work on the item.\n", requests[1].Prompt, "a step that the user canceled was not lost, so it gets no note")
	assert.Empty(t, lostSteps(t, client), "a cancel leaves no lost record")
}

// lostSteps returns the steps named agent/lost<n> of every phase in the store.
func lostSteps(t *testing.T, client *Client) []dbos.StepInfo {
	t.Helper()

	phases, err := dbos.ListWorkflows(client.flow, dbos.WithFilterName(phaseName))
	require.NoError(t, err)

	var found []dbos.StepInfo

	for _, phase := range phases {
		steps, err := dbos.GetWorkflowSteps(client.flow, phase.ID)
		require.NoError(t, err)

		for _, step := range steps {
			if strings.HasPrefix(step.StepName, lostStepPrefix) {
				found = append(found, step)
			}
		}
	}

	return found
}

func countOfText(text, part string) int {
	return strings.Count(text, part)
}

func TestPhase_ALostStartThatFailsAndResumesLeavesNoOldLossToDerive(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	loseAgent(t, harness, 3*time.Minute)

	harness.agents.mu.Lock()
	harness.agents.result = func(AgentRequest) agent.Result {
		return agent.Result{Outcome: agent.OutcomeExited, ExitCode: 3, Note: "bad exit"}
	}
	harness.agents.mu.Unlock()

	engine := harness.engine(t)
	_, err := engine.Run(testItem, testProject)
	require.Error(t, err)

	harness.agents.mu.Lock()
	harness.agents.result = nil
	harness.agents.mu.Unlock()

	// The fresh claim of the resume holds a lease that began at the resume, long after the first start.
	harness.beats.setRenewed(time.Now().Add(50 * time.Minute))

	client := harness.client(t, testVersion)
	_, err = resumeAndJoin(t, engine, client)
	require.NoError(t, err)

	requests := harness.agents.seen()
	require.Len(t, requests, 3)
	assert.Contains(t, requests[1].Prompt, "lost after 3 minutes")
	assert.NotContains(t, requests[2].Prompt, "lost after", "the resumed phase starts clean: the old loss is not read again")
	assert.Empty(t, lostSteps(t, client), "the rewind dropped the record of the old loss, and the resumed phase has no loss")
}
