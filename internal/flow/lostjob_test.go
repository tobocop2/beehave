package flow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/host"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/review"
)

// lostAdvice is the end of the lost error of the fake gate: the cancel command of the adapter.
const lostAdvice = "Run ssh gpu-b systemctl --user stop job-1 to cancel it"

// lostError is the error that the wait of a gate job returns when the link to the host stayed down.
func lostError() error {
	return fmt.Errorf("job job-1: no read of the job answered for 10m0s: %w. %s", host.ErrLost, lostAdvice)
}

// readingClock is a reading of the clock that only the test moves.
type readingClock struct {
	mu  sync.Mutex
	now time.Time
}

func newReadingClock() *readingClock {
	return &readingClock{now: time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC)}
}

func (c *readingClock) read() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

func (c *readingClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = c.now.Add(d)
}

// lostFacts is a project whose host block sets lost_job_wait to limit and whose gate list has the steps build and test.
func lostFacts(limit time.Duration) func(string) model.Project {
	return func(string) model.Project {
		return model.Project{Host: &model.HostConfig{LostJobWait: limit}, GateSteps: []model.GateSpec{{Name: "build", Command: "make"}, {Name: "test", Command: "make test"}}}
	}
}

// lostGateBody starts the host gpu-b, runs the gate there and fails with the fields of the gate result, so the test reads them from the error.
const lostGateBody = `host_up(name = "start", host = "gpu-b")
g = gate(name = "check")
fail("GATE passed=%s steps=%s proof=%s host=%s job=%s report=%s" % (g.passed, [s["status"] for s in g.steps], g.host_loss["proof"], g.host_loss["host"], g.host_loss["job"], g.report))
`

// lostHarness is a harness whose gate loses its job on the host gpu-b, with a reading clock that moves a minute at each read of the proof.
func lostHarness(t *testing.T, limit time.Duration) (*harness, *upHosts, *readingClock) {
	t.Helper()

	h := newHarness(t)
	clock := newReadingClock()
	hosts := &upHosts{started: true, afterProof: func() { clock.advance(time.Minute) }}
	h.hosts, h.reading, h.facts = hosts, clock.read, lostFacts(limit)
	h.gate.err = lostError()

	return h, hosts, clock
}

// hostJobSteps lists the step names of the one host job run of the engine.
func hostJobSteps(t *testing.T, engine *Engine) []string {
	t.Helper()

	rows, err := dbos.ListWorkflows(engine.ctx, dbos.WithFilterName(hostJobName))
	require.NoError(t, err)
	require.Len(t, rows, 1, "one host job ran")

	return stepNamesOf(t, engine, rows[0].ID)
}

func TestLostJob_InsideTheLimitTheWaitRetriesTheStopAndStartsNoSecondJob(t *testing.T) {
	t.Parallel()

	h, hosts, _ := lostHarness(t, 5*time.Minute)
	engine := h.engine(t)

	runInBackground2(engine, writeFlow(t, bodyFlow(lostGateBody)))
	waitOn(t, theLostJobShows(t, h, testItem))

	assert.Equal(t, 5, hosts.proofReads(), "one read of the proof for each look: the clock moves a minute at each read, and the fifth look is at the limit")
	assert.Equal(t, slices.Repeat([]string{"gpu-b/job-1"}, 5), hosts.proved, "each look stops the job on the host that it ran on")
	assert.EqualValues(t, 1, h.gate.submits.Load(), "the wait submits no second job")
	assert.Equal(t, 1, h.gate.calls, "the wait runs the gate once")

	steps := hostJobSteps(t, engine)
	looks := slices.DeleteFunc(slices.Clone(steps), func(name string) bool { return !strings.HasPrefix(name, gateLostLookName) })
	assert.Equal(t, []string{"gate:lost-look.0", "gate:lost-look.1", "gate:lost-look.2", "gate:lost-look.3", "gate:lost-look.4"}, looks)
	assert.Equal(t, 4, strings.Count(strings.Join(steps, " "), "DBOS.sleep"), "a durable sleep between two looks, and none after the last")
}

