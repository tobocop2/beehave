package flow

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
)

// forkFlow runs three commands that each print the vars mode and keep. With mode wait it asks the owner a question after step two.
const forkFlow = `def main(item):
    f = facts()
    command(name = "one", run = ["one", f.vars["mode"], f.vars["keep"]])
    command(name = "two", run = ["two", f.vars["mode"], f.vars["keep"]])
    if f.vars["mode"] == "wait":
        wait(name = "ask", question = "What now?")
    command(name = "three", run = ["three", f.vars["mode"], f.vars["keep"]])
`

const forkSettle = 20 * time.Second

// forkWorld is an item that ran once, with the vars mode = a and keep = k, and the engine that runs the forks of it.
type forkWorld struct {
	h      *harness
	engine *Engine
	client *Client
	file   string
	mode   *atomic.Value
}

func forkSeed(t *testing.T) *forkWorld {
	t.Helper()

	w := &forkWorld{h: newHarness(t), mode: &atomic.Value{}}
	w.mode.Store("a")
	w.h.facts = func(project string) model.Project {
		return model.Project{Name: project, Repo: "o/n", BaseBranch: "main", Vars: map[string]any{"mode": fmt.Sprint(w.mode.Load()), "keep": "k"}}
	}
	w.engine = w.h.engine(t)
	w.client = w.h.client(t, testVersion)
	w.file = writeFlow(t, forkFlow)

	_, err := w.engine.RunFile(testItem, testProject, w.file, modeOff())
	require.NoError(t, err)

	return w
}

func (w *forkWorld) ran() string { return strings.Join(w.h.commands.ranBy(testItem), " | ") }

// fork forks run source from step with the vars, and waits until the new run ends.
func (w *forkWorld) fork(t *testing.T, source, step string, vars map[string]any) string {
	t.Helper()

	id, err := w.client.Fork(ForkRequest{Run: source, Step: step, Vars: vars})
	require.NoError(t, err)
	w.settled(t, id)

	return id
}

func (w *forkWorld) settled(t *testing.T, id string) {
	t.Helper()

	require.Eventually(t, func() bool {
		rows, err := dbos.ListWorkflows(w.client.flow, dbos.WithFilterWorkflowIDs(id))

		return err == nil && len(rows) == 1 && rows[0].Status == dbos.WorkflowStatusSuccess
	}, forkSettle, 25*time.Millisecond)
}

func (w *forkWorld) row(t *testing.T, id string) dbos.WorkflowStatus {
	t.Helper()

	rows, err := dbos.ListWorkflows(w.client.flow, dbos.WithFilterWorkflowIDs(id), dbos.WithFilterLoadInput(true), dbos.WithFilterLoadOutput(true))
	require.NoError(t, err)
	require.Len(t, rows, 1)

	return rows[0]
}

// snapshot is what an owner reads of a run: its status, error, input, output and every step with its output. Attributes are left out.
func (w *forkWorld) snapshot(t *testing.T, id string) string {
	t.Helper()

	row := w.row(t, id)

	steps, err := dbos.GetWorkflowSteps(w.client.flow, id, dbos.WithStepsLoadOutput(true))
	require.NoError(t, err)

	parts := []string{fmt.Sprint(row.Status, errorText(row.Error), row.Input, row.Output)}
	for _, step := range steps {
		parts = append(parts, fmt.Sprint(step.StepID, step.StepName, step.Output, step.Error))
	}

	return strings.Join(parts, "\n")
}

// A forked run needs no code of beehave to carry its overlay: DBOS stores an attribute on a run that ended and copies it to the fork.
func TestFork_TheStoreSetsAnAttributeOnAnEndedRunAndTheForkCopiesIt(t *testing.T) {
	t.Parallel()

	w := forkSeed(t)

	require.NoError(t, dbos.SetWorkflowAttributes(w.client.flow, testRun, map[string]any{"fork:item-12-r2": map[string]any{"mode": "b"}}, dbos.WithMergeAttributes()))

	_, err := dbos.ForkWorkflow[ItemResult](w.client.flow, dbos.ForkWorkflowInput{OriginalWorkflowID: testRun, ForkedWorkflowID: "item-12-r2", StartStep: 1, QueueName: rootsQueueName})
	require.NoError(t, err)

	assert.Equal(t, map[string]any{"mode": "b"}, w.row(t, "item-12-r2").Attributes["fork:item-12-r2"], "the copy has the overlay from the first instant")
}

