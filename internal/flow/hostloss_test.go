package flow

import (
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
)

// lostTreeFlow is a parent with three children, one after the other. The gate of items 21 and 22 runs on the host gpu-b, and item 23 lands
// the pull requests that replace PRS with one merge there.
const lostTreeFlow = `def worker(item):
    host_up(name = "start", host = "gpu-b")
    gate(name = "check")

def lander(item):
    host_up(name = "start", host = "gpu-b")
    merge(prs = [PRS], base = "main", repo = "o/r", name = "b")

def main(item):
    gather(spawn(items = ["21"], entry = "worker"), fail = "raise")
    gather(spawn(items = ["22"], entry = "worker"), fail = "raise")
    gather(spawn(items = ["23"], entry = "lander"), fail = "raise")
`

// lastRunOf is the id of the last run of item.
func lastRunOf(t *testing.T, engine *Engine, item string) string {
	t.Helper()

	last, found, err := latestRun(viewOf(engine.ctx, engine.dir), item)
	require.NoError(t, err)
	require.True(t, found, "item %s has no run", item)

	return last.Status.ID
}

// gateRecordOf is the recorded outputs of the gate call of a run, as the record holds them.
func gateRecordOf(t *testing.T, engine *Engine, id, call string) map[string]any {
	t.Helper()

	steps, err := dbos.GetWorkflowSteps(engine.ctx, id, dbos.WithStepsLoadOutput(true))
	require.NoError(t, err)

	for _, step := range steps {
		if step.StepName != call {
			continue
		}

		text, isText := step.Output.(string)
		require.True(t, isText)

		var gate recorded[map[string]any]

		require.NoError(t, json.Unmarshal([]byte(text), &gate))

		return gate.Outputs
	}

	t.Fatalf("the run %s has no gate call %s", id, call)

	return nil
}

// statusJSONOf is what beehave status <run id> --json prints for a run, as a JSON object.
func statusJSONOf(t *testing.T, h *harness, id string) map[string]any {
	t.Helper()

	view, err := h.client(t, testVersion).Steps(id)
	require.NoError(t, err)

	text, err := json.Marshal(view)
	require.NoError(t, err)

	var shown map[string]any

	require.NoError(t, json.Unmarshal(text, &shown))

	return shown
}

func TestHostLoss_TheRootRunCountsLossesOfItsChildRuns(t *testing.T) {
	t.Parallel()

	// The gate loses the first three jobs: the gates of items 21 and 22, then the batch gate of item 23, whose rerun passes.
	rig, _ := lostBatchRig(t, 3)
	engine := rig.engine(t)
	prs := `struct(item = "1", head = "` + rig.heads["1"] + `", closes = []), struct(item = "3", head = "` + rig.heads["3"] + `", closes = [])`

	_, err := engine.RunFile(testItem, testProject, writeFlow(t, strings.Replace(lostTreeFlow, "PRS", prs, 1)), modeOff())
	require.NoError(t, err)

	first, second, lander := lastRunOf(t, engine, "21"), lastRunOf(t, engine, "22"), lastRunOf(t, engine, "23")

	assert.Contains(t, stepNamesOf(t, engine, first), "gate:check#1/host-loss", "the item run records the loss as its own step")
	assert.Contains(t, stepNamesOf(t, engine, second), "gate:check#1/host-loss", "the item run records the loss as its own step")
	assert.Contains(t, stepNamesOf(t, engine, lander), "batch:b.gate#1/host-loss", "the item run records the loss of the batch gate as its own step")
	assert.EqualValues(t, 1, gateRecordOf(t, engine, first, "gate:check#1")[hostLossesKey], "the first loss of the tree")
	assert.EqualValues(t, 2, gateRecordOf(t, engine, second, "gate:check#1")[hostLossesKey], "the second result counts the loss of the first child and its own")
	assert.Equal(t, []string{"1", "3"}, rig.merger.landed(), "the batch gate ran again after the proof, and the batch landed")

	for _, id := range []string{testRun, first, second, lander} {
		assert.EqualValues(t, 3, statusJSONOf(t, rig.h, id)[hostLossesKey], "status of %s shows the losses of the tree of the run", id)
	}
}

func TestHostLoss_TwoLossesOfOneRunCountOneThenTwo(t *testing.T) {
	t.Parallel()

	h, hosts, _ := lostHarness(t, time.Hour)
	hosts.proofs = []model.LostProof{{Kind: model.LostStopped}}
	engine := h.engine(t)

	_, err := engine.RunFile(testItem, testProject, writeFlow(t, bodyFlow("host_up(name = \"start\", host = \"gpu-b\")\ngate(name = \"first\")\ngate(name = \"second\")\n")), modeOff())
	require.NoError(t, err)

	assert.Subset(t, stepNamesOf(t, engine, testRun), []string{"gate:first#1/host-loss", "gate:second#2/host-loss"}, "the run records each loss as its own step")
	assert.EqualValues(t, 1, gateRecordOf(t, engine, testRun, "gate:first#1")[hostLossesKey], "the first loss of the run")
	assert.EqualValues(t, 2, gateRecordOf(t, engine, testRun, "gate:second#2")[hostLossesKey], "the second loss counts the earlier loss of the same run once, and its own")
	assert.EqualValues(t, 2, statusJSONOf(t, h, testRun)[hostLossesKey], "status counts each loss of the run once")
}

