package flow

import (
	"context"
	"sync"
	"testing"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/review"
)

func TestQueuePriority_OneRuleGivesTheOrderOfSeverityAndAWaitingPerson(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		severity model.Severity
		waits    bool
		want     uint
	}{
		{"p0_comes_first", model.SeverityP0, false, 1},
		{"p0_with_a_person_waiting_still_comes_first", model.SeverityP0, true, 1},
		{"a_person_waiting_comes_next", model.SeverityP2, true, 2},
		{"p1", model.SeverityP1, false, 3},
		{"p2", model.SeverityP2, false, 4},
		{"p3", model.SeverityP3, false, 5},
		{"no_severity_comes_last", model.SeverityUnknown, false, priorityNormal},
		{"an_empty_severity_is_no_severity", "", false, priorityNormal},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, test.want, placeOf(test.severity, test.waits))
		})
	}
}

func TestQueuePriority_AGateTakesItsPlaceFromTheSeverityOfItsItem(t *testing.T) {
	t.Parallel()

	in := ExclusiveInput{Kind: kindGate, Severity: string(model.SeverityP0)}

	assert.Equal(t, priorityP0, priorityOf(in, review.Standing{}, true))
	assert.Equal(t, priorityNormal, priorityOf(ExclusiveInput{Kind: kindMerge, Severity: string(model.SeverityP0)}, review.Standing{}, true), "a merge keeps the normal place")
}

// indexOf is the place of item in order, or -1.
func indexOf(order []string, item string) int {
	for i, one := range order {
		if one == item {
			return i
		}
	}

	return -1
}

// itemsStartOrder runs a parent that spawns the items 21 to 24 on an items queue of two slots. The commands of the first two block until the other two wait in the queue.
// It returns the items in the order their commands started.
func itemsStartOrder(t *testing.T, severities map[string]string) []string {
	t.Helper()

	h := newHarness(t)
	h.items.severities = severities

	var (
		mu      sync.Mutex
		order   []string
		release = make(chan struct{})
		tokens  = make(chan struct{})
	)

	started := func(n int) bool {
		mu.Lock()
		defer mu.Unlock()

		return len(order) == n
	}

	h.commands.before = func(ctx context.Context, request CommandRequest) error {
		if request.Item == testItem {
			return nil
		}

		mu.Lock()
		order = append(order, request.Item)
		held := len(order) <= 3
		mu.Unlock()

		if held {
			select {
			case <-tokens:
			case <-release:
			case <-ctx.Done():
			}
		}

		return nil
	}

	engine := h.engine(t)
	items := []string{"21", "22", "23", "24"}
	flow := "def worker(item):\n    command(name = \"w\", run = [\"w\"])\n\ndef main(item):\n    gather(spawn(items = [\"21\", \"22\", \"23\", \"24\"], entry = \"worker\"))\n"
	file := writeFlow(t, flow)
	done := make(chan error, 1)

	go func() {
		_, err := engine.RunFile(testItem, testProject, file)
		done <- err
	}()

	assert.True(t, waitUntil(func() bool { return countEnqueued(engine, items) == 2 }), "two runs wait while two run")

	// One slot frees at a time, so the third and the fourth start are in the order that the queue picked them.
	tokens <- struct{}{}

	assert.True(t, waitUntil(func() bool { return started(3) }))

	tokens <- struct{}{}

	assert.True(t, waitUntil(func() bool { return started(4) }))
	close(release)
	require.NoError(t, <-done)

	mu.Lock()
	defer mu.Unlock()

	return append([]string(nil), order...)
}

func TestQueuePriority_AP0ItemStartsBeforeEarlierRunsOnTheItemsQueue(t *testing.T) {
	t.Parallel()

	order := itemsStartOrder(t, map[string]string{"21": "P2", "22": "P2", "23": "P2", "24": "P0"})

	require.Len(t, order, 4)
	assert.Less(t, indexOf(order, "24"), indexOf(order, "23"), "the later run with a person waiting starts first: %v", order)
}

func TestQueuePriority_RunsOfEqualPriorityStartInTheOrderTheyWereQueued(t *testing.T) {
	t.Parallel()

	order := itemsStartOrder(t, nil)

	require.Len(t, order, 4)
	assert.ElementsMatch(t, []string{"21", "22"}, order[:2])
	assert.Equal(t, []string{"23", "24"}, order[2:])
}

// agentStartOrder runs the items 21, 22 and 23 one after the other, each with one agent step, on one login. The agent of 21 holds the login until the other two wait.
// It returns the items in the order their agents started.
func agentStartOrder(t *testing.T, severities map[string]string) []string {
	t.Helper()

	h := newHarness(t)
	h.items.severities = severities
	h.agents.gate = make(chan struct{})

	h.flowIs(t, noKindFlow)

	engine := h.engine(t)

	var wg sync.WaitGroup

	start := func(item string) {
		wg.Add(1)

		go func() {
			defer wg.Done()

			_, err := engine.Run(item, testProject)
			assert.NoError(t, err)
		}()
	}

	enqueued := func() int {
		rows, err := dbos.ListWorkflows(engine.ctx, dbos.WithFilterName(phaseName), dbos.WithFilterStatus(dbos.WorkflowStatusEnqueued))
		require.NoError(t, err)

		return len(rows)
	}

	start("21")
	require.True(t, waitUntil(func() bool { return h.agents.running() == 1 }), "the first agent never started")

	start("22")
	require.True(t, waitUntil(func() bool { return enqueued() == 1 }))

	start("23")
	require.True(t, waitUntil(func() bool { return enqueued() == 2 }))

	close(h.agents.gate)
	wg.Wait()

	var order []string

	for _, request := range h.agents.seen() {
		order = append(order, request.Item)
	}

	return order
}

func TestQueuePriority_AnAgentStepOfAP0ItemStartsBeforeEarlierStepsOnTheAgentQueue(t *testing.T) {
	t.Parallel()

	order := agentStartOrder(t, map[string]string{"21": "P2", "22": "P2", "23": "P0"})

	assert.Equal(t, []string{"21", "23", "22"}, order)
}

func TestQueuePriority_AgentStepsOfEqualPriorityStartInTheOrderTheyWereQueued(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{"21", "22", "23"}, agentStartOrder(t, nil))
}
