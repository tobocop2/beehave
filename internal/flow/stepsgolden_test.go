package flow

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.starlark.net/starlark"

	"github.com/tobocop2/beehave/internal/step"
	"github.com/tobocop2/beehave/internal/workflow"
)

// moduleRegistry is the registry that an engine builds from the module of this package.
func moduleRegistry(t *testing.T) step.Registry {
	t.Helper()

	registry, err := step.NewRegistry([]step.Module{StepModule()})
	require.NoError(t, err)

	return registry
}

// resultShape lists every builtin that has result fields with its sorted fields, one line each. It reads the table of the workflow package,
// so a builtin added to the table is in the shape at once and cannot be left out of the golden data.
func resultShape(t *testing.T) []string {
	t.Helper()

	var lines []string

	for _, name := range workflow.Results() {
		lines = append(lines, name+": "+strings.Join(tableOf(t, name), ","))
	}

	return lines
}

// goldenShapes holds the shape of the recorded results. The key is stepsVersion, the single version that came before the modules.
// A change to a builtin or to a result field is a change to what a run records: bump the version of the module that owns the kind in moduleTable
// (stepmodules.go) and update the shape. A later change re-keys these goldens for each module; until then the key stays star30.
var goldenShapes = map[string][]string{
	"star30": {
		"agent: committed,cost_estimated,cost_unknown,cost_usd,head_sha,rewrote,session,skills,stop,text,transcript,violations",
		"cancel: " + workflow.CancelledField,
		"command: attempts,error,rc,stderr,stdout",
		"earlier_calls: count",
		"facts: base_branch,build_host,forge,gate,gate_steps,repo,runners,source,tracker,vars",
		"first_of: kind,message,outcome,run,runs",
		"gate: checks,head,host_loss,host_losses,new_tests,passed,report,required,revert,steps,tree",
		"gather: results",
		"host_teardown: down,reason",
		"host_up: host,started",
		"item_comment: id",
		"item_create: created,id",
		"item_read: comments,fields,found,id,parent",
		"item_status: id,status",
		"merge: deferred,findings,gates,host_loss,host_losses,merged,moved,note,rejected,tip,tree,unproven",
		"pr_comment: number",
		"pr_history: pulls",
		"pr_open: created,number,stack",
		"pr_state: state",
		"preflight: code,ok,reason,stop",
		"push: head",
		"queue: free,pulls,rows",
		"recv: id,sender,timed_out,value",
		"send: id",
		"sleep: slept",
		"spawn: runs",
		"wait: answer,answers,timed_out",
		"write: attempts,key,rc,skipped,stderr,stdout",
	},
}

func TestStepsVersion_TheShapeOfTheRecordedResultsIsTheGoldenShapeOfTheVersion(t *testing.T) {
	t.Parallel()

	golden, ok := goldenShapes[stepsVersion]
	require.True(t, ok, "the golden shapes have no entry for the key %s", stepsVersion)
	assert.Equal(t, golden, resultShape(t), "a builtin or a result field changed: bump the version of the module that owns it in moduleTable (stepmodules.go) and update the golden shape")
	assert.Len(t, goldenShapes, 1, "only the shape of the current version stays")
}

// goldenKinds holds the table of step kinds for each steps version, as the engine records them. The test needs no workflow to call a builtin:
// the table is the one the engine checks every step name against. A builtin with no result fields is in it too.
var goldenKinds = map[string][]string{
	"star30": {
		"agent", "cancel", "command", "earlier_calls", "env", "facts", "first_of", "gate", "gather", "host_teardown", "host_up",
		"item_comment", "item_create", "item_read", "item_status", "login", "now",
		"pr_comment", "pr_history", "pr_open", "pr_state", "preflight", "progress", "publish", "push", "queue", "recv", "scope", "send", "sleep", "spawn", "wait", "write",
	},
}

func TestStepsVersion_TheTableOfStepKindsIsTheGoldenTableOfTheVersion(t *testing.T) {
	t.Parallel()

	golden, ok := goldenKinds[stepsVersion]
	require.True(t, ok, "the golden step kinds have no entry for the key %s", stepsVersion)
	assert.Equal(t, golden, moduleRegistry(t).Kinds(), "a step kind changed: bump the version of the module that owns it in moduleTable (stepmodules.go) and update the golden table")
	assert.Len(t, goldenKinds, 1, "only the table of the current version stays")
}

func TestStepsVersion_EveryBuiltinWithResultFieldsRecordsUnderAKindOfTheTable(t *testing.T) {
	t.Parallel()

	for _, name := range workflow.Results() {
		if name == "merge" {
			continue // its steps are named batch:<name>.<kind> and the golden list of its steps holds them
		}

		assert.Contains(t, moduleRegistry(t).Kinds(), name, "the builtin %s has result fields and no step kind", name)
	}
}

func TestStepsVersion_AStepOfAKindOutsideTheTableIsAFaultOfTheBuild(t *testing.T) {
	t.Parallel()

	kinds := moduleRegistry(t).Kinds()

	require.ErrorIs(t, checkStepKind(kinds, "pr_comment2"), errUnknownStepKind)
	require.NoError(t, checkStepKind(kinds, "pr_comment"))
}