func TestLostJob_AProofEndsTheWaitAsAVoidGateWithTheHostLoss(t *testing.T) {
	t.Parallel()

	h, hosts, _ := lostHarness(t, time.Hour)
	hosts.proofs = []model.LostProof{{Note: "the link is down"}, {Note: "the link is down"}, {Kind: model.LostStopped}}
	engine := h.engine(t)

	_, err := engine.RunFile(testItem, testProject, writeFlow(t, bodyFlow(lostGateBody)))

	require.ErrorContains(t, err, `GATE passed=False steps=["unknown", "unknown"] proof=stopped host=gpu-b job=job-1 report=the gate job was lost on gpu-b and the proof is stopped`)
	assert.Equal(t, 3, hosts.proofReads(), "the third look proves")
	assert.EqualValues(t, 1, h.gate.submits.Load(), "no second submit")
	assert.Equal(t, 1, h.gate.calls, "no rerun of the gate")

	rows, err := dbos.ListWorkflows(engine.ctx, dbos.WithFilterName(hostJobName), dbos.WithFilterLoadOutput(true))
	require.NoError(t, err)
	require.Len(t, rows, 1)

	var out ExclusiveOutput

	text, isText := rows[0].Output.(string)
	require.True(t, isText)
	require.NoError(t, json.Unmarshal([]byte(text), &out))
	require.NotNil(t, out.Lost, "the gate run returns the lost job")
	assert.Equal(t, LostJob{Kind: kindGate, Job: "job-1", Host: "gpu-b", Proof: model.LostProof{Kind: model.LostStopped}}, LostJob{Kind: out.Lost.Kind, Job: out.Lost.Job, Host: out.Lost.Host, Proof: out.Lost.Proof})
	assert.False(t, out.Lost.Since.IsZero(), "the wait records its start")
}

func TestLostJob_AProvedLossOfAGateWithNoListGivesTheOneStepOfItsCommandAsUnknown(t *testing.T) {
	t.Parallel()

	h, hosts, _ := lostHarness(t, time.Hour)
	h.facts = func(string) model.Project { return model.Project{Host: &model.HostConfig{LostJobWait: time.Hour}} }
	hosts.proofs = []model.LostProof{{Kind: model.LostGone}}
	engine := h.engine(t)

	_, err := engine.RunFile(testItem, testProject, writeFlow(t, bodyFlow(lostGateBody)))

	require.ErrorContains(t, err, `GATE passed=False steps=["unknown"] proof=gone host=gpu-b job=job-1`, "a gate with no list is still void, not red")
}

func TestLostJob_TheLimitIsReadFromTheConfigAtTheTimeOfTheLook(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	clock := newReadingClock()
	hosts := &upHosts{started: true}
	h.hosts, h.reading, h.facts = hosts, clock.read, lostFacts(2*time.Minute)
	h.gate.err = lostError()

	file := writeFlow(t, bodyFlow(lostGateBody))

	stopCtx, stop := context.WithCancel(t.Context())
	first, err := NewEngine(stopCtx, h.opts, h.deps())
	require.NoError(t, err)
	require.NoError(t, first.Launch())

	ended := runInBackground2(first, file)

	waitOn(t, condition(func() bool { return hosts.proofReads() >= 2 }))
	stop()
	require.NoError(t, first.Shutdown())
	receive(t, ended)

	// The old limit has passed. The config of the next engine raises it, so the looks go on.
	clock.advance(5 * time.Minute)
	h.facts = lostFacts(10 * time.Minute)
	before := hosts.proofReads()

	next := h.engine(t)
	done := runInBackground2(next, file)

	waitOn(t, condition(func() bool { return hosts.proofReads() >= before+2 }))
	hosts.prove(model.LostProof{Kind: model.LostStopped})

	outcome := receive(t, done)
	require.ErrorContains(t, outcome.err, "proof=stopped", "the wait went on past the old limit and ended with the proof")
	assert.EqualValues(t, 1, h.gate.submits.Load())
}

// lostOnceGate loses the host job of its first gate and passes every later gate.
type lostOnceGate struct {
	submits atomic.Int32
	runs    atomic.Int32
}

func (*lostOnceGate) Key(context.Context, string, string, string, string, string) (GateKey, error) {
	return GateKey{}, nil
}

func (g *lostOnceGate) Submit(context.Context, string, string, string, string, string) (GateJob, error) {
	return GateJob{ID: fmt.Sprintf("job-%d", g.submits.Add(1)), Head: gatedHead}, nil
}