// treeReads is a context of the engine that counts the reads of runs and of steps once armed, and fails each of them.
type treeReads struct {
	dbos.Context

	armed atomic.Bool
	reads atomic.Int32
}

func (r *treeReads) read() error {
	if !r.armed.Load() {
		return nil
	}

	r.reads.Add(1)

	return errors.New("the store failed")
}

func (r *treeReads) ListWorkflows(_ dbos.Client, opts ...dbos.ListWorkflowsOption) ([]dbos.WorkflowStatus, error) {
	if err := r.read(); err != nil {
		return nil, err
	}

	return dbos.ListWorkflows(r.Context, opts...)
}

func (r *treeReads) GetWorkflowSteps(_ dbos.Client, id string, opts ...dbos.GetWorkflowStepsOption) ([]dbos.StepInfo, error) {
	if err := r.read(); err != nil {
		return nil, err
	}

	return dbos.GetWorkflowSteps(r.Context, id, opts...)
}

func TestHostLoss_AResumeAfterTheRecordKeepsTheRecordedCountAndWalksNoTree(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	opts := rig.h.opts
	state, err := openStore(opts)
	require.NoError(t, err)

	held, err := claimFolder(state.dir, applicationVersion(opts.Version), ModeRun)
	require.NoError(t, err)

	engine := newEngine(opts, rig.h.deps(), state, held, moduleRegistry(t))
	require.NoError(t, engine.open(t.Context(), opts, state))
	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	tree := &treeReads{Context: engine.ctx}
	counting := &Engine{ctx: tree, redact: engine.redact}

	// The run records the loss, then fails as a run that stops after the step. Between the failure and the resume, every read of the tree fails.
	loss := func(ctx dbos.Context, in ItemInput) (int, error) {
		record, err := counting.recordLoss(ctx, in.Item, "gate:check#1", LostJob{Kind: kindGate, Job: "job-1", Host: "gpu-b"})
		if err != nil {
			return 0, err
		}

		if !tree.armed.Load() {
			return 0, errors.New("the run stopped after the record of the loss")
		}

		return record.Losses, nil
	}

	dbos.RegisterWorkflow(engine.ctx, loss, dbos.WithWorkflowName(itemName))
	require.NoError(t, dbos.Launch(engine.ctx))

	id := runID(testItem, 1)
	handle, err := dbos.RunWorkflow(engine.ctx, loss, ItemInput{Item: testItem, Project: testProject}, dbos.WithWorkflowID(id))
	require.NoError(t, err)

	_, err = resultOfHandle(t, handle)
	require.ErrorContains(t, err, "the run stopped after the record of the loss")

	steps, err := dbos.GetWorkflowSteps(engine.ctx, id)
	require.NoError(t, err)
	require.Equal(t, []string{"gate:check#1/host-loss"}, stepNamesOf(t, engine, id), "the run recorded the loss and nothing else")

	tree.armed.Store(true)

	// The rewind keeps every recorded step, as beehave resume does for a run that failed after them, so the run replays the record of the loss.
	resumed, err := dbos.RewindWorkflow[int](engine.ctx, id, dbos.WithRewindStartStep(uint(len(steps))))
	require.NoError(t, err)

	count, err := resultOfHandle(t, resumed)
	require.NoError(t, err, "the replay reads the recorded count, so a tree that cannot be read does not fail it")
	assert.Equal(t, 1, count, "the replay keeps the count that the step recorded")
	assert.Zero(t, tree.reads.Load(), "the replay walks no tree")
}

func TestHostLoss_TheRecordAndTheCountFailOutsideARun(t *testing.T) {
	t.Parallel()

	engine := idleEngine(t, newHarness(t))

	_, err := engine.recordLoss(engine.ctx, testItem, "gate:check#1", LostJob{Kind: kindGate, Job: "job-1", Host: "gpu-b"})

	require.ErrorContains(t, err, "record the lost job of the gate: ")

	_, err = engine.treeHostLosses(engine.ctx, testItem)

	require.ErrorContains(t, err, "read the id of the run of item 12", "the count needs the run to find its tree")
}

func TestHostLoss_TheCountOfTheStatusReportsAReadOfTheTreeThatFailed(t *testing.T) {
	t.Parallel()

	engine := deepTree(t, newAgentRig(t))
	c := &Client{flow: &failingRead{Context: engine.ctx, at: 1}}

	err := c.addHostLosses(runID("3", 1), &RunProgress{})

	require.ErrorContains(t, err, "the store failed", "the climb to the root fails")

	// The climb from the root reads the root once, then the walk reads its steps.
	c.flow = &failingRead{Context: engine.ctx, at: 2}
	err = c.addHostLosses(runID("1", 1), &RunProgress{})

	require.ErrorContains(t, err, "read the steps of "+runID("1", 1), "the walk from the root fails")
}

func TestHostLoss_AGateThatLostNothingCountsNoneAndStatusOmitsTheCount(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	engine := h.engine(t)

	_, err := engine.RunFile(testItem, testProject, writeFlow(t, bodyFlow("gate(name = \"check\")\n")), modeOff())
	require.NoError(t, err)

	outputs := gateRecordOf(t, engine, testRun, "gate:check#1")
	assert.EqualValues(t, 0, outputs[hostLossesKey], "a gate that lost nothing counts no loss")
	assert.Equal(t, map[string]any{}, outputs[hostLossKey])
	assert.NotContains(t, statusJSONOf(t, h, testRun), hostLossesKey, "status omits a count of 0")
}