func TestFork_AForkFromAStepKeepsTheEarlierOutputsAndRunsTheRestAsTheNextAttempt(t *testing.T) {
	t.Parallel()

	w := forkSeed(t)

	id := w.fork(t, testRun, "command:two#2", nil)

	assert.Equal(t, "item-12-r2", id)
	assert.Equal(t, "one a k | two a k | three a k | two a k | three a k", w.ran(), "step one does not run again")

	last, found, err := latestRun(w.client.store(), testItem)
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, id, last.Status.ID, "the new run is the latest run of the item")
	assert.Equal(t, testRun, last.Status.ForkedFrom)

	before, err := dbos.GetWorkflowSteps(w.client.flow, testRun, dbos.WithStepsLoadOutput(true))
	require.NoError(t, err)

	after, err := dbos.GetWorkflowSteps(w.client.flow, id, dbos.WithStepsLoadOutput(true))
	require.NoError(t, err)
	require.NotEmpty(t, before)
	require.NotEmpty(t, after)

	for i := range before {
		if before[i].StepName == "command:two#2" {
			break
		}

		assert.Equal(t, before[i].Output, after[i].Output, "the recorded output of %s is kept", before[i].StepName)
	}
}

func TestFork_VarsChangeOnlyTheKeysTheyNameFromTheStepOn(t *testing.T) {
	t.Parallel()

	w := forkSeed(t)

	w.fork(t, testRun, "command:two#2", map[string]any{"mode": "b"})

	assert.Equal(t, "one a k | two a k | three a k | two b k | three b k", w.ran(), "mode changes from step two on, and keep stays as the source run recorded it")
}

func TestFork_AForkWithNoVarsReadsTheRecordedVarsNotTheLiveConfigNorAStaleKey(t *testing.T) {
	t.Parallel()

	w := forkSeed(t)
	w.mode.Store("c")

	// An earlier fork that failed after it stored its overlay leaves the key of the id that this fork takes.
	require.NoError(t, dbos.SetWorkflowAttributes(w.client.flow, testRun, map[string]any{"fork:item-12-r2": map[string]any{"mode": "stale"}}, dbos.WithMergeAttributes()))

	w.fork(t, testRun, "command:three#3", nil)

	assert.Equal(t, "one a k | two a k | three a k | three a k", w.ran())
}

func TestFork_AForkOfAForkKeepsTheOverlayOfItsParentAndMergesNewVarsOverIt(t *testing.T) {
	t.Parallel()

	w := forkSeed(t)

	parent := w.fork(t, testRun, "command:two#2", map[string]any{"mode": "b"})
	w.fork(t, parent, "command:three#3", map[string]any{"keep": "x"})

	assert.Equal(t, "one a k | two a k | three a k | two b k | three b k | three b x", w.ran(), "the child keeps mode b of its parent and takes keep x")
}

func TestFork_TheNewRunTakesOneFreshClaimAndReleasesItsOwnToken(t *testing.T) {
	t.Parallel()

	w := forkSeed(t)
	before := w.h.life.takes[testItem]

	w.fork(t, testRun, "command:two#2", nil)

	assert.Equal(t, before+1, w.h.life.takes[testItem])

	released := w.h.life.releasedBy()
	require.Len(t, released, 2)
	assert.NotEqual(t, released[0], released[1], "the new run releases the fresh token, not the token of the old run")
}

func TestFork_AForkFromTheClaimStepTakesNoClaimOfItsOwn(t *testing.T) {
	t.Parallel()

	w := forkSeed(t)
	before := w.h.life.takes[testItem]

	w.fork(t, testRun, string(model.StepClaim), nil)

	assert.Equal(t, before+1, w.h.life.takes[testItem], "the run claims by itself: the fork took none")
}