func (g *lostOnceGate) Run(context.Context, string, string, string, GateJob) (GateResult, error) {
	if g.runs.Add(1) == 1 {
		return GateResult{}, lostError()
	}

	return GateResult{Passed: true, Head: gatedHead, Steps: commandStep(true)}, nil
}

func TestLostJob_ACanceledWaitReadsTheProofOnceAndNeverCancelsTheJob(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.forge.state = review.Standing{Number: testPull, Head: "A"}
	hosts := &upHosts{started: true}
	h.hosts, h.lostPoll = hosts, time.Hour

	gated := &lostOnceGate{}
	engine := launchHostEngine(t, h, gated)

	old := queueHostGate(engine, testProject, testItem, "A", "box")

	// The first look gives no proof, and the wait sleeps for an hour before the next.
	waitOn(t, condition(func() bool {
		rows, err := dbos.ListWorkflows(engine.ctx, dbos.WithFilterName(hostJobName))
		if err != nil || len(rows) != 1 {
			return false
		}

		steps, err := dbos.GetWorkflowSteps(engine.ctx, rows[0].ID)

		return err == nil && slices.ContainsFunc(steps, func(s dbos.StepInfo) bool { return s.StepName == "DBOS.sleep" })
	}))
	require.Equal(t, 1, hosts.proofReads())

	setHead(h, "B")

	current := queueHostGate(engine, testProject, testItem, "B", "box")

	require.ErrorIs(t, endsAfterTicks(t, h, old).err, errSuperseded)
	waitOn(t, condition(func() bool {
		h.tick(t)

		return hosts.proofReads() == 2
	}))
	require.NoError(t, receive(t, current).err, "the gate of the new head runs once the wait has ended")

	assert.Equal(t, 2, hosts.proofReads(), "the canceled wait reads the proof once more, and never again")
	assert.Equal(t, []string{"box/job-1", "box/job-1"}, hosts.proved)
	assert.Zero(t, hosts.teardownCount(), "the wait never banks or stops the host")
	assert.EqualValues(t, 2, gated.submits.Load(), "one job for each gate: the wait submits none")
	assert.EqualValues(t, 2, gated.runs.Load())
}

// lostOnCancelGate waits on the job of its first gate until the cancel of the run, then reports the job as lost, as a wait does when the cancel
// comes while it reads a dropped link. Every later gate passes.
type lostOnCancelGate struct {
	lostOnceGate
	waiting atomic.Bool
}

func (g *lostOnCancelGate) Run(ctx context.Context, project, item, host string, job GateJob) (GateResult, error) {
	if g.runs.Load() > 0 {
		return g.lostOnceGate.Run(ctx, project, item, host, job)
	}

	g.runs.Add(1)
	g.waiting.Store(true)
	hang(ctx.Done())

	return GateResult{}, lostError()
}

// everyHostJobStep lists the step names of every host job run of the engine, sorted, and the count of the runs.
func everyHostJobStep(t *testing.T, engine *Engine) ([]string, int) {
	t.Helper()

	rows, err := dbos.ListWorkflows(engine.ctx, dbos.WithFilterName(hostJobName))
	require.NoError(t, err)

	var names []string
	for _, row := range rows {
		names = append(names, stepNamesOf(t, engine, row.ID)...)
	}

	slices.Sort(names)

	return names, len(rows)
}

func TestLostJob_ALossThatACancelEndedStartsNoWaitAndReadsNoProof(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.forge.state = review.Standing{Number: testPull, Head: "A"}
	hosts := &upHosts{started: true}
	h.hosts, h.lostPoll = hosts, time.Hour

	gated := &lostOnCancelGate{}
	engine := launchHostEngine(t, h, gated)

	old := queueHostGate(engine, testProject, testItem, "A", "box")

	waitOn(t, condition(gated.waiting.Load))
	setHead(h, "B")

	current := queueHostGate(engine, testProject, testItem, "B", "box")

	require.ErrorIs(t, endsAfterTicks(t, h, old).err, errSuperseded)
	require.NoError(t, receive(t, current).err, "the gate of the new head runs once the canceled gate has ended")

	names, runs := everyHostJobStep(t, engine)
	require.Equal(t, 2, runs, "one host job run for each head")
	assert.Equal(t, []string{kindGate, gateJobName, gateJobName}, names,
		"the canceled run keeps only the step that submitted its job: the gate step of a canceled run is not recorded, and no wait starts")
	assert.Zero(t, hosts.proofReads(), "a loss after a cancel reads no proof")
	assert.EqualValues(t, 2, gated.submits.Load(), "one job for each gate")
}

