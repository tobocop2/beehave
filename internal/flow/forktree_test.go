package flow

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/model"
)

// recordOf makes the record of a star step that returned outputs. The store keeps the output of a step as JSON text.
func recordOf(t *testing.T, id int, name string, outputs map[string]any) dbos.StepInfo {
	t.Helper()

	text, err := json.Marshal(stepResult{Outputs: outputs})
	require.NoError(t, err)

	return dbos.StepInfo{StepID: id, StepName: name, Output: string(text)}
}

// treeRecord makes the record of the step that made the worktree of a round item at commit.
func treeRecord(t *testing.T, id int, commit string) dbos.StepInfo {
	t.Helper()

	text, err := json.Marshal(commit)
	require.NoError(t, err)

	return dbos.StepInfo{StepID: id, StepName: treeMakeStepName, Output: string(text)}
}

func TestForkHead_TheLastHeadThatAStepBeforeTheStartRecordedWins(t *testing.T) {
	t.Parallel()

	records := []dbos.StepInfo{
		recordOf(t, 1, "agent:a#1", map[string]any{recordedStartHead: "base"}),
		recordOf(t, 2, "agent:a#1", map[string]any{headShaField: "one"}),
		recordOf(t, 3, "agent:b#2", map[string]any{recordedStartHead: "one"}),
		recordOf(t, 4, "agent:b#2", map[string]any{headShaField: "two"}),
		recordOf(t, 5, "agent:c#3", map[string]any{headShaField: "three"}),
	}

	assert.Equal(t, "two", forkHead(records, 5), "the work of the steps before the start, and no later work")
	assert.Equal(t, "one", forkHead(records, 3))
}

func TestForkHead_AStartBeforeAnyAgentTakesTheTreeStartThenTheBaselineOfTheFirstAgent(t *testing.T) {
	t.Parallel()

	agent := recordOf(t, 3, "agent:a#1", map[string]any{recordedStartHead: "base"})

	assert.Equal(t, "tree", forkHead([]dbos.StepInfo{treeRecord(t, 1, "tree"), agent}, 2), "a round item starts where its worktree was made")
	assert.Equal(t, "base", forkHead([]dbos.StepInfo{agent}, 3), "a tracker item starts where the first agent call found the folder")
	assert.Empty(t, forkHead([]dbos.StepInfo{recordOf(t, 1, "command:a#1", nil)}, 2), "a run with no recorded head has none to restore")

	unreadable := dbos.StepInfo{StepID: 1, StepName: treeMakeStepName, Output: "not json"}
	assert.Equal(t, "base", forkHead([]dbos.StepInfo{unreadable, agent}, 2), "a tree record that cannot be read gives no commit")
}

func TestCallStart_ACallWithNoRecordOfItsOwnStartsAtItsFirstRecord(t *testing.T) {
	t.Parallel()

	group := []dbos.StepInfo{{StepID: 4, StepName: "agent:a#1/commit"}, {StepID: 5, StepName: "agent:a#1/render"}}

	assert.Equal(t, uint(4), callStart(group, "gate"))
	assert.Equal(t, uint(5), callStart(group[1:], "agent:a#1"), "a record of the call itself is the start")
}

func TestForkBranch_ARoundItemHasNoBranchAndAnyOtherItemHasOneOfItsOwnPerAttempt(t *testing.T) {
	t.Parallel()

	assert.Empty(t, forkBranch("round/main", 2))
	assert.Equal(t, "12-r2", forkBranch("12", 2))
	assert.Equal(t, "12-r3", forkBranch("12", 3))
}

// placeLog is a placer that keeps its calls as project|item|head|branch and fails with err.
type placeLog struct {
	mu    sync.Mutex
	calls []string
	err   error
}

func (p *placeLog) Place(_ context.Context, project, item, head, branch string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.calls = append(p.calls, strings.Join([]string{project, item, head, branch}, "|"))

	return p.err
}

// editFlow is an agent call that commits its edit, and a command named show after it.
func editFlow() string {
	return bodyFlow(agentCallText("") + "command(name = \"show\", run = [\"show\"])\n")
}

// editedRun runs editFlow once on a real git folder and returns the rig, the head that the agent committed, and the client with a placer.
func editedRun(t *testing.T) (*agentRig, string, *Client, *placeLog) {
	t.Helper()

	rig := commitRig(t)
	rig.writes(map[string]string{"a.txt": "one"}, goodOutput)

	_, err := rig.run(t, editFlow(), "Write to {{ .output_file }}\n")
	require.NoError(t, err)

	placer := &placeLog{}
	client := rig.h.client(t, testVersion)
	client.give.Place = placer

	return rig, rig.tip(t), client, placer
}