func TestFork_TheOldRunKeepsItsStatusInputOutputAndSteps(t *testing.T) {
	t.Parallel()

	w := forkSeed(t)
	before := w.snapshot(t, testRun)

	w.fork(t, testRun, "command:two#2", map[string]any{"mode": "b"})

	assert.Equal(t, before, w.snapshot(t, testRun))
}

func TestFork_ARequestThatCannotStartARunIsRefusedWithTheCauseAndMakesNoRun(t *testing.T) {
	t.Parallel()

	w := forkSeed(t)
	other := w.h.client(t, "other-build")

	for name, tc := range map[string]struct {
		client *Client
		req    ForkRequest
		want   string
	}{
		"an unknown step lists the steps of the run": {w.client, ForkRequest{Run: testRun, Step: "command:nine#9"}, "command:two#2"},
		"no step":                        {w.client, ForkRequest{Run: testRun}, "--from"},
		"a fact of the project":          {w.client, ForkRequest{Run: testRun, Step: "command:two#2", Vars: map[string]any{"gate": "x"}}, `"gate"`},
		"a var that a run cannot record": {w.client, ForkRequest{Run: testRun, Step: "command:two#2", Vars: map[string]any{"ratio": math.NaN()}}, "cannot record"},
		"a run that does not exist":      {w.client, ForkRequest{Run: "item-99-r1", Step: "command:two#2"}, "item-99-r1"},
		"a name that is not a run id":    {w.client, ForkRequest{Run: "twelve", Step: "command:two#2"}, "not a run id"},
		"a run of another build":         {other, ForkRequest{Run: testRun, Step: "command:two#2"}, "version"},
	} {
		id, err := tc.client.Fork(tc.req)

		require.ErrorContains(t, err, tc.want, name)
		assert.Empty(t, id, name)
	}

	rows, err := dbos.ListWorkflows(w.client.flow, dbos.WithFilterWorkflowIDPrefix(runPrefix+testItem+attemptMarker))
	require.NoError(t, err)
	assert.Len(t, rows, 1, "no refusal made a run")
	assert.Equal(t, "one a k | two a k | three a k", w.ran())
}

func TestFork_ARunThatBeehaveRunDidNotStartAsARootIsRefused(t *testing.T) {
	t.Parallel()

	w := forkSeed(t)

	var seen atomic.Int32

	w.client.flow = &forkFault{Client: w.client.flow, list: func(rows []dbos.WorkflowStatus, err error) ([]dbos.WorkflowStatus, error) {
		if seen.Add(1) == 1 {
			rows[0].Name = passName
		}

		return rows, err
	}}

	_, err := w.client.Fork(ForkRequest{Run: testRun, Step: "command:two#2"})
	require.ErrorContains(t, err, "Fork the root run")

	w.client.flow = &forkFault{Client: w.client.flow, list: func(rows []dbos.WorkflowStatus, err error) ([]dbos.WorkflowStatus, error) {
		rows[0].Input = `{"item":"12","project":"p","depth":1}`

		return rows, err
	}}

	_, err = w.client.Fork(ForkRequest{Run: testRun, Step: "command:two#2"})
	require.ErrorContains(t, err, "Fork the root run", "a spawn child is refused too")
}

func TestFork_AnOpenRunOfTheItemRefusesTheForkOfItselfAndOfAnEarlierRun(t *testing.T) {
	t.Parallel()

	w := forkSeed(t)

	waiting, err := w.client.Fork(ForkRequest{Run: testRun, Step: "command:two#2", Vars: map[string]any{"mode": "wait"}})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		last, found, err := latestRun(w.client.store(), testItem)

		return err == nil && found && last.Status.ID == waiting && !last.terminal() && strings.HasSuffix(w.ran(), "two wait k")
	}, forkSettle, 25*time.Millisecond)

	_, err = w.client.Fork(ForkRequest{Run: testRun, Step: "command:two#2"})
	require.ErrorContains(t, err, "beehave cancel 12 first", "the source ended but a later run is open")

	_, err = w.client.Fork(ForkRequest{Run: waiting, Step: "command:two#2"})
	require.ErrorContains(t, err, "beehave cancel 12 first", "the source is the open run")

	rows, err := dbos.ListWorkflows(w.client.flow, dbos.WithFilterWorkflowIDPrefix(runPrefix+testItem+attemptMarker))
	require.NoError(t, err)
	assert.Len(t, rows, 2, "the refused forks made no run")
}