func TestLostJob_TheWaitHoldsTheHostSoASecondGateOnItStartsOnlyAfterTheProof(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.forge.state = review.Standing{Number: testPull, Head: "A"}
	hosts := &upHosts{started: true}
	h.hosts, h.lostPoll = hosts, 50*time.Millisecond

	gated := &lostOnceGate{}
	engine := launchHostEngine(t, h, gated)

	first := queueHostGate(engine, "project-one", "1", "A", "box")

	waitOn(t, condition(func() bool { return hosts.proofReads() >= 1 }))

	second := queueHostGate(engine, "project-two", "2", "A", "box")

	waitOn(t, condition(waitsOnHost(engine, 1)))

	// The queue polls once for each second, so the second gate would have started within three polls had the wait freed the host.
	require.Never(t, func() bool { return gated.submits.Load() > 1 || gated.runs.Load() > 1 }, 3*storePoll, 100*time.Millisecond,
		"the second gate waits while the first waits for the proof on the same host")
	assert.Greater(t, hosts.proofReads(), 1, "the first gate was still looking for the proof")

	hosts.prove(model.LostProof{Kind: model.LostStopped})

	end := receive(t, first)
	require.NoError(t, end.err)
	require.NotNil(t, end.out.Lost, "the first gate ended with the proof")
	assert.Equal(t, model.LostStopped, end.out.Lost.Proof.Kind)
	require.NoError(t, receive(t, second).err, "the second gate runs once the wait has ended")
	assert.EqualValues(t, 2, gated.submits.Load())
}

func TestLostJob_AProofReadThatFailsFailsTheGate(t *testing.T) {
	t.Parallel()

	h, hosts, _ := lostHarness(t, time.Hour)
	hosts.proofErr = errors.New("the job id is not a unit of this host")

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, bodyFlow(lostGateBody)))

	require.ErrorContains(t, err, "read the proof that the lost job job-1 on host gpu-b can no longer run: the job id is not a unit of this host")
	assert.Equal(t, 1, hosts.proofReads(), "a read that fails ends the wait")
}

// lastProofWarning is the start of the warning that the last read of a canceled wait logs when it gives no proof.
const lastProofWarning = "the gate stopped while its lost job may still run on the host"

// lostStoppedLog runs the end of a canceled wait whose last read of the proof is answered by hosts, and returns what the engine logged and the error it gave back.
func lostStoppedLog(t *testing.T, hosts *upHosts) (string, error) {
	t.Helper()

	engine, err := NewEngine(t.Context(), newHarness(t).opts, Deps{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	var logged bytes.Buffer

	engine.logger = newLogger(&logged, runRedactor(nil))
	engine.deps.Hosts = hosts

	canceled, cancel := dbos.WithCancel(engine.ctx)
	cancel()

	stopped := errors.New("the run was canceled")

	err = engine.lostStopped(canceled, ExclusiveInput{Project: testProject, Item: testItem}, LostJob{Job: "job-1", Host: "gpu-b"}, stopped)

	return logged.String(), err
}

func TestLostJob_ACanceledWaitLogsTheWarningOnlyWhenItsLastReadGivesNoProof(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		hosts    *upHosts
		warnings int
		contains string
	}{
		{"logs_nothing_when_the_read_has_a_proof", &upHosts{proofs: []model.LostProof{{Kind: model.LostStopped}}}, 0, ""},
		{"logs_once_when_the_read_has_no_proof_and_no_error", &upHosts{proofs: []model.LostProof{{Note: "the link is down"}}}, 1, "the link is down"},
		{"logs_once_with_the_error_when_the_read_fails", &upHosts{proofs: []model.LostProof{{Kind: model.LostStopped}}, proofErr: errors.New("the job id is not a unit of this host")}, 1, "the job id is not a unit of this host"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			logged, err := lostStoppedLog(t, tc.hosts)

			require.EqualError(t, err, "the run was canceled", "the end of the wait gives back the error that stopped it")
			assert.Equal(t, 1, tc.hosts.proofReads(), "the canceled wait reads the proof once")
			assert.Equal(t, tc.warnings, strings.Count(logged, lastProofWarning))
			assert.Contains(t, logged, tc.contains)
		})
	}
}