func TestFork_TheWorktreeStartsAtTheHeadThatTheCopiedStepsRecordedOnABranchOfItsOwn(t *testing.T) {
	t.Parallel()

	_, head, client, placer := editedRun(t)

	id, err := client.Fork(ForkRequest{Run: testRun, Step: "command:show#1"})
	require.NoError(t, err)

	assert.Equal(t, "item-12-r2", id)
	assert.Equal(t, []string{testProject + "|" + testItem + "|" + head + "|12-r2"}, placer.calls)
}

// idOf is the id of the record with the name in records.
func idOf(t *testing.T, records []dbos.StepInfo, name string) uint {
	t.Helper()

	at := slices.IndexFunc(records, func(record dbos.StepInfo) bool { return record.StepName == name })
	require.GreaterOrEqual(t, at, 0, "a record named %s", name)

	return uint(records[at].StepID)
}

// An agent call has many records: its start, its render, its read, and its commit after the read. The right start of a call is its own first record,
// because DBOS copies the records before the start and runs from it. The last record would copy the agent and run only its read again. The commit
// that the grouping puts in front of the next call is a record of the agent call, so a start at it would run the commit again on a folder reset to the head before it.
func TestFork_TheStartOfACallWithManyRecordsIsItsOwnFirstRecord(t *testing.T) {
	t.Parallel()

	_, _, client, _ := editedRun(t)

	records, err := client.records(testRun)
	require.NoError(t, err)

	agent, err := forkStart(records, ForkRequest{Run: testRun, Step: "agent:a#1"})
	require.NoError(t, err)
	assert.Equal(t, idOf(t, records, "agent:a#1/start"), agent, "the agent call starts at its first record, so the agent runs again")

	show, err := forkStart(records, ForkRequest{Run: testRun, Step: "command:show#1"})
	require.NoError(t, err)
	assert.Equal(t, idOf(t, records, "command:show#1"), show, "the call after the agent starts after the commit of the agent")
	assert.Greater(t, show, idOf(t, records, "agent:a#1/commit"))
}

// A run that failed in a step shows that step in beehave status, so a fork can start from it, as it starts from a finished step.
func TestFork_TheStepThatARunFailedInIsAStartLikeAFinishedStep(t *testing.T) {
	t.Parallel()

	rig := commitRig(t)
	rig.reply("", agent.Result{Outcome: agent.OutcomeTimedOut, Note: "too slow"})

	_, err := rig.run(t, editFlow(), "Write to {{ .output_file }}\n")
	require.Error(t, err)

	records, err := rig.h.client(t, testVersion).records(testRun)
	require.NoError(t, err)

	start, err := forkStart(records, ForkRequest{Run: testRun, Step: "agent:a#1"})

	require.NoError(t, err)
	assert.Equal(t, idOf(t, records, "agent:a#1/start"), start)

	_, err = forkStart(records, ForkRequest{Run: testRun, Step: "agent:zz#9"})
	require.ErrorContains(t, err, "Its steps are", "a name that no step has is refused")
	require.ErrorContains(t, err, "agent:a#1", "the failed step is listed")
}

func TestFork_AHeadThatTheCloneCannotFindRefusesTheForkAndLeavesNoRunAndNoClaim(t *testing.T) {
	t.Parallel()

	rig, head, client, placer := editedRun(t)
	placer.err = errors.New("the clone does not hold commit " + head)
	takes := rig.h.life.takes[testItem]

	_, err := client.Fork(ForkRequest{Run: testRun, Step: "command:show#1"})

	require.ErrorIs(t, err, errBadFork)
	require.ErrorContains(t, err, head, "the error names the commit")

	rows, listErr := dbos.ListWorkflows(client.flow, dbos.WithFilterWorkflowIDs("item-12-r2"))
	require.NoError(t, listErr)
	assert.Empty(t, rows, "no run was made")
	assert.Equal(t, takes, rig.h.life.takes[testItem], "no claim was taken")
}