func TestFork_ACanceledRunForksLikeAnyEndedRun(t *testing.T) {
	t.Parallel()

	w := forkSeed(t)

	waiting, err := w.client.Fork(ForkRequest{Run: testRun, Step: "command:two#2", Vars: map[string]any{"mode": "wait"}})
	require.NoError(t, err)
	require.Eventually(t, func() bool { return strings.HasSuffix(w.ran(), "two wait k") }, forkSettle, 25*time.Millisecond)
	require.NoError(t, w.client.Cancel(testItem))

	id := w.fork(t, waiting, "command:two#2", map[string]any{"mode": "d"})

	assert.Equal(t, "item-12-r3", id)
}

func TestFork_AClaimThatTheItemDoesNotGiveRefusesTheForkAndNamesTheCommandToRepeat(t *testing.T) {
	t.Parallel()

	w := forkSeed(t)
	w.h.life.failing = map[string]bool{"claim": true}

	_, err := w.client.Fork(ForkRequest{Run: testRun, Step: "command:two#2"})

	require.ErrorContains(t, err, "Then run beehave fork item-12-r1 --from command:two#2 again")
	assert.Equal(t, "one a k | two a k | three a k", w.ran())
}

func TestFork_AForkThatFailsGivesTheFreshClaimBack(t *testing.T) {
	t.Parallel()

	for name, fault := range map[string]*forkFault{
		"the store refuses the overlay": {set: errors.New("no attributes")},
		"the store refuses the fork":    {fork: errors.New("no fork")},
	} {
		w := forkSeed(t)
		w.client.flow = fault.over(w.client.flow)

		_, err := w.client.Fork(ForkRequest{Run: testRun, Step: "command:two#2"})
		require.Error(t, err, name)

		released := w.h.life.releasedBy()
		require.Len(t, released, 2, name)
		assert.Equal(t, "owner-12#2", released[1], "%s: the fresh token goes back", name)

		saved, err := savedOwner(w.h.opts.StateDir, "item-12-r2")
		require.NoError(t, err, name)
		assert.Empty(t, saved, "%s: the saved token is removed", name)
	}
}

func TestFork_AReadThatFailsRefusesTheForkWithTheCauseAtEachPoint(t *testing.T) {
	t.Parallel()

	storeFailed := errors.New("the store failed")

	for name, tc := range map[string]struct {
		fault *forkFault
		want  string
	}{
		"the run": {&forkFault{failList: 1}, "read run item-12-r1"},
		"the input": {&forkFault{list: func(rows []dbos.WorkflowStatus, err error) ([]dbos.WorkflowStatus, error) {
			rows[0].Input = "{"
			return rows, err
		}}, "read the input of run item-12-r1"},
		"the item":                 {&forkFault{failList: 2}, "list the runs of item 12"},
		"the steps":                {&forkFault{steps: storeFailed}, "the store failed"},
		"the overlay":              {&forkFault{failList: 3}, "read the vars of run item-12-r1"},
		"the store of the overlay": {&forkFault{set: storeFailed}, "store the vars of run item-12-r2"},
		"the fork":                 {&forkFault{fork: storeFailed}, "fork run item-12-r1 as item-12-r2"},
	} {
		w := forkSeed(t)
		w.client.flow = tc.fault.over(w.client.flow)

		_, err := w.client.Fork(ForkRequest{Run: testRun, Step: "command:two#2"})

		require.ErrorContains(t, err, tc.want, name)
	}
}

