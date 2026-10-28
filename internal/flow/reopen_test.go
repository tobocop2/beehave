package flow

import (
	"testing"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/agent"
	"github.com/tobocop2/obee/internal/model"
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

	require.NoError(t, client.Resume(testItem))

	result, err := engine.Run(testItem, testProject)
	require.NoError(t, err)
	assert.Equal(t, ItemResult{Item: testItem}, result)

	infos, err = client.Status()
	require.NoError(t, err)
	require.Len(t, infos, 1)
	assert.Equal(t, RunDone, infos[0].State)
	assert.Equal(t, []model.Step{model.StepPlan, model.StepPlan, model.StepImplement, model.StepReview}, stepsOf(harness.agents.seen()),
		"the plan agent runs again after its failure and the finished steps do not")
}

func TestResume_AFailedRunWithNoFailedStepReplaysFromTheEndOfItsHistory(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	deps := harness.deps()
	deps.Logins = &loginOnce{} // the run starts, then its own pick finds no login: the failure is no step.

	engine, err := NewEngine(t.Context(), harness.opts, deps)
	require.NoError(t, err)
	require.NoError(t, engine.Launch())
	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	client := harness.client(t, testVersion)

	_, err = engine.Run(testItem, testProject)
	require.ErrorIs(t, err, ErrEmptyLogin)

	require.NoError(t, client.Resume(testItem))
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

	_, err = client.reopenChild("child")
	require.ErrorContains(t, err, "read workflow child")
	require.ErrorContains(t, client.rewind("item-1-r1", 0), "read workflow item-1-r1")
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
	harness.agents.result = func(request AgentRequest) agent.Result {
		if request.Step == model.StepImplement {
			return agent.Result{Outcome: agent.OutcomeExited, ExitCode: 3, Note: "bad exit"}
		}

		return agent.Result{Outcome: agent.OutcomeExited}
	}
	engine := harness.engine(t)
	client := harness.client(t, testVersion)

	_, err := engine.Run(testItem, testProject)
	require.ErrorContains(t, err, "the implement of item 12 ended as exited (exit 3)")

	_, err = engine.Run(testItem, testProject)
	require.ErrorContains(t, err, "the implement of item 12 ended as exited (exit 3)", "run names the step that failed")

	harness.agents.mu.Lock()
	harness.agents.result = nil
	harness.agents.mu.Unlock()

	require.NoError(t, client.Resume(testItem))

	_, err = engine.Run(testItem, testProject)
	require.NoError(t, err)

	assert.Equal(t, []model.Step{model.StepPlan, model.StepImplement, model.StepImplement, model.StepReview}, stepsOf(harness.agents.seen()))
}
