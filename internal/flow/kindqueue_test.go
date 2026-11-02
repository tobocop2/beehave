package flow

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const noKindFlow = "def main(item):\n    agent(name = \"work\", prompt = \"prompts/work.md\", model = \"m\")\n"

func kindFlow(kind string) string {
	return fmt.Sprintf("def main(item):\n    agent(name = \"work\", prompt = \"prompts/work.md\", model = \"m\", kind = %q)\n", kind)
}

func enqueuedOn(t *testing.T, engine *Engine, queue string) int {
	t.Helper()

	rows, err := dbos.ListWorkflows(engine.ctx, dbos.WithFilterStatus(dbos.WorkflowStatusEnqueued), dbos.WithFilterQueueName(queue))
	require.NoError(t, err)

	return len(rows)
}

func TestAgentKind_ARunOfAKindWaitsWhenTheCapOfItsQueueIsFull(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, flow, full, other string
	}{
		{"review_runs_wait_on_the_review_queue", kindFlow("review"), reviewsQueueName, queueName},
		{"implement_runs_wait_on_the_agents_queue", kindFlow("implement"), queueName, reviewsQueueName},
		{"a_call_with_no_kind_is_an_implement_run", noKindFlow, queueName, reviewsQueueName},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			harness := newHarness(t)
			harness.opts.MaxConcurrent, harness.opts.MaxReviewConcurrent = 2, 2
			harness.flowIs(t, test.flow)
			harness.logins = fakeLogins{byItem: map[string]string{"1": "a", "2": "b", "3": "c"}}
			harness.agents.gate = make(chan struct{})
			release := sync.OnceFunc(func() { close(harness.agents.gate) })
			t.Cleanup(release)

			engine := harness.engine(t)
			runs := []<-chan runOutcome{runInBackground(engine, "1"), runInBackground(engine, "2"), runInBackground(engine, "3")}

			eventually(t, func() bool { return harness.agents.running() == 2 })
			eventually(t, func() bool { return enqueuedOn(t, engine, test.full) == 1 })
			assert.Zero(t, enqueuedOn(t, engine, test.other))
			release()

			for _, run := range runs {
				require.NoError(t, (<-run).err)
			}

			assert.Equal(t, 2, harness.agents.maxTotal)
		})
	}
}

// An agent phase is its own workflow on the agent queue and ends with the agent, so an item that waits for the owner after its agent call holds no agent slot.
func TestAgentQueue_AnItemThatWaitsForTheOwnerHoldsNoAgentSlot(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.opts.MaxConcurrent, harness.opts.MaxReviewConcurrent = 1, 1
	harness.flowIs(t, waitFlow)

	engine := harness.engine(t)
	client := harness.client(t, testVersion)
	run := runInBackground(engine, testItem)

	eventually(t, func() bool { return len(harness.agents.seen()) == 1 })
	eventually(t, func() bool { return activePhases(t, engine) == 0 })

	select {
	case <-run:
		t.Fatal("the item must still wait for the owner")
	default:
	}

	eventually(t, func() bool { return client.Answer(testItem, "go") == nil })
	require.NoError(t, (<-run).err)
}

func activePhases(t *testing.T, engine *Engine) int {
	t.Helper()

	total := 0

	for _, status := range []dbos.WorkflowStatusType{dbos.WorkflowStatusPending, dbos.WorkflowStatusEnqueued} {
		rows, err := dbos.ListWorkflows(engine.ctx, dbos.WithFilterName(phaseName), dbos.WithFilterStatus(status))
		require.NoError(t, err)

		total += len(rows)
	}

	return total
}

