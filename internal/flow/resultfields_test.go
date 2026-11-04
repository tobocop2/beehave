package flow

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	"github.com/tobocop2/obee/internal/batch"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/preflight"
	"github.com/tobocop2/obee/internal/workflow"
)

func keysOf(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}

	slices.Sort(keys)

	return keys
}

func tableOf(t *testing.T, builtin string) []string {
	t.Helper()

	fields, ok := workflow.ResultFields(builtin)
	require.True(t, ok, builtin)
	slices.Sort(fields)

	return fields
}

func TestResultFields_TheOutputsOfEachBuiltinAreTheFieldsOfTheTable(t *testing.T) {
	t.Parallel()

	got := map[string][]string{
		"agent":         keysOf(agentOutputs("", "", "", "", "", "", 0, false)),
		"board_sync":    keysOf(boardOutputs(0, "")),
		"command":       keysOf(commandOutputs("", "", 0)),
		"gate":          keysOf(gateOutputs(true, "", "", "", nil, nil)),
		"facts":         keysOf(factsOutputs(model.Project{})),
		"item_comment":  keysOf(commentOutputs("")),
		"item_create":   keysOf(createOutputs("", true)),
		"item_status":   keysOf(statusOutputs("", "")),
		"merge":         keysOf(mergeOutputs("", "")),
		"host_teardown": keysOf(teardownOutputs(true, "")),
		"host_up":       keysOf(upOutputs(true, "")),
		"pr_assign":     keysOf(prOutputs(0, nil)),
		"pr_comment":    keysOf(prCommentOutputs(0)),
		"pr_open":       keysOf(openOutputs(OpenResult{})),
		"preflight":     keysOf(preflightOutputs(preflight.Result{})),
		"wait":          keysOf(answerOutputs("", nil, false)),
		"write":         keysOf(writeOutputs("", "", 0, "", false)),
	}

	for builtin, keys := range got {
		assert.Equal(t, tableOf(t, builtin), keys, builtin)
	}
}

func batchStruct(t *testing.T, value starlark.Value) *starlarkstruct.Struct {
	t.Helper()

	out, ok := value.(*starlarkstruct.Struct)
	require.True(t, ok)

	return out
}

func TestResultFields_TheStructsOfTheSwarmBuiltinsAreTheFieldsOfTheTable(t *testing.T) {
	t.Parallel()

	got := map[string][]string{
		"batch_merge": batchStruct(t, batchResult(batch.Result{}, batch.Report{}, nil)).AttrNames(),
		"gather":      outcomesValue([]childOutcome{{}}).AttrNames(),
		"queue":       queuedValue(queued{}).AttrNames(),
		"spawn":       runsValue([]spawnedRun{{}}).AttrNames(),
	}

	for builtin, names := range got {
		slices.Sort(names)
		assert.Equal(t, tableOf(t, builtin), names, builtin)
	}
}

func TestResultFields_TheTableHasEveryBuiltinThatReturnsAStruct(t *testing.T) {
	t.Parallel()

	for _, name := range workflow.Builtins() {
		_, has := workflow.ResultFields(name)
		assert.Equal(t, name != "env" && name != "json" && name != "now" && name != "struct" && name != "sandbox", has, name)
	}

	assert.Len(t, workflow.Results(), len(workflow.Builtins())-5)
}
