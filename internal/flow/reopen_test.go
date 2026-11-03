package flow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/agent"
)

func TestResume_AFailedRunContinuesFromItsFailedStepAndRunRefusesUntilThen(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.agents.err = errFake
	engine := harness.engine(t)
	client := harness.client(t, testVersion)

	_, err := engine.Run(testItem, testProject)
	require.ErrorContains(t, err, "fake failure")

	_, err = engine.Run(testItem, testProject)
	require.ErrorIs(t, err, errRunFailed)
	require.ErrorContains(t, err, "fake failure")
	require.ErrorContains(t, err, "Run obee resume 12")

	infos, err := client.Status()
	require.NoError(t, err)
	require.Len(t, infos, 1, "a failed run starts no new attempt")

	harness.agents.mu.Lock()
	harness.agents.err = nil
	harness.agents.mu.Unlock()

	result, err := resumeAndJoin(t, engine, client)
	require.NoError(t, err)
	assert.Equal(t, ItemResult{Item: testItem}, result)

	infos, err = client.Status()
	require.NoError(t, err)
	require.Len(t, infos, 1)
	assert.Equal(t, runDone, infos[0].State)
	assert.Len(t, harness.agents.seen(), 2, "the agent runs again after its failure")
}

func TestResume_AFailedRunWithNoFailedStepReplaysFromTheEndOfItsHistory(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	file := writeFlow(t, crashFlow)
	interrupted(t, harness, file)
	require.NoError(t, os.WriteFile(file, []byte(crashFlow+"# edited\n"), 0o600)) // the replay refuses the changed file: the failure is no step.

	_, err := runFileWithin(t, harness.engine(t), file)
	require.ErrorContains(t, err, "changed since the run started")

	require.NoError(t, harness.client(t, testVersion).Resume(testItem))
}

func TestReopen_ReportsAStoreThatCannotBeRead(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)

	client, err := OpenClient(t.Context(), harness.opts, ReleaseDeps{})
	require.NoError(t, err)

	_, err = client.row("item-1-r1")
	require.ErrorIs(t, err, errNoRow)

	require.NoError(t, client.Close())

	_, err = client.row("item-1-r1")
	require.ErrorContains(t, err, "read workflow item-1-r1")

	require.ErrorContains(t, client.reopen("item-1-r1"), "read the steps of item-1-r1")

	_, err = client.reopenChild("child", nil)
	require.ErrorContains(t, err, "read workflow child")
	require.ErrorContains(t, client.rewind("item-1-r1", 0), "read workflow item-1-r1")
}

func TestRaised_SkipsAGatherRecordThatIsNotJSON(t *testing.T) {
	t.Parallel()

	steps := []dbos.StepInfo{
		{StepName: "gather:a#1", Output: "not json"},
		{StepName: "gather:b#2", Output: `{"canceled":["item-1-r1"]}`},
	}

	assert.Equal(t, map[string]bool{"item-1-r1": true}, raised(steps))
}

func TestReopen_ReportsACanceledItemWhoseStepsCannotBeRead(t *testing.T) {
	t.Parallel()

	client, err := OpenClient(t.Context(), newHarness(t).opts, ReleaseDeps{})
	require.NoError(t, err)
	require.NoError(t, client.Close())

	require.ErrorContains(t, client.reopenCanceledPhases("item-1-r1"), "read the steps of item-1-r1")
	require.ErrorContains(t, client.reopenAgain(dbos.WorkflowStatus{ID: "item-1-r1", Status: dbos.WorkflowStatusCancelled}), "read the steps of item-1-r1")
}

func TestReopen_ReportsACanceledItemWhoseCanceledPhaseIsGone(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.agents.err = errFake
	engine := harness.engine(t)
	client := harness.client(t, testVersion)

	_, err := engine.Run(testItem, testProject)
	require.Error(t, err)

	steps, err := dbos.GetWorkflowSteps(client.flow, "item-12-r1")
	require.NoError(t, err)

	var children []string

	for _, step := range steps {
		if step.ChildWorkflowID != "" {
			children = append(children, step.ChildWorkflowID)
		}
	}

	require.NotEmpty(t, children)
	require.NoError(t, dbos.DeleteWorkflows(client.flow, children))

	require.ErrorIs(t, client.reopenCanceledPhases("item-12-r1"), errNoRow)
	require.ErrorIs(t, client.reopenAgain(dbos.WorkflowStatus{ID: "item-12-r1", Status: dbos.WorkflowStatusCancelled}), errNoRow)
}