func TestAgentKind_TheCapsOfTheTwoQueuesAreIndependent(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.opts.MaxConcurrent, harness.opts.MaxReviewConcurrent = 1, 1
	harness.logins = fakeLogins{byItem: map[string]string{"1": "a", "2": "b"}}
	harness.agents.gate = make(chan struct{})
	release := sync.OnceFunc(func() { close(harness.agents.gate) })
	t.Cleanup(release)

	folder := filepath.Dir(harness.opts.StateDir)
	implement, review := filepath.Join(folder, "implement.star"), filepath.Join(folder, "review.star")
	require.NoError(t, os.WriteFile(implement, []byte(kindFlow("implement")), 0o600))
	require.NoError(t, os.WriteFile(review, []byte(kindFlow("review")), 0o600))

	engine := harness.engine(t)

	var first, second runOutcome

	var group sync.WaitGroup

	group.Add(2)

	go func() { defer group.Done(); first.result, first.err = engine.RunFile("1", testProject, implement) }()
	go func() { defer group.Done(); second.result, second.err = engine.RunFile("2", testProject, review) }()

	eventually(t, func() bool { return harness.agents.running() == 2 })
	release()
	group.Wait()
	require.NoError(t, first.err)
	require.NoError(t, second.err)
}

func TestAgentKind_AKindThatTheCheckCannotSeeIsRefusedWhenTheCallRuns(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	flow := bodyFlow("kind = \"triage\"\na = " + agentCallText(`kind = kind, `))

	_, err := rig.run(t, flow, "x")

	require.ErrorContains(t, err, `kind is "triage". Use one of: implement, review`)
	assert.Empty(t, rig.h.agents.seen())
}

func TestAgentKind_AReopenedReviewGoesBackToTheReviewQueue(t *testing.T) {
	t.Parallel()

	assert.Equal(t, reviewsQueueName, agentQueueName("review"))
	assert.Equal(t, queueName, agentQueueName("implement"))
	assert.Equal(t, queueName, agentQueueName(""))

	queue, partition, err := phaseRewindTarget(dbos.WorkflowStatus{Name: phaseName, Input: `{"login":"a","kind":"review"}`})
	require.NoError(t, err)
	assert.Equal(t, reviewsQueueName, queue)
	assert.Equal(t, "a", partition)

	queue, partition, err = phaseRewindTarget(dbos.WorkflowStatus{Name: phaseName, Input: `{"login":"b"}`})
	require.NoError(t, err)
	assert.Equal(t, queueName, queue)
	assert.Equal(t, "b", partition)

	options, err := rewindQueue(dbos.WorkflowStatus{Name: phaseName, Input: `{"login":"a","kind":"review"}`})
	require.NoError(t, err)
	assert.Len(t, options, 2)
}

// The partition of a login is per queue, and the pick only prefers a free login: one login runs a review and an implement step at once.
func TestAgentKind_AReviewRunsBesideAnImplementStepOfTheSameLogin(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.opts.MaxConcurrent, harness.opts.MaxReviewConcurrent = 2, 2
	harness.logins = fakeLogins{login: "a"}
	harness.agents.gate = make(chan struct{})
	release := sync.OnceFunc(func() { close(harness.agents.gate) })
	t.Cleanup(release)

	folder := filepath.Dir(harness.opts.StateDir)
	implement, review := filepath.Join(folder, "implement.star"), filepath.Join(folder, "review.star")
	require.NoError(t, os.WriteFile(implement, []byte(kindFlow("implement")), 0o600))
	require.NoError(t, os.WriteFile(review, []byte(kindFlow("review")), 0o600))

	engine := harness.engine(t)

	var first, second runOutcome

	var group sync.WaitGroup

	group.Add(2)

	go func() { defer group.Done(); first.result, first.err = engine.RunFile("1", testProject, implement) }()
	eventually(t, func() bool { return harness.agents.running() == 1 })
	go func() { defer group.Done(); second.result, second.err = engine.RunFile("2", testProject, review) }()

	eventually(t, func() bool { return harness.agents.running() == 2 })
	release()
	group.Wait()
	require.NoError(t, first.err)
	require.NoError(t, second.err)
	assert.Equal(t, 2, harness.agents.maxLogin)
}

func TestOptions_RefuseAReviewCapBelowOne(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.opts.MaxReviewConcurrent = 0

	_, err := NewEngine(t.Context(), harness.opts, harness.deps())
	require.ErrorContains(t, err, "agents.max_review_concurrent is 0: set it to 1 or more")
}
