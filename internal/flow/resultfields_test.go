package flow

import (
	"errors"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/landing"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/preflight"
	"github.com/tobocop2/beehave/internal/workflow"
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
		"agent":           keysOf(agentOutputs("", "", "", "", "", "", agent.Spend{}, false, false, nil)),
		"command":         keysOf(commandOutputs(CommandOutput{}, 1)),
		"earlier_calls":   keysOf(earlierOutputs(0)),
		"earlier_reads":   keysOf(earlierReadsOutputs(nil)),
		"pr_closing":      keysOf(prClosingOutputs(closingRead{})),
		"base_tip":        keysOf(baseTipOutputs("", "")),
		"landing_proof":   keysOf(landingProofOutputs(nil)),
		"issue_state":     keysOf(issueStateOutputs("", false, "")),
		"gate":            keysOf(gateOutputs(true, "", "", "", nil, nil, nil)),
		"facts":           keysOf(factsOutputs(model.Project{})),
		"item_comment":    keysOf(commentOutputs("")),
		"item_create":     keysOf(createOutputs("", true)),
		"item_list":       keysOf(itemListOutputs(nil)),
		"pr_list":         keysOf(prListOutputs(nil)),
		"run_read":        keysOf(runReadOutputs(nil, 0)),
		"item_read":       keysOf(readOutputs(false, "", nil, nil, "")),
		"item_status":     keysOf(statusOutputs("", "")),
		"host_teardown":   keysOf(teardownOutputs(true, "")),
		"host_up":         keysOf(upOutputs(true, "")),
		"pr_comment":      keysOf(prCommentOutputs(0, "")),
		"pr_open":         keysOf(openOutputs(OpenResult{})),
		"pr_state":        keysOf(prStateOutputs("")),
		"pr_history":      keysOf(prHistoryOutputs(nil)),
		"pr_read":         keysOf(prReadOutputs(model.PullFacts{})),
		"issue_assignees": keysOf(issueAssigneesOutputs(nil)),
		"item_stop":       keysOf(itemStopOutputs(false)),
		"preflight":       keysOf(preflightOutputs(preflight.Result{})),
		"recv":            keysOf(recvOutputs(envelope{}, false)),
		"send":            keysOf(sendOutputs("")),
		"sleep":           keysOf(sleepOutputs(0)),
		"wait":            keysOf(answerOutputs("", nil, false)),
		"write":           keysOf(writeOutputs(CommandOutput{}, 1, "", false)),
	}

	for builtin, keys := range got {
		assert.Equal(t, tableOf(t, builtin), keys, builtin)
	}

	e := &Engine{redact: func(text string) string { return text }}
	assert.Equal(t, tableOf(t, "command"), keysOf(e.failedOutputs(errors.New("lost"), 1)), "a command whose failure is returned has the fields of any other command")
}

func batchStruct(t *testing.T, value starlark.Value) *starlarkstruct.Struct {
	t.Helper()

	out, ok := value.(*starlarkstruct.Struct)
	require.True(t, ok)

	return out
}

func TestResultFields_TheStructsOfTheSwarmBuiltinsAreTheFieldsOfTheTable(t *testing.T) {
	t.Parallel()

	gathered, err := outcomesValue([]childOutcome{{}})
	require.NoError(t, err)

	first, err := firstOfValue(firstChoice{})
	require.NoError(t, err)

	got := map[string][]string{
		"cancel":   cancelValue(cancelOutcome{}).AttrNames(),
		"first_of": first.AttrNames(),
		"merge":    batchStruct(t, unionResult(unionOutcome{}, landing.Tip{})).AttrNames(),
		"hand_off": batchStruct(t, handResultValue(handResult{})).AttrNames(),
		"gather":   gathered.AttrNames(),
		"spawn":    runsValue([]spawnedRun{{}}).AttrNames(),
		"stream":   streamValue(streamRead{}).AttrNames(),
	}

	for builtin, names := range got {
		slices.Sort(names)
		assert.Equal(t, tableOf(t, builtin), names, builtin)
	}
}

func TestResultFields_TheTableHasEveryBuiltinThatReturnsAStruct(t *testing.T) {
	t.Parallel()

	noStruct := []string{"env", "json", "now", "struct", "sandbox", "paths_outside", "closing_references", "keep_issues", "publish", "progress", "patched"}
	for _, name := range workflow.Builtins() {
		_, has := workflow.ResultFields(name)
		assert.Equal(t, !slices.Contains(noStruct, name), has, name)
	}

	assert.Len(t, workflow.Results(), len(workflow.Builtins())-len(noStruct))
}
