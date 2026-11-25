package flow

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
)

// lossyGate gives a void result with no host cause for its first `void` runs, loses the host job of the next `lose` runs, and passes every later run, on the head that it is given.
type lossyGate struct {
	cleanTree

	head string
	void int32
	lose int32
	runs atomic.Int32
	jobs atomic.Int32
}

func (*lossyGate) Key(context.Context, string, string, string, string, string) (GateKey, error) {
	return GateKey{}, nil
}

func (g *lossyGate) Submit(context.Context, string, string, string, string, string, string, model.Slot) (GateJob, error) {
	return GateJob{ID: "job-" + strconv.Itoa(int(g.jobs.Add(1))), Head: g.head}, nil
}

func (g *lossyGate) Run(context.Context, string, string, string, GateJob) (GateResult, error) {
	run := g.runs.Add(1)
	if run <= g.void {
		return GateResult{Head: g.head, Steps: []GateStep{{Name: "command", Status: "unknown"}}}, nil
	}

	if run <= g.void+g.lose {
		return GateResult{}, lostError()
	}

	return GateResult{Passed: true, Head: g.head, Tree: exampleTree, Steps: commandStep(true)}, nil
}

// loopSteps is the steps argument of the loops that the flows below run: the models that their tests do not assert.
const loopSteps = "{\"implement\": {\"model\": \"sonnet\"}, \"review\": {\"model\": \"opus\"}, \"fix\": {\"model\": \"sonnet\"}}"

// loopFlow runs the review loop of the library on the base main, on the host gpu-b, and returns the loop result fields that the tests read.
const loopFlow = "load(\"@beehave//review_fix.star\", \"review_fix\")\n\n" +
	"def main(item):\n" +
	"    return pipeline(item)\n\n" +
	"def pipeline(item):\n" +
	"    host_up(name = \"start\", host = \"gpu-b\")\n" +
	"    impl = agent(name = \"implement\", prompt = \"prompts/a.md\", model = \"sonnet\")\n" +
	"    loop = review_fix(item, impl, \"prompts/a.md\", \"prompts/a.md\", base = \"main\", rounds = 2, steps = " + loopSteps + ")\n" +
	"    return {\"approved\": loop.review != None and loop.review.verdict == \"approve\", \"host_losses\": loop.host_losses, \"host_lost\": loop.host_lost, \"held\": loop.held}\n"

// hostLossLimit is HOST_LOSS_LIMIT of the library.
const hostLossLimit = 5

// loopValue is what loopFlow returns.
type loopValue struct {
	Approved   bool   `json:"approved"`
	HostLosses int    `json:"host_losses"`
	HostLost   bool   `json:"host_lost"`
	Held       string `json:"held"`
}

// lossyLoop runs loopFlow over a gate that gives `void` plain voids, then loses `lose` jobs, with a review that approves everything. It returns the result and the names of the steps of the run.
func lossyLoop(t *testing.T, void, lose int32) (loopValue, []string) {
	t.Helper()

	value, steps, err := lossyLoopWith(t, void, lose, "")
	require.NoError(t, err)

	return value, steps
}

// lossyLoopWith is lossyLoop with extra arguments of review_fix, written as ", name = value", and the error of the run.
func lossyLoopWith(t *testing.T, void, lose int32, extra string) (loopValue, []string, error) {
	t.Helper()

	rig := newAgentRig(t)
	clock := newReadingClock()
	hosts := &upHosts{started: true, proofs: []model.LostProof{{Kind: model.LostStopped}}, afterProof: func() { clock.advance(time.Minute) }}
	rig.h.hosts, rig.h.reading, rig.h.facts = hosts, clock.read, lostFacts(time.Hour)
	rig.h.gate.via = &lossyGate{head: rig.head, void: void, lose: lose}
	rig.h.gate.checks = cleanChecks()
	rig.baseAt(t, "main")
	rig.reply(`{"verdict":"approve","findings":"[]"}`, exited)

	file := writeFlow(t, strings.Replace(loopFlow, "rounds = 2", "rounds = 2"+extra, 1))
	require.NoError(t, os.MkdirAll(filepath.Join(filepath.Dir(file), "prompts"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(file), "prompts", "a.md"), []byte("Write to {{ .output_file }}\n"), 0o600))

	engine := rig.h.engine(t)

	result, err := runPipelineOf(engine, testItem, file)
	if err != nil {
		return loopValue{}, stepNamesOf(t, engine, testRun), err
	}

	var value loopValue

	require.NoError(t, json.Unmarshal(result.Value, &value))

	return value, stepNamesOf(t, engine, testRun), nil
}

// stepsNamed counts the steps whose name contains part. The sub-steps of a call, such as its host-loss record, are not counted.
func stepsNamed(names []string, part string) int {
	count := 0

	for _, name := range names {
		if strings.Contains(name, part) && !strings.Contains(name, "/") {
			count++
		}
	}

	return count
}

func TestHostLoss_AGateVoidWithAHostCauseRerunsAndLeavesTheFixCountAndTheVoidCountAlone(t *testing.T) {
	t.Parallel()

	value, steps := lossyLoop(t, 0, 2)

	assert.True(t, value.Approved, "the third gate passed and the loop went on to the review")
	assert.Equal(t, 2, value.HostLosses, "the loop keeps the highest count that a gate read: the passing rerun reads 0")
	assert.False(t, value.HostLost)
	assert.Equal(t, 1, stepsNamed(steps, "gate:candidate-gate#"), "the first gate")
	assert.Equal(t, 1, stepsNamed(steps, "gate:candidate-gate-host-1#"), "the first host rerun")
	assert.Equal(t, 1, stepsNamed(steps, "gate:candidate-gate-host-2#"), "the second host rerun")
	assert.Zero(t, stepsNamed(steps, "candidate-gate-void-"), "a host loss takes no plain rerun")
	assert.Zero(t, stepsNamed(steps, "gate-void-"), "a host loss asks the owner nothing")
	assert.Zero(t, stepsNamed(steps, "fix-scoped-"), "a host loss starts no fix")
}

func TestReviewFix_ALoopThatStoppedOnTheHostLossLimitReturnsItsCountAndLeavesTheItemUnheld(t *testing.T) {
	t.Parallel()

	value, steps := lossyLoop(t, 0, 100)

	assert.True(t, value.HostLost, "the fifth loss of the tree stops the loop")
	assert.Equal(t, hostLossLimit, value.HostLosses)
	assert.Empty(t, value.Held, "the item is not held: a host loss is no fault of the item")
	assert.False(t, value.Approved)
	assert.Equal(t, hostLossLimit, stepsNamed(steps, "gate:candidate-gate"), "one gate for each loss, and no gate after the limit")
	assert.Zero(t, stepsNamed(steps, "fix-scoped-"), "no fix agent reads the host report")
	assert.Zero(t, stepsNamed(steps, "gate-void-"), "the limit asks the owner nothing: the caller does")
}

func TestReviewFix_AGateVoidWithNoHostCauseIsAPlainVoidRerunAndNeverAHostRerun(t *testing.T) {
	t.Parallel()

	value, steps := lossyLoop(t, 1, 0)

	assert.True(t, value.Approved, "the plain rerun passed and the loop went on to the review")
	assert.Zero(t, value.HostLosses, "a void with no host cause is no host loss")
	assert.False(t, value.HostLost)
	assert.Equal(t, 1, stepsNamed(steps, "gate:candidate-gate-void-1#"), "the plain rerun")
	assert.Zero(t, stepsNamed(steps, "candidate-gate-host-"), "a void with no host cause takes no host rerun")
}
