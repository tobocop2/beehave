package flow

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.starlark.net/starlark"
)

func TestFirstOf_RefusesACallThatBreaksItsRules(t *testing.T) {
	t.Parallel()

	cases := map[string]struct{ call, want string }{
		"no source":      {`first_of()`, "first_of needs something to wait for"},
		"no keyword":     {`first_of(**{"wait": 1})`, "wait"},
		"runs as number": {"n = 4 + 1\nfirst_of(runs = n)", "runs must"},
		"topics as text": {"bad = \"go\" + \"\"\nfirst_of(topics = bad, timeout = \"1s\")", "topics"},
		"reserved topic": {`first_of(topics = ["beehave.child"], timeout = "1s")`, "reserved"},
		"bad timeout":    {`first_of(timeout = "soon")`, "timeout"},
		"a stranger":     {`first_of(runs = [struct(item = "9", run = "item-9-r1")])`, "not a child"},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, bodyFlow(c.call)), modeOff())

			require.ErrorContains(t, err, c.want)
		})
	}
}

func TestFirstOf_ATimeoutThatCannotBeRecordedFailsTheCall(t *testing.T) {
	t.Parallel()

	engine := newHarness(t).engine(t)
	s := &script{e: engine, ctx: engine.ctx}
	timeout := starlark.Tuple{starlark.String(timeoutKeyword), starlark.String("1s")}

	_, err := s.firstOf(starlark.NewBuiltin(firstOfKind, nil), nil, []starlark.Tuple{timeout})

	require.Error(t, err, "the deadline step runs only inside a workflow run")
}

func TestFirstOf_ATurnThatCannotBeRecordedFailsTheCall(t *testing.T) {
	t.Parallel()

	engine := newHarness(t).engine(t)
	s := &script{e: engine, ctx: engine.ctx}
	topics := starlark.Tuple{starlark.String(topicsKeyword), starlark.NewList([]starlark.Value{starlark.String("go")})}

	_, err := s.firstOf(starlark.NewBuiltin(firstOfKind, nil), nil, []starlark.Tuple{topics})

	require.Error(t, err, "the look step runs only inside a workflow run")
}

func TestFirstOf_ALookThatCannotReadTheRunsFails(t *testing.T) {
	t.Parallel()

	engine := newHarness(t).engine(t)
	require.NoError(t, engine.Shutdown())

	_, err := engine.lookFirst(ItemInput{Item: testItem}, []spawnedRun{{Item: "21", Run: "item-21-r1"}}, []spawnedRun{{Item: "21", Run: "item-21-r1"}}, nil, time.Time{}, false)

	require.ErrorContains(t, err, "read the runs of the spawned items")
}

// stepsFailWhenArmed is a store whose list of the steps of a run fails once armed is set. Each other read goes to the store.
type stepsFailWhenArmed struct {
	dbos.Context

	armed *atomic.Bool
}

func (s *stepsFailWhenArmed) GetWorkflowSteps(_ dbos.Client, id string, opts ...dbos.GetWorkflowStepsOption) ([]dbos.StepInfo, error) {
	if s.armed.Load() {
		return nil, errors.New("the store failed")
	}

	return dbos.GetWorkflowSteps(s.Context, id, opts...)
}

func TestFirstOf_ASpendThatCannotBeReadFailsTheLook(t *testing.T) {
	t.Parallel()

	var armed atomic.Bool

	engine := newHarness(t).engineWith(t, func(e *Engine) { e.ctx = &stepsFailWhenArmed{Context: e.ctx, armed: &armed} })
	file := `def worker(item):
    command(name = "w", run = ["w"])

def main(item):
    gather(spawn(items = ["21"], entry = "worker", name = "fan"), name = "fan")
`
	_, err := engine.RunFile(testItem, testProject, writeFlow(t, file), modeOff())
	require.NoError(t, err)

	armed.Store(true)

	refs := []spawnedRun{{Item: "21", Run: "item-21-r1"}}
	_, err = engine.lookFirst(ItemInput{Item: testItem}, refs, refs, nil, time.Time{}, false)

	require.ErrorContains(t, err, "read the spend of run item-21-r1")
}

func TestFirstOf_AValueThatIsNotJSONFailsTheCall(t *testing.T) {
	t.Parallel()

	_, err := firstOfValue(firstChoice{Ended: []endedRun{{Outcome: childOutcome{Item: "21", OK: true, Value: []byte("{")}}}})
	require.ErrorContains(t, err, "read the value of the run of item 21")

	_, err = firstOfValue(firstChoice{Message: []byte("{")})
	require.ErrorContains(t, err, "read the message of first_of")
}

func TestFirstOf_ACanceledRunWithNoParentOrAGoneParentTellsNobody(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	client := h.client(t, testVersion)

	assert.NotPanics(t, func() {
		client.tellParent(run{Status: dbos.WorkflowStatus{ID: "item-21-r1"}})
		client.tellParent(run{Status: dbos.WorkflowStatus{ID: "item-21-r1", ParentWorkflowID: "item-gone-r1"}})
	})
}
