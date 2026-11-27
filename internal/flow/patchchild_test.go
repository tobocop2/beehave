package flow

import (
	"context"
	"slices"
	"testing"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.starlark.net/starlark"

	"github.com/tobocop2/beehest/internal/review"
	"github.com/tobocop2/beehest/internal/step"
)

// gateFlow calls gate, waits for the owner, then calls gate again. Each gate starts a child workflow.
const gateFlow = "def main(item):\n    gate(name = \"g\")\n    wait(name = \"ask\", question = \"What now?\")\n    gate(name = \"h\")\n"

// gatePatchName is the name of the patch of the changed gate.
const gatePatchName = "gate-checks-first"

// gateModules are the modules of one build: the engine module with the builtin gate wrapped. The old build calls the gate as it is.
// The new build calls Patch from the body of the builtin, and a call that takes the new code records one more step before the gate starts its child workflow.
func gateModules(t *testing.T, patched bool) []step.Module {
	t.Helper()

	engine := StepModule()
	at := slices.IndexFunc(engine.Builtins, func(b step.Builtin) bool { return b.Name == "gate" })
	require.GreaterOrEqual(t, at, 0)

	if patched {
		engine.Builtins[at].Call = patchedGate(engine.Builtins[at].Call)
	}

	return []step.Module{engine}
}

// patchedGate wraps the call of the gate. The patch is asked in the body of the builtin, before its first step.
func patchedGate(original func(step.Call, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error)) func(step.Call, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
	return func(c step.Call, fn *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		s, ok := c.(*script)
		if !ok {
			return nil, errFake
		}

		changed, err := dbos.Patch(s.ctx, gatePatchName)
		if err != nil {
			return nil, err
		}

		if changed {
			if _, err := countedStep(s, "gate", "check", func(context.Context) (string, error) { return "check", nil }); err != nil {
				return nil, err
			}
		}

		return original(c, fn, args, kwargs)
	}
}

// gateChildren lists the steps of each child workflow that a gate started, in the order of the gates.
func gateChildren(t *testing.T, engine *Engine) [][]string {
	t.Helper()

	rows, err := dbos.ListWorkflows(engine.ctx, dbos.WithFilterName(exclusiveName))
	require.NoError(t, err)

	children := make([][]string, 0, len(rows))

	for _, row := range rows {
		steps, err := dbos.GetWorkflowSteps(engine.ctx, row.ID)
		require.NoError(t, err)

		names := make([]string, len(steps))
		for i, record := range steps {
			names[i] = record.StepName
		}

		children = append(children, names)
	}

	return children
}

func TestPatch_AGateThatStartsAChildWorkflowKeepsTheOldStepsOfARunThatPassedItAndTheNewStepsOfALaterCall(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.gate.results = []GateResult{{Passed: true, Head: gatedHead}, {Passed: true, Head: gatedHead}}
	h.forge.state = review.Standing{Number: testPull, Head: gatedHead}

	h.opts.Modules = gateModules(t, false)
	file := writeFlow(t, gateFlow)
	interrupted(t, h, file)

	h.opts.Modules = gateModules(t, true)
	engine := h.engine(t)
	client := h.client(t, testVersion)
	done := runInBackground2(engine, file)

	waitOn(t, waiting[struct{}]{what: "the open run to take the answer", run: runOfClient(t, client), read: func() (struct{}, bool, error) {
		return struct{}{}, client.Reply(testItem, Reply{Text: "yes"}) == nil, nil
	}})

	outcome := receive(t, done)

	require.NoError(t, outcome.err, "the replay of gate 1 reads the old steps, and gate 2 takes the new code")

	names, err := client.stepNames(testRun)
	require.NoError(t, err)
	t.Logf("steps: %v", names)
	assert.Equal(t, 1, namedCount(names, "DBOS.patch-"+gatePatchName), "only gate 2 took the new code, so one marker")
	assert.Equal(t, 1, namedCount(names, "gate:check#2"), "the extra step of the new code ran once, in the second call")
	assert.Len(t, gateChildren(t, engine), 2, "each gate started its own child workflow")
}

// namedCount counts the names equal to want.
func namedCount(names []string, want string) int {
	return len(slices.DeleteFunc(slices.Clone(names), func(name string) bool { return name != want }))
}