func TestLostJob_ABuildWithNoHostReaderWaitsToTheLimitWithNoProof(t *testing.T) {
	t.Parallel()

	engine := &Engine{deps: Deps{Clock: clock{Reading: newReadingClock().read}}}
	since := engine.deps.Clock.reading()

	look, err := engine.lookLost(t.Context(), testProject, LostJob{Job: "job-1", Host: "gpu-b", Since: since.Add(-config.DefaultLostJobWait)})

	require.NoError(t, err)
	assert.Empty(t, look.Proof.Kind)
	assert.Equal(t, "this build cannot read the host", look.Proof.Note)
	assert.True(t, look.Limit, "a project with no host block waits for the default of host.lost_job_wait")
}

func TestLostJob_AStoreThatRefusesTheStartOfTheWaitEndsIt(t *testing.T) {
	t.Parallel()

	engine, err := NewEngine(t.Context(), newHarness(t).opts, Deps{})
	require.NoError(t, err)
	require.NoError(t, engine.Shutdown())

	_, err = engine.holdLost(engine.ctx, ExclusiveInput{Project: testProject, Item: testItem}, LostJob{Job: "job-1", Host: "gpu-b"})

	require.Error(t, err)
}

// theLostJobShows is the fact that the open run of item shows the lost gate job and its question. Its value is the status of the run. The run waits for the owner, so no run is named.
func theLostJobShows(t *testing.T, h *harness, item string) waiting[RunInfo] {
	t.Helper()

	read := newStatusRead(h.client(t, testVersion))

	return waiting[RunInfo]{what: "the run of item " + item + " to wait for the owner on the lost gate job", last: read.last, read: func() (RunInfo, bool, error) {
		for _, info := range read.next() {
			if info.Item == item && info.Question != "" && info.LostJob != nil {
				return info, true, nil
			}
		}

		return RunInfo{}, false, nil
	}}
}

// endedRuns lists the status of every run of the engine with the workflow name, so a test reads how many child runs a gate started and whether they ended.
func endedRuns(t *testing.T, engine *Engine, name string) []dbos.WorkflowStatusType {
	t.Helper()

	rows, err := dbos.ListWorkflows(engine.ctx, dbos.WithFilterName(name))
	require.NoError(t, err)

	states := make([]dbos.WorkflowStatusType, len(rows))
	for i, row := range rows {
		states[i] = row.Status
	}

	return states
}

// lostChildFlow is a parent that spawns item 21 and gathers it with fail = "raise". The child runs its gate on the host gpu-b.
const lostChildFlow = `def worker(item):
    host_up(name = "start", host = "gpu-b")
    gate(name = "check")

def main(item):
    gather(spawn(items = ["21"], entry = "worker"), fail = "raise")
`

func TestLostJob_NoProofAtTheLimitIsAnOwnerWaitAndNotAFailedStep(t *testing.T) {
	t.Parallel()

	h, hosts, clock := lostHarness(t, 2*time.Minute)
	since := clock.read()
	engine := h.engine(t)

	parent := runInBackground2(engine, writeFlow(t, lostChildFlow))
	child := waitOn(t, theLostJobShows(t, h, "21"))

	assert.Equal(t, runRunning, child.State, "the child run waits for the owner and reads running")
	assert.Equal(t, &LostJobShown{Job: "job-1", Host: "gpu-b", Since: since, Limit: "2m0s"}, child.LostJob)
	assert.Contains(t, child.Question, "job-1", "the question names the job")
	assert.Contains(t, child.Question, "gpu-b", "the question names the host")
	assert.Contains(t, child.Question, lostAdvice, "the question names the cancel command of the adapter")
	assert.Contains(t, child.Question, "CONTINUE")
	assert.Equal(t, 2, hosts.proofReads(), "the second look is at the limit")
	assert.EqualValues(t, 1, h.gate.submits.Load())

	assert.Equal(t, []dbos.WorkflowStatusType{dbos.WorkflowStatusSuccess}, endedRuns(t, engine, hostJobName), "the host job run has ended, so the host slot is free")
	assert.Equal(t, []dbos.WorkflowStatusType{dbos.WorkflowStatusSuccess}, endedRuns(t, engine, exclusiveName), "the exclusive run has ended, so the project slot is free")

	steps, err := dbos.GetWorkflowSteps(engine.ctx, child.ID)
	require.NoError(t, err)

	for _, step := range steps {
		require.NoError(t, step.Error, "no step of the child failed: %s", step.StepName)
	}

	assert.Equal(t, dbos.WorkflowStatusPending, childStatus(t, engine, "21"), "the parent round keeps the child: the raise of gather cancels no run that waits")
	select {
	case outcome := <-parent:
		t.Fatalf("the parent ended while its child waits for the owner: %v", outcome.err)
	default:
	}
}