func TestStepsVersion_EveryPathThatRecordsAStepRefusesAKindOutsideTheTable(t *testing.T) {
	t.Parallel()

	engine := idleEngine(t, newHarness(t))
	s := &script{e: engine, ctx: engine.ctx, counts: map[string]int{}, kinds: []string{"none"}}
	noop := func(context.Context) (string, error) { return "", nil }

	_, err := s.name("agent", "")
	require.ErrorIs(t, err, errUnknownStepKind)

	_, err = countedStep(s, "now", "", noop)
	require.ErrorIs(t, err, errUnknownStepKind)

	_, err = s.durable("facts", "", func(context.Context, string) (stepResult, error) { return stepResult{}, nil })
	require.ErrorIs(t, err, errUnknownStepKind)

	_, _, _, err = s.ready(&agentArgs{}, "")
	require.ErrorIs(t, err, errUnknownStepKind)

	_, _, err = s.writePlan(writeRaw{})
	require.ErrorIs(t, err, errUnknownStepKind)

	_, err = s.gather(starlark.NewBuiltin("gather", nil), starlark.Tuple{starlark.NewList(nil)}, nil)
	require.ErrorIs(t, err, errUnknownStepKind)

	assert.Empty(t, s.counts, "a refused kind counts no call")
}

// goldenBatchSteps are the step names of one merge of items 1 and 3 under the steps version.
var goldenBatchSteps = []string{
	"batch:b.refuse", "batch:b.heads#1", "batch:b.tip#1", "batch:b.union#1", "batch:b.tip#2", "batch:b.scope#1", "batch:b.intent#1",
	"batch:b.tip#3", "batch:b.scope#3", "batch:b.intent#3", "batch:b.tip#4", "batch:b.prove", "batch:b.issues",
}

func TestStepsVersion_TheStepNamesOfABatchMergeMatchTheGoldenListOfTheStepsVersion(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	engine := rig.engine(t)

	_, err := engine.RunFile(testItem, testProject, writeFlow(t, bodyFlow(rig.call("1 3", ""))))
	require.NoError(t, err)

	var batch []string

	for _, name := range stepNamesOf(t, engine, testRun) {
		if strings.HasPrefix(name, "batch:") {
			batch = append(batch, name)
		}
	}

	assert.Equal(t, goldenBatchSteps, batch, "the steps of merge changed: bump the version of the land module in moduleTable (stepmodules.go) and update the list")
	assert.Equal(t, goldenBatchGateSteps, batchGateSteps(t, engine), "the steps of the batch gate changed: bump the version of the land module in moduleTable (stepmodules.go) and update the list")
}

// goldenBatchGateSteps are the steps of the run of the project gate of a batch. The step batch_gate:job holds the checkout and the host job,
// and a replay that finds it waits on that job.
var goldenBatchGateSteps = []string{"batch_gate:job", "batch_gate"}

// goldenLostBatchSteps are the steps of merge in the item run when the first gate of the union loses its job and the rerun passes:
// the record of the loss comes after the first gate and before the members merge.
var goldenLostBatchSteps = []string{
	"batch:b.refuse", "batch:b.heads#1", "batch:b.tip#1", "batch:b.union#1", "batch:b.gate#1/host-loss", "batch:b.tip#2", "batch:b.scope#1", "batch:b.intent#1",
	"batch:b.tip#3", "batch:b.scope#3", "batch:b.intent#3", "batch:b.tip#4", "batch:b.prove", "batch:b.issues",
}

func TestStepsVersion_TheStepNamesOfABatchMergeWhoseGateLostItsJobMatchTheGoldenList(t *testing.T) {
	t.Parallel()

	rig, _ := lostBatchRig(t, 1)
	engine := rig.engine(t)

	_, err := engine.RunFile(testItem, testProject, writeFlow(t, bodyFlow("host_up(name = \"start\", host = \"gpu-b\")\n"+rig.call("1 3", ""))))
	require.NoError(t, err)

	var batch []string

	for _, name := range stepNamesOf(t, engine, testRun) {
		if strings.HasPrefix(name, "batch:") {
			batch = append(batch, name)
		}
	}

	assert.Equal(t, goldenLostBatchSteps, batch, "the steps of merge changed: bump the version of the land module in moduleTable (stepmodules.go) and update the list")
}

// goldenRunnerFacts holds the fields of each entry of the runners field of the facts result. The shape above lists top-level fields only, so a nested field of a recorded result is pinned here.
var goldenRunnerFacts = []string{"loads_skills"}

func TestStepsVersion_TheEntryOfARunnerInTheRecordedFactsHoldsTheGoldenFields(t *testing.T) {
	t.Parallel()

	runners := runnerFacts(map[string]bool{"a": true})
	entry, ok := runners["a"].(map[string]any)
	require.True(t, ok, "the runners field holds a dict for each runner")

	var fields []string
	for name := range entry {
		fields = append(fields, name)
	}

	slices.Sort(fields)
	assert.Equal(t, goldenRunnerFacts, fields, "a field of the runners entry changed: bump the core module in stepmodules.go and update the golden fields")
}