func TestFork_TwoForksAtOnceMakeOneRunAndOneClearRefusalAndTakeOneClaim(t *testing.T) {
	t.Parallel()

	w := forkSeed(t)
	before := w.h.life.takes[testItem]

	var (
		wg      sync.WaitGroup
		results [2]string
		errs    [2]error
	)

	for i := range results {
		wg.Go(func() { results[i], errs[i] = w.client.Fork(ForkRequest{Run: testRun, Step: "command:two#2"}) })
	}

	wg.Wait()

	made := slices.DeleteFunc(slices.Clone(results[:]), func(id string) bool { return id == "" })
	refused := slices.DeleteFunc(slices.Clone(errs[:]), func(err error) bool { return err == nil })

	require.Equal(t, []string{"item-12-r2"}, made, "one run")
	require.Len(t, refused, 1, "one refusal")
	require.ErrorIs(t, refused[0], errWrongState)
	require.ErrorContains(t, refused[0], "has an open run", "the refusal says why")

	w.settled(t, "item-12-r2")
	assert.Equal(t, before+1, w.h.life.takes[testItem], "the refused fork took no claim")
	assert.Len(t, w.h.life.releasedBy(), 2, "every claim taken is released: the first run and the fork")
}

const forkPushFlow = `def main(item):
    f = facts()
    command(name = "one", run = ["one"])
    push(name = "p", branch = f.vars["branch"], head = "aaa", expect = "")
    command(name = "three", run = ["three"])
`

// pushWorld is an item that pushed head aaa to branch fix-7 and ended.
func pushWorld(t *testing.T) *forkWorld {
	t.Helper()

	w := &forkWorld{h: newHarness(t), mode: &atomic.Value{}}
	w.h.facts = func(project string) model.Project {
		return model.Project{Name: project, Repo: "o/n", BaseBranch: "main", Vars: map[string]any{"branch": "fix-7"}}
	}
	w.engine = w.h.engine(t)
	w.client = w.h.client(t, testVersion)

	_, err := w.engine.RunFile(testItem, testProject, writeFlow(t, forkPushFlow), modeOff())
	require.NoError(t, err)

	return w
}

func (w *forkWorld) failed(t *testing.T, id string) string {
	t.Helper()

	require.Eventually(t, func() bool {
		rows, err := dbos.ListWorkflows(w.client.flow, dbos.WithFilterWorkflowIDs(id))

		return err == nil && len(rows) == 1 && rows[0].Status == dbos.WorkflowStatusError
	}, forkSettle, 25*time.Millisecond)

	return errorText(w.row(t, id).Error)
}

func TestFork_APushStopsTheItemWhenTheBranchesOfTheForkCannotBeRead(t *testing.T) {
	t.Parallel()

	var armed atomic.Bool

	w := &forkWorld{h: newHarness(t)}
	w.h.commands.before = func(context.Context, CommandRequest) error {
		armed.Store(true)

		return nil
	}
	w.engine = w.h.engineWith(t, func(e *Engine) { e.ctx = &listFailsWhenArmed{Context: e.ctx, armed: &armed} })

	_, err := w.engine.RunFile(testItem, testProject, writeFlow(t, "def main(item):\n    command(name = \"arm\", run = [\"arm\"])\n    push(name = \"p\", branch = \"fix-7\", head = \"aaa\", expect = \"\")\n"), modeOff())

	require.ErrorContains(t, err, "read the vars of run item-12-r1")
	assert.Empty(t, w.h.forge.pushed, "nothing was pushed")
}

func TestFork_AForkNeverPushesToABranchThatTheRunItCameFromPushed(t *testing.T) {
	t.Parallel()

	w := pushWorld(t)

	id, err := w.client.Fork(ForkRequest{Run: testRun, Step: "command:one#1"})
	require.NoError(t, err)

	text := w.failed(t, id)

	assert.Contains(t, text, "fix-7", "the error names the branch")
	assert.Equal(t, []string{testProject + "|" + testItem + "|fix-7|aaa|"}, w.h.forge.pushed, "the fork pushed nothing")
}

func TestFork_AForkPushesToAnotherBranchWhenItsVarsNameOne(t *testing.T) {
	t.Parallel()

	w := pushWorld(t)

	w.fork(t, testRun, "command:one#1", map[string]any{"branch": "fix-7-again"})

	assert.Equal(t, []string{testProject + "|" + testItem + "|fix-7|aaa|", testProject + "|" + testItem + "|fix-7-again|aaa|"}, w.h.forge.pushed)
}

func TestFork_AForkOfAForkStillRefusesTheBranchesOfEveryRunBeforeIt(t *testing.T) {
	t.Parallel()

	w := pushWorld(t)

	parent := w.fork(t, testRun, "command:one#1", map[string]any{"branch": "fix-7-again"})

	id, err := w.client.Fork(ForkRequest{Run: parent, Step: "command:one#1", Vars: map[string]any{"branch": "fix-7"}})
	require.NoError(t, err)

	assert.Contains(t, w.failed(t, id), "fix-7", "the first run pushed it")
}