func TestLostJob_ContinueWithNoProofWaitsAgainAndStartsNoGateJob(t *testing.T) {
	t.Parallel()

	h, hosts, _ := lostHarness(t, 2*time.Minute)
	engine := h.engine(t)

	runInBackground2(engine, writeFlow(t, bodyFlow(lostGateBody)))
	waitOn(t, theLostJobShows(t, h, testItem))

	before := hosts.proofReads()
	require.NoError(t, h.client(t, testVersion).Reply(testItem, Reply{Text: "CONTINUE"}))

	waitOn(t, conditionOf(runOfEngine(t, engine), func() bool { return slices.Contains(stepNamesOf(t, engine, testRun), "gate:check#1/lost-ask.0") }))

	again := waitOn(t, theLostJobShows(t, h, testItem))

	assert.NotNil(t, again.LostJob, "the run still shows the lost job")
	assert.Equal(t, before+1, hosts.proofReads(), "the answer reads the proof once")
	assert.EqualValues(t, 1, h.gate.submits.Load(), "no gate job started")
	assert.Equal(t, 1, h.gate.calls)
	assert.Len(t, endedRuns(t, engine, exclusiveName), 1, "no second gate run")
	assert.NotContains(t, stepNamesOf(t, engine, testRun), "gate:check#1/lost-ask.1", "the run waits for the next answer")
}

func TestLostJob_EachAnswerReadsTheProofInTheNextNumberedStep(t *testing.T) {
	t.Parallel()

	h, hosts, _ := lostHarness(t, 2*time.Minute)
	engine := h.engine(t)

	runInBackground2(engine, writeFlow(t, bodyFlow(lostGateBody)))
	waitOn(t, theLostJobShows(t, h, testItem))

	before := hosts.proofReads()
	client := h.client(t, testVersion)

	require.NoError(t, client.Reply(testItem, Reply{Text: "CONTINUE"}))
	waitOn(t, conditionOf(runOfEngine(t, engine), func() bool { return slices.Contains(stepNamesOf(t, engine, testRun), "gate:check#1/lost-ask.0") }))
	waitOn(t, theLostJobShows(t, h, testItem))

	require.NoError(t, client.Reply(testItem, Reply{Text: "CONTINUE"}))
	waitOn(t, conditionOf(runOfEngine(t, engine), func() bool { return slices.Contains(stepNamesOf(t, engine, testRun), "gate:check#1/lost-ask.1") }))

	steps := stepNamesOf(t, engine, testRun)

	assert.Contains(t, steps, "gate:check#1/lost-ask.0")
	assert.Contains(t, steps, "gate:check#1/lost-ask.1", "the second answer reads the proof in the next numbered step")
	assert.NotContains(t, steps, "gate:check#1/lost-ask.-1")
	assert.Equal(t, before+2, hosts.proofReads(), "each answer reads the proof once, in a step of its own")
	assert.EqualValues(t, 1, h.gate.submits.Load(), "no gate job started")
}

// goldenLostAskSteps are the steps of the item run of lostGateBody whose gate waited for the owner, and whose first answer came after the host came back.
// The read of the proof after an answer and the record of the loss are sub-steps of the gate call, recorded before the gate call itself.
var goldenLostAskSteps = []string{"host_up:start#1", "gate:tip", "exclusive", "wait:notify", "gate:check#1/lost-ask.0", "gate:check#1/host-loss", "scope:check#1", "gate:check#1"}