func TestReopen_ReportsAPhaseWhoseInputIsNotReadable(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	client := harness.client(t, testVersion)

	_, err := dbos.Enqueue[any](client.flow, queueName, phaseName, "not a phase", dbos.WithEnqueueWorkflowID("phase-x"), dbos.WithEnqueueQueuePartitionKey(testLogin))
	require.NoError(t, err)

	require.ErrorContains(t, client.rewind("phase-x", 0), "read the input of phase-x")
}

func TestResume_AFailedRunWhoseFailedPhaseIsGoneReportsIt(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.agents.err = errFake
	engine := harness.engine(t)
	client := harness.client(t, testVersion)

	_, err := engine.Run(testItem, testProject)
	require.Error(t, err)

	steps, err := dbos.GetWorkflowSteps(client.flow, "item-12-r1")
	require.NoError(t, err)

	var children []string

	for _, step := range steps {
		if step.ChildWorkflowID != "" {
			children = append(children, step.ChildWorkflowID)
		}
	}

	require.NotEmpty(t, children)
	require.NoError(t, dbos.DeleteWorkflows(client.flow, children))

	require.ErrorIs(t, client.Resume(testItem), errNoRow)
	assert.Equal(t, []string{"owner-12", "owner-12#2"}, harness.life.releasedBy(), "a resume that cannot reopen the run gives its fresh claim back")
}

func TestResume_AFinishedPhaseDoesNotRunAgainWhenALaterPhaseFailed(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.flowIs(t, bodyFlow("agent(model = \"m\", prompt = \"prompts/work.md\", name = \"first\")\nagent(model = \"m\", prompt = \"prompts/second.md\", name = \"second\")\n"))
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(harness.opts.StateDir), "prompts", "second.md"), []byte("Second.\n"), 0o600))

	harness.agents.result = func(request AgentRequest) agent.Result {
		if strings.Contains(request.Prompt, "Second.") {
			return agent.Result{Outcome: agent.OutcomeExited, ExitCode: 3, Note: "bad exit"}
		}

		return agent.Result{Outcome: agent.OutcomeExited}
	}
	engine := harness.engine(t)
	client := harness.client(t, testVersion)

	_, err := engine.Run(testItem, testProject)
	require.ErrorContains(t, err, "the agent step of item 12 ended as exited (exit 3)")

	_, err = engine.Run(testItem, testProject)
	require.ErrorContains(t, err, "the agent step of item 12 ended as exited (exit 3)", "run names the step that failed")

	harness.agents.mu.Lock()
	harness.agents.result = nil
	harness.agents.mu.Unlock()

	_, err = resumeAndJoin(t, engine, client)
	require.NoError(t, err)

	var prompts []string

	for _, request := range harness.agents.seen() {
		prompts = append(prompts, request.Prompt)
	}

	assert.Equal(t, []string{"Work on the item.\n", "Second.\n", "Second.\n"}, prompts, "the first agent does not run again and the second does")
}

func TestOpenStart_AFinishedAgentStepClosesTheMarksBeforeIt(t *testing.T) {
	t.Parallel()

	steps := []dbos.StepInfo{
		{StepID: 0, StepName: startStepPrefix + "1"},
		{StepID: 1, StepName: agentStepName},
		{StepID: 2, StepName: "later"},
	}

	assert.Equal(t, len(steps), openStart(steps), "a rewind must not go back before a finished agent step")

	reopened := append([]dbos.StepInfo{}, steps...)
	reopened = append(reopened, dbos.StepInfo{StepID: 3, StepName: startStepPrefix + "1"})
	assert.Equal(t, 3, openStart(reopened), "only the marks after the agent step are open")
}