func TestFork_ALockThatCannotBeTakenRefusesTheFork(t *testing.T) {
	t.Parallel()

	w := forkSeed(t)
	w.client.dir = w.h.opts.StateDir + "/missing"

	_, err := w.client.Fork(ForkRequest{Run: testRun, Step: "command:two#2"})

	require.ErrorContains(t, err, probeLockName)
}

func TestFork_AReadOfTheOverlayThatFailsFailsTheFactsOfTheForkedRun(t *testing.T) {
	t.Parallel()

	var armed atomic.Bool

	w := &forkWorld{h: newHarness(t)}
	w.h.facts = func(project string) model.Project {
		armed.Store(true)

		return model.Project{Name: project, Repo: "o/n", BaseBranch: "main", Vars: map[string]any{"mode": "a", "keep": "k"}}
	}
	w.engine = w.h.engineWith(t, func(e *Engine) { e.ctx = &listFailsWhenArmed{Context: e.ctx, armed: &armed} })

	_, err := w.engine.RunFile(testItem, testProject, writeFlow(t, forkFlow), modeOff())

	require.ErrorContains(t, err, "read the vars of run item-12-r1")
}

func TestForkVars_OnlyAJSONObjectIsAVarsFile(t *testing.T) {
	t.Parallel()

	none, err := ParseForkVars(nil)
	require.NoError(t, err)
	assert.Nil(t, none)

	vars, err := ParseForkVars([]byte(`{"mode": "b", "n": 2}`))
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"mode": "b", "n": float64(2)}, vars)

	for _, text := range []string{`["mode"]`, `"mode"`, `{`, ``} {
		_, err = ParseForkVars([]byte(text))
		require.ErrorContains(t, err, "one JSON object", text)
	}
}

// forkFault is a client store that fails or changes the reads and writes of a fork. Each other call goes to the store.
type forkFault struct {
	dbos.Client

	// failList is the number of the list that fails, counted from 1, and list changes the rows of each list.
	failList int
	list     func([]dbos.WorkflowStatus, error) ([]dbos.WorkflowStatus, error)
	steps    error
	set      error
	fork     error

	lists atomic.Int32
}

// over returns the fault on top of the store, and keeps the settings that the test made.
func (f *forkFault) over(store dbos.Client) *forkFault {
	f.Client = store

	return f
}

func (f *forkFault) ListWorkflows(_ dbos.Client, opts ...dbos.ListWorkflowsOption) ([]dbos.WorkflowStatus, error) {
	rows, err := dbos.ListWorkflows(f.Client, opts...)

	if int(f.lists.Add(1)) == f.failList {
		return nil, errors.New("the store failed")
	}

	if f.list != nil {
		return f.list(rows, err)
	}

	return rows, err
}

func (f *forkFault) GetWorkflowSteps(_ dbos.Client, id string, opts ...dbos.GetWorkflowStepsOption) ([]dbos.StepInfo, error) {
	if f.steps != nil {
		return nil, f.steps
	}

	return dbos.GetWorkflowSteps(f.Client, id, opts...)
}

func (f *forkFault) SetWorkflowAttributes(_ dbos.Client, id string, attributes map[string]any, opts ...dbos.SetWorkflowAttributesOption) error {
	if f.set != nil {
		return f.set
	}

	return dbos.SetWorkflowAttributes(f.Client, id, attributes, opts...)
}

func (f *forkFault) ForkWorkflow(_ dbos.Client, input dbos.ForkWorkflowInput) (dbos.WorkflowHandle[any], error) {
	if f.fork != nil {
		return nil, f.fork
	}

	return f.Client.ForkWorkflow(f.Client, input)
}

// listFailsWhenArmed is a store whose list of runs fails once armed is set. Each other read goes to the store.
type listFailsWhenArmed struct {
	dbos.Context

	armed *atomic.Bool
}

func (l *listFailsWhenArmed) ListWorkflows(_ dbos.Client, opts ...dbos.ListWorkflowsOption) ([]dbos.WorkflowStatus, error) {
	if l.armed.Load() {
		return nil, errors.New("the store failed")
	}

	return dbos.ListWorkflows(l.Context, opts...)
}