func TestLostJob_ContinueAfterTheHostCameBackReadsTheProofBeforeAnyRerun(t *testing.T) {
	t.Parallel()

	h, hosts, _ := lostHarness(t, 2*time.Minute)
	engine := h.engine(t)

	done := runInBackground2(engine, writeFlow(t, bodyFlow(lostGateBody)))
	waitOn(t, theLostJobShows(t, h, testItem))

	before := hosts.proofReads()

	hosts.prove(model.LostProof{Kind: model.LostStopped})
	require.NoError(t, h.client(t, testVersion).Reply(testItem, Reply{Text: "CONTINUE"}))

	outcome := receive(t, done)

	require.ErrorContains(t, outcome.err, `GATE passed=False steps=["unknown", "unknown"] proof=stopped host=gpu-b job=job-1 report=the gate job was lost on gpu-b and the proof is stopped`)
	assert.Equal(t, before+1, hosts.proofReads(), "the answer reads the proof once")
	assert.EqualValues(t, 1, h.gate.submits.Load(), "no gate job started on the head before the proof, and none after: the rerun is the workflow's")
	assert.Equal(t, 1, h.gate.calls)
	assert.Len(t, endedRuns(t, engine, exclusiveName), 1)

	// The calls of the workflow have a colon in their names; the lifecycle steps and the records of the library have none.
	steps := slices.DeleteFunc(stepNamesOf(t, engine, testRun), func(name string) bool { return !strings.Contains(name, ":") && name != exclusiveName })
	assert.False(t, unpatchedChange(goldenLostAskSteps, steps), "the steps of the owner wait for a lost gate job changed with no marker: put the change behind a patch. The gate steps belong to the gate module and wait:notify to the core module (stepmodules.go)")
	assert.EqualValues(t, 1, gateRecordOf(t, engine, testRun, "gate:check#1")[hostLossesKey], "a loss proven after an answer of the owner counts as the first loss of the tree")

	shown, err := dbos.GetEvent[LostJobShown](engine.ctx, testRun, lostJobKey, time.Millisecond)
	require.NoError(t, err)
	assert.Zero(t, shown, "the proof clears the lost job from the status")
}

func TestLostJob_AProofReadThatFailsAfterAnAnswerFailsTheGate(t *testing.T) {
	t.Parallel()

	h, hosts, _ := lostHarness(t, 2*time.Minute)
	engine := h.engine(t)

	done := runInBackground2(engine, writeFlow(t, bodyFlow(lostGateBody)))
	waitOn(t, theLostJobShows(t, h, testItem))

	hosts.mu.Lock()
	hosts.proofErr = errors.New("the job id is not a unit of this host")
	hosts.mu.Unlock()
	require.NoError(t, h.client(t, testVersion).Reply(testItem, Reply{Text: "CONTINUE"}))

	outcome := receive(t, done)

	require.ErrorContains(t, outcome.err, `gate "check" lost its job: read the proof that the lost job job-1 on host gpu-b can no longer run: the job id is not a unit of this host`)
	assert.EqualValues(t, 1, h.gate.submits.Load())
}

func TestLostJob_ACancelWhileTheRunAsksTheOwnerEndsTheRunAndStartsNoGateJob(t *testing.T) {
	t.Parallel()

	h, hosts, _ := lostHarness(t, 2*time.Minute)
	engine := h.engine(t)

	done := runInBackground2(engine, writeFlow(t, bodyFlow(lostGateBody)))
	waitOn(t, theLostJobShows(t, h, testItem))

	before := hosts.proofReads()
	require.NoError(t, h.client(t, testVersion).Cancel(testItem))

	outcome := waitOn(t, waiting[runOutcome]{what: "the canceled run to end", ch: done, read: func() (runOutcome, bool, error) {
		h.tick(t)

		return runOutcome{}, false, nil
	}})
	require.Error(t, outcome.err, "the canceled run ends with an error")

	assert.Equal(t, before, hosts.proofReads(), "the cancel reads no proof in the item run")
	assert.EqualValues(t, 1, h.gate.submits.Load())
}

func TestLostJob_AnAskThatCannotShowTheLostJobEndsTheGate(t *testing.T) {
	t.Parallel()

	engine := idleEngine(t, newHarness(t))

	_, err := engine.askLost(engine.ctx, ExclusiveInput{Project: testProject, Item: testItem}, "gate:check#1", LostJob{Job: "job-1", Host: "gpu-b", Limit: true})

	require.ErrorContains(t, err, "item 12 cannot show the lost gate job")
}
