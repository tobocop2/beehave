package flow

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/workflow"
)

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

// goldenShapes holds the shape of the recorded results for each steps version. The key is the steps version.
// A change to a builtin or to a result field is a change to what a run records: bump stepsVersion and add the new shape under the new version.
// The old entry goes away with the bump, because a build refuses the runs of an older version.
var goldenShapes = map[string][]string{
	"star23": {
		"agent: cost_estimated,cost_unknown,cost_usd,head_sha,rewrote,session,stop,text,transcript,violations",
		"batch_merge: deferred,findings,gates,merged,note,rejected,tip,tree,unproven",
		"board_sync: number,status",
		"command: rc,stderr,stdout",
		"earlier_calls: count",
		"facts: base_branch,board_merged,board_number,board_owner,board_started,brief_rules,build_host,changelog,conventions,epics,forge,gate,gate_files,gate_steps,limits,people,repo,source,stacked_rules,token_rules,tracker,traps",
		"gate: head,passed,report,required,steps,tree",
		"gather: results",
		"host_teardown: down,reason",
		"host_up: host,started",
		"item_assign: id",
		"item_comment: id",
		"item_create: created,id",
		"item_status: id,status",
		"merge: reason,state",
		"pr_assign: issues,number",
		"pr_comment: number",
		"pr_open: created,number,stack",
		"preflight: code,ok,reason,stop",
		"queue: free,rows",
		"spawn: runs",
		"wait: answer,answers,timed_out",
		"write: key,rc,skipped,stderr,stdout",
	},
}

func TestStepsVersion_TheShapeOfTheRecordedResultsIsTheGoldenShapeOfTheVersion(t *testing.T) {
	t.Parallel()

	golden, ok := goldenShapes[stepsVersion]
	require.True(t, ok, "stepsVersion %s has no golden shape: add the shape of the results under the new version and remove the old one", stepsVersion)
	assert.Equal(t, golden, resultShape(t), "a builtin or a result field changed: bump stepsVersion and update the golden shape")
	assert.Len(t, goldenShapes, 1, "only the shape of the current version stays")
}

// goldenKinds holds the table of step kinds for each steps version, as the engine records them. The test needs no workflow to call a builtin:
// the table is the one the engine checks every step name against. A builtin with no result fields is in it too.
var goldenKinds = map[string][]string{
	"star23": {
		"agent", "board_sync", "command", "earlier_calls", "env", "facts", "gate", "gather", "host_teardown", "host_up",
		"item_assign", "item_comment", "item_create", "item_status", "login", "merge", "now",
		"pr_assign", "pr_comment", "pr_open", "preflight", "queue", "scope", "scope-check", "spawn", "wait", "write",
	},
}

func TestStepsVersion_TheTableOfStepKindsIsTheGoldenTableOfTheVersion(t *testing.T) {
	t.Parallel()

	golden, ok := goldenKinds[stepsVersion]
	require.True(t, ok, "stepsVersion %s has no golden step kinds: add the table under the new version and remove the old one", stepsVersion)
	assert.Equal(t, golden, stepKinds(), "a step kind changed: bump stepsVersion and update the golden table")
	assert.Len(t, goldenKinds, 1, "only the table of the current version stays")
}

func TestStepsVersion_EveryBuiltinWithResultFieldsRecordsUnderAKindOfTheTable(t *testing.T) {
	t.Parallel()

	for _, name := range workflow.Results() {
		if name == "batch_merge" {
			continue // its steps are named batch:<name>.<kind> and the golden list of its steps holds them
		}

		assert.Contains(t, stepKinds(), name, "the builtin %s has result fields and no step kind", name)
	}
}

func TestStepsVersion_AStepOfAKindOutsideTheTableIsAFaultOfTheBuild(t *testing.T) {
	t.Parallel()

	require.ErrorIs(t, checkStepKind(stepKinds(), "pr_comment2"), errUnknownStepKind)
	require.NoError(t, checkStepKind(stepKinds(), "pr_comment"))
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

	assert.Empty(t, s.counts, "a refused kind counts no call")
}

// goldenBatchSteps are the step names of one batch_merge of items 1 and 3 under the steps version.
var goldenBatchSteps = []string{
	"batch:b.refuse", "batch:b.heads#1", "batch:b.tip#1", "batch:b.union#1", "batch:b.tip#2", "batch:b.intent#1",
	"batch:b.tip#3", "batch:b.intent#3", "batch:b.tip#4", "batch:b.prove", "batch:b.issues",
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

	assert.Equal(t, goldenBatchSteps, batch, "the steps of batch_merge changed: bump stepsVersion and update the list")
}
