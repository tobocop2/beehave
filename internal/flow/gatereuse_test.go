package flow

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/merge"
)

const twoGates = "a = gate(name = \"first\")\nb = gate(name = \"second\")\ncommand(run = [\"show\", b.head, str(b.passed), b.report])\n"

func reuseHarness(t *testing.T, results []GateResult, keys []GateKey) *harness {
	t.Helper()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})
	h.gate.results, h.gate.keys = results, keys

	return h
}

func gateKey(id, head string) GateKey { return GateKey{ID: id, Head: head, Tree: "tree-1"} }

func TestGateReuse_ASecondGateOnTheSameKeyReusesThePassAndRunsNoGate(t *testing.T) {
	t.Parallel()

	h := reuseHarness(t, []GateResult{{Passed: true, Head: gatedHead, Tree: "tree-1", Steps: stepsPass}}, []GateKey{gateKey("k", gatedHead), gateKey("k", rewrittenHead)})

	_, err := walkText(t, h, bodyFlow(twoGates))

	require.NoError(t, err)
	assert.Equal(t, 1, h.gate.calls)
	require.Len(t, h.commands.ran(), 1)
	assert.Contains(t, h.commands.ran()[0], rewrittenHead, "the reused pass is for the commit of the second gate")
	assert.Contains(t, h.commands.ran()[0], "reused the gate pass of run ")
	assert.Contains(t, h.commands.ran()[0], "tree-1")
}

func TestGateReuse_AnotherKeyRunsTheGate(t *testing.T) {
	t.Parallel()

	h := reuseHarness(t, []GateResult{{Passed: true, Head: gatedHead, Tree: "tree-1", Steps: stepsPass}}, []GateKey{gateKey("k", gatedHead), gateKey("other", gatedHead)})

	_, err := walkText(t, h, bodyFlow(twoGates))

	require.NoError(t, err)
	assert.Equal(t, 2, h.gate.calls)
}

func TestGateReuse_ARedGateIsNeverReusedSoARedNeverBecomesGreen(t *testing.T) {
	t.Parallel()

	red := GateResult{Passed: false, Head: gatedHead, Tree: "tree-1", Report: "boom", Steps: []GateStep{{Name: "command", Status: "fail"}}}
	h := reuseHarness(t, []GateResult{red}, []GateKey{gateKey("k", gatedHead)})

	_, err := walkText(t, h, bodyFlow(twoGates))

	require.NoError(t, err)
	assert.Equal(t, 2, h.gate.calls)
	assert.Contains(t, h.commands.ran()[0], "False")
}

func TestGateReuse_AVoidGateIsNeverReused(t *testing.T) {
	t.Parallel()

	h := reuseHarness(t, []GateResult{{Passed: false, Head: gatedHead, Tree: "tree-1", Steps: stepsUnknown}}, []GateKey{gateKey("k", gatedHead)})

	_, err := walkText(t, h, bodyFlow(twoGates))

	require.NoError(t, err)
	assert.Equal(t, 2, h.gate.calls)
}

func TestGateReuse_AGateThatJudgedAnotherTreeThanItsKeyIsNotRecorded(t *testing.T) {
	t.Parallel()

	h := reuseHarness(t, []GateResult{{Passed: true, Head: rewrittenHead, Tree: "tree-2", Steps: stepsPass}}, []GateKey{gateKey("k", gatedHead)})

	_, err := walkText(t, h, bodyFlow(twoGates))

	require.NoError(t, err)
	assert.Equal(t, 2, h.gate.calls)
}

func TestGateReuse_AKeyThatCannotBeReadRunsTheGate(t *testing.T) {
	t.Parallel()

	h := reuseHarness(t, []GateResult{{Passed: true, Head: gatedHead, Tree: "tree-1", Steps: stepsPass}}, nil)
	h.gate.keyErr = errFake

	_, err := walkText(t, h, bodyFlow(twoGates))

	require.NoError(t, err)
	assert.Equal(t, 2, h.gate.calls)
}

func TestGateReuse_AStoreThatCannotBeReadFindsNoPass(t *testing.T) {
	t.Parallel()

	h := reuseHarness(t, nil, nil)
	engine, err := NewEngine(t.Context(), h.opts, h.deps())
	require.NoError(t, err)
	require.NoError(t, engine.Shutdown())

	_, ok := engine.earlierPass(gateKey("k", gatedHead), "demo")

	assert.False(t, ok)
}

func TestRecordedKey_OnlyAPassOnEveryStepThatJudgedTheKeyedTreeKeepsTheKey(t *testing.T) {
	t.Parallel()

	key := gateKey("k", gatedHead)
	pass := GateResult{Passed: true, Head: gatedHead, Tree: "tree-1", Steps: stepsPass}
	red := GateResult{Passed: false, Head: gatedHead, Tree: "tree-1", Steps: []GateStep{{Name: "command", Status: "fail"}}}
	void := GateResult{Passed: false, Head: gatedHead, Tree: "tree-1", Steps: stepsUnknown}
	flagged := GateResult{Passed: true, Head: gatedHead, Tree: "tree-1", Steps: stepsUnknown}
	moved := GateResult{Passed: true, Head: rewrittenHead, Tree: "tree-1", Steps: stepsPass}

	assert.Equal(t, "k", recordedKey(pass, key))
	assert.Empty(t, recordedKey(red, key))
	assert.Empty(t, recordedKey(void, key))
	assert.Empty(t, recordedKey(flagged, key))
	assert.Empty(t, recordedKey(GateResult{Passed: false, Head: gatedHead, Tree: "tree-1", Steps: stepsPass}, key))
	assert.Empty(t, recordedKey(moved, key))
}

func TestPassOf_AnotherProjectWithTheSameKeyIsNotAPass(t *testing.T) {
	t.Parallel()

	text := `{"outputs":{"passed":true,"steps":[{"name":"command","status":"pass"}]},"key":"k","project":"alpha"}`

	_, ok := passOf(text, "k", "alpha")
	assert.True(t, ok)

	_, ok = passOf(text, "k", "beta")
	assert.False(t, ok)

	_, ok = passOf(text, "other", "alpha")
	assert.False(t, ok)
}

func TestStepsOf_ARecordThatIsNotAPassOnEveryStepReadsAsNotFound(t *testing.T) {
	t.Parallel()

	_, ok := stepsOf(map[string]any{"passed": true, "steps": []any{map[string]any{"name": "command", "status": "unknown"}}})
	assert.False(t, ok)

	_, ok = stepsOf(map[string]any{"passed": true, "steps": make(chan int)})
	assert.False(t, ok)

	_, ok = stepsOf(map[string]any{"passed": true, "steps": "text"})
	assert.False(t, ok)
}
