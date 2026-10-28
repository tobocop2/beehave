package flow

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/agent"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/proc"
)

const (
	foreignItem   = "13"
	badInputItem  = "12"
	nobodyQueue   = "nobody-listens"
	staleVersion  = "v0"
	otherVersion  = "v2"
	tickEvery     = 5 * time.Millisecond
	stuckItemID   = "item-77-r1"
	stuckItemName = "77"
)

// idleEngine builds an engine over the harness that is not launched and returns it. The cleanup shuts it down.
func idleEngine(t *testing.T, harness *harness) *Engine {
	t.Helper()

	engine, err := NewEngine(t.Context(), harness.opts, harness.deps())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	return engine
}

// launchedWith builds and launches an engine over the harness with other collaborators. The cleanup shuts it down.
func launchedWith(t *testing.T, harness *harness, change func(*Deps)) *Engine {
	t.Helper()

	deps := harness.deps()
	change(&deps)

	engine, err := NewEngine(t.Context(), harness.opts, deps)
	require.NoError(t, err)
	require.NoError(t, engine.Launch())
	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	return engine
}

func TestHolds_ACallOutsideAWorkflowHoldsAndTracksNothing(t *testing.T) {
	t.Parallel()

	engine := idleEngine(t, newHarness(t))

	id, forget := engine.holds.hold(engine.ctx, testProject, testOwner)
	forget()

	assert.Empty(t, id)
	assert.Empty(t, engine.holds.snapshot())

	var runCtx dbos.Context

	_, err := engine.track(engine.ctx, ItemInput{}, func(ctx dbos.Context) (ItemResult, error) {
		runCtx = ctx

		return ItemResult{}, nil
	})
	require.NoError(t, err)

	require.ErrorIs(t, runCtx.Err(), context.Canceled)
}

func TestHolds_FailKeepsTheReasonAndASnapshotDropsTheLostClaim(t *testing.T) {
	t.Parallel()

	var held holds

	held.add("run-1", claimHold{project: testProject, owner: testOwner, renewed: time.Unix(100, 0)})
	held.add("run-3", claimHold{renewed: time.Unix(100, 0)})

	claim, _, _ := held.distrust("run-1", clock{Now: func() time.Time { return time.Unix(100, 0) }})
	require.NoError(t, claim.Err(), "a claim that is trusted has a live context")

	held.fail("run-1", "the tracker said no")
	held.fail("run-2", "no such run")

	require.ErrorIs(t, claim.Err(), context.Canceled, "a loss ends the context of the claim")
	assert.Len(t, held.snapshot(), 1)
	assert.Equal(t, "the tracker said no", held.rows["run-1"].lost)
	assert.NotContains(t, held.rows, "run-2")
}

func TestHolds_DropEndsTheContextOfTheClaimAndForgetsIt(t *testing.T) {
	t.Parallel()

	var held holds

	held.add("run-1", claimHold{renewed: time.Unix(100, 0)})

	claim, _, _ := held.distrust("run-1", clock{Now: func() time.Time { return time.Unix(100, 0) }})

	held.drop("run-1")
	held.drop("run-2")

	require.ErrorIs(t, claim.Err(), context.Canceled, "the end of the run ends the agents of the run")
	assert.Empty(t, held.rows)
}

func TestHolds_DistrustNamesWhyARunHoldsNoTrustedClaim(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_000_000, 0)

	cases := map[string]struct {
		row    claimHold
		find   string
		reason string
		early  bool
	}{
		"no row for the run":     {find: "other-run", reason: "holds no claim on the item", early: true},
		"a lost claim":           {row: claimHold{renewed: now, lost: "the tracker said no"}, reason: "the tracker said no"},
		"no good renewal yet":    {row: claimHold{}, reason: "no renewal of the claim has worked yet", early: true},
		"a renewal at the limit": {row: claimHold{renewed: now.Add(-trustLimit)}, reason: "its lease can end"},
		"a fresh renewal":        {row: claimHold{renewed: now.Add(-trustLimit + time.Second)}},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var held holds

			held.add("run-1", tc.row)

			claim, reason, early := held.distrust(cmp.Or(tc.find, "run-1"), clock{Now: func() time.Time { return now }})

			assert.Contains(t, reason, tc.reason)
			assert.Equal(t, tc.early, early)
			assert.Equal(t, tc.reason == "", claim != nil, "a trusted claim comes with its context, and nothing else does")
		})
	}
}

func TestHolds_ASnapshotSkipsAClaimThatHasNoFirstTrustYet(t *testing.T) {
	t.Parallel()

	var held holds

	held.mu.Lock()
	held.rows = map[string]claimHold{"run-1": {project: testProject, owner: testOwner}}
	held.mu.Unlock()

	assert.Empty(t, held.snapshot())
}

func TestHolds_RenewMovesTheStartOfTheTrustOfTheNamedRunOnlyAndSwapsItsTimer(t *testing.T) {
	t.Parallel()

	var held holds

	start := time.Unix(100, 0)
	stopped := 0

	held.mu.Lock()
	held.rows = map[string]claimHold{"run-1": {project: testProject, owner: testOwner, failure: "old", stop: func() { stopped++ }}, "run-3": {lost: "gone"}}
	held.mu.Unlock()

	assert.True(t, held.renew("run-1", start, func() {}))
	assert.False(t, held.renew("run-2", start, func() {}))
	assert.False(t, held.renew("run-3", start, func() {}), "a lost claim is not renewed")

	assert.Equal(t, start, held.rows["run-1"].renewed)
	assert.Empty(t, held.rows["run-1"].failure, "a good renewal forgets the earlier failure")
	assert.NotContains(t, held.rows, "run-2")
	assert.Equal(t, 1, stopped, "the timer of the earlier renewal stops")
}

func TestHolds_ExpiredNamesTheClaimOnlyForTheStampThatArmedTheTimer(t *testing.T) {
	t.Parallel()

	var held holds

	start := time.Unix(100, 0)

	held.mu.Lock()
	held.rows = map[string]claimHold{"run-1": {renewed: start}, "run-2": {renewed: start, lost: "gone"}}
	held.mu.Unlock()

	_, expired := held.expired("run-1", start)
	assert.True(t, expired)

	_, expired = held.expired("run-1", start.Add(time.Second))
	assert.False(t, expired, "a later renewal makes the timer void")

	_, expired = held.expired("run-2", start)
	assert.False(t, expired)

	_, expired = held.expired("run-3", start)
	assert.False(t, expired)
}

func TestHolds_NoteKeepsTheFailureOfTheNamedRunOnly(t *testing.T) {
	t.Parallel()

	var held holds

	held.mu.Lock()
	held.rows = map[string]claimHold{"run-1": {}}
	held.mu.Unlock()

	held.note("run-1", "down")
	held.note("run-2", "down")

	assert.Equal(t, "down", held.rows["run-1"].failure)
	assert.NotContains(t, held.rows, "run-2")
}

func TestClock_AgeCountsTheLargerOfTheMonotonicAndTheWallTime(t *testing.T) {
	t.Parallel()

	start := time.Now()

	cases := map[string]struct {
		monotonic time.Duration
		wall      time.Duration
		want      time.Duration
	}{
		"a sleep moves the wall reading and not the monotonic reading": {wall: time.Minute, want: time.Minute},
		"a wall clock that stands still while the machine runs":        {monotonic: time.Minute, want: time.Minute},
		"a wall clock set back while the machine runs":                 {monotonic: time.Minute, wall: -time.Hour, want: time.Minute},
		"both readings move": {monotonic: time.Minute, wall: 2 * time.Minute, want: 2 * time.Minute},
		"no time passes":     {},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			clk := clock{
				Now:  func() time.Time { return start.Add(tc.monotonic) },
				Wall: func() time.Time { return start.Round(0).Add(tc.wall) },
			}

			assert.Equal(t, tc.want, clk.age(start))
		})
	}
}

func TestClock_TheWallReadingOfAClockWithoutOneIsTheWallPartOfNow(t *testing.T) {
	t.Parallel()

	start := time.Now()
	clk := clock{Now: func() time.Time { return start.Add(time.Minute) }}

	assert.Zero(t, clk.wall().Round(0).Compare(start.Round(0).Add(time.Minute)))
	assert.Equal(t, time.Minute, clk.age(start))
}

func TestClock_TheClockOfTheProcessCarriesAMonotonicReading(t *testing.T) {
	t.Parallel()

	assert.Contains(t, clock{}.now().String(), "m=", "a clock without the monotonic reading overstates the trust")
}

func TestClock_AfterStartsTheFunctionAndItsStopFunctionCancelsIt(t *testing.T) {
	t.Parallel()

	fired := make(chan struct{})

	clock{}.after(time.Millisecond, func() { close(fired) })
	<-fired

	stop := clock{}.after(time.Hour, func() { t.Error("a stopped timer fires") })
	stop()
}

func TestCancels_StopCancelsTheRunAndKeepsItListed(t *testing.T) {
	t.Parallel()

	var runs cancels

	stopped := 0

	runs.add("run-1", openRun{id: "run-1", cancel: func() { stopped++ }, in: ItemInput{Item: testItem}})

	runs.stop("run-1")
	runs.stop("run-2")

	assert.Equal(t, 1, stopped, "this process does not execute run-2")
	assert.Equal(t, []string{"run-1"}, runs.ids())
}

func TestTick_TheRealTickerRenewsAHeldClaim(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	engine, err := NewEngine(t.Context(), harness.opts, Deps{Beats: harness.beats})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	engine.holds.mu.Lock()
	engine.holds.rows = map[string]claimHold{"run-1": {project: testProject, owner: testOwner + testItem, renewed: time.Now()}}
	engine.holds.mu.Unlock()

	stop := make(chan struct{})
	ended := make(chan struct{})

	go func() {
		engine.tick(tickEvery, stop)
		close(ended)
	}()

	eventually(t, func() bool { return len(harness.beats.seen()) > 0 })
	close(stop)
	<-ended

	assert.Equal(t, testProject+"/"+testOwner+testItem, harness.beats.seen()[0])
}

func TestEngine_AStoreThatIsShutDownRefusesEachRead(t *testing.T) {
	t.Parallel()

	engine, err := NewEngine(t.Context(), newHarness(t).opts, Deps{})
	require.NoError(t, err)
	require.NoError(t, engine.Shutdown())

	engine.cancels.add("item-12-r1", openRun{id: "item-12-r1", ended: make(chan struct{})})
	engine.cancelCancelled(nil)

	require.ErrorContains(t, engine.deleteOldRuns(), "list the finished runs")
	engine.retain()

	_, err = engine.Run(testItem, testProject)
	require.ErrorContains(t, err, "list the runs of item 12")

	// Outside a workflow the run id is empty: hold a trusted claim under it, so the guard on the phase start passes.
	engine.holds.add("", claimHold{renewed: time.Now()})

	err = engine.runPhase(engine.ctx, PhaseInput{Item: testItem, Project: testProject, Step: model.StepPlan, Login: testLogin})
	require.ErrorContains(t, err, "queue the plan of item 12")

	_, err = engine.runOpen(testItem)
	require.ErrorContains(t, err, "read run 12")

	_, err = engine.awaitClaim(t.Context(), PhaseInput{Run: "run-gone", Item: testItem})
	require.ErrorContains(t, err, "read run run-gone")
}

func TestClock_TheZeroValueAddsNoResultOption(t *testing.T) {
	t.Parallel()

	assert.Nil(t, clock{}.resultOptions())
	assert.Len(t, clock{Poll: tickEvery}.resultOptions(), 1)
}

func TestLaunch_ReportsAnAgentProcessFileItCannotRead(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	engine, err := NewEngine(t.Context(), harness.opts, harness.deps())
	require.NoError(t, err)
	require.NoError(t, os.Mkdir(groupsFile(harness.opts.StateDir), 0o700))

	// The folder in the place of the file also stops the shutdown from writing it.
	t.Cleanup(func() { require.ErrorContains(t, engine.Shutdown(), "write the agent process file") })

	require.ErrorContains(t, engine.Launch(), "read the agent process file")
}

func TestLaunch_KillsTheAgentGroupAnEarlierWorkerLeft(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	engine := idleEngine(t, harness)
	pid := startSleeper(t)
	started, err := proc.StartTime(t.Context(), pid)
	require.NoError(t, err)
	writeRow(t, &groups{path: groupsFile(harness.opts.StateDir)}, pid, started)

	require.NoError(t, engine.Launch())

	requireDies(t, pid)
}

// enqueueRun puts a run of the item workflow in the store under a queue that nobody listens on.
func enqueueRun[P any](t *testing.T, client *Client, id string, input P, version string) {
	t.Helper()

	_, err := dbos.Enqueue[ItemResult](client.flow, nobodyQueue, itemName, input,
		dbos.WithEnqueueWorkflowID(id), dbos.WithEnqueueApplicationVersion(version))
	require.NoError(t, err)
}

func TestRun_RefusesAnOpenRunWhoseStoredInputIsNotAnItemInput(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	engine := idleEngine(t, harness)
	enqueueRun(t, harness.client(t, testVersion), runID(badInputItem, 1), "not an item input", testVersion)

	_, err := engine.Run(badInputItem, testProject)

	require.ErrorContains(t, err, "read the input of run "+runID(badInputItem, 1))
}

func TestRun_RefusesAnOpenRunOfAnotherVersionAndNamesIt(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.opts.Version = otherVersion
	engine := idleEngine(t, harness)
	enqueueRun(t, harness.client(t, testVersion), runID(foreignItem, 1), ItemInput{Item: foreignItem, Project: testProject}, staleVersion)

	_, err := engine.Run(foreignItem, testProject)

	require.ErrorIs(t, err, errStranded)
	require.ErrorContains(t, err, staleVersion)
}

func TestRun_AFailedReviewPhaseEndsTheRunAndNamesTheStep(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	engine := launchedWith(t, harness, func(deps *Deps) {
		deps.Agents = agentFunc(func(ctx context.Context, request AgentRequest) (agent.Result, error) {
			if request.Step == model.StepReview {
				return agent.Result{Outcome: agent.OutcomeExited, ExitCode: 3, Note: "review broke"}, nil
			}

			return harness.agents.Run(ctx, request)
		})
	})

	_, err := engine.Run(testItem, testProject)

	require.ErrorContains(t, err, "the review of item 12 ended as exited (exit 3): review broke")
}

// freshFails picks the login as fakeLogins does and fails when asked for a fresh one.
type freshFails struct{ fakeLogins }

func (freshFails) PickFresh(context.Context, string, string) (string, error) {
	return "", errFake
}

func TestRun_AFreshLoginThatCannotBePickedEndsTheRunAtFixRoundFour(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.verdicts.reviews = changes(freshRound)
	engine := launchedWith(t, harness, func(deps *Deps) { deps.Logins = freshFails{fakeLogins{login: testLogin}} })

	_, err := engine.Run(testItem, testProject)

	require.ErrorContains(t, err, "pick the fresh runner of item 12")
	require.ErrorIs(t, err, errFake)
}

func TestWaitOwner_ReportsAQuestionItCannotShow(t *testing.T) {
	t.Parallel()

	engine := idleEngine(t, newHarness(t))

	_, err := engine.waitOwner(engine.ctx, testItem, "which way?")

	require.ErrorContains(t, err, "item 12 cannot show its question")
}

func TestRedactAttr_RedactsEveryKindOfValue(t *testing.T) {
	t.Parallel()

	secret := "sk-ant-" + "abcdefghijklmnop"
	redact := agent.Redactor()

	tests := []struct {
		name string
		attr slog.Attr
		want string
	}{
		{"text", slog.String("k", "token "+secret), "k=token " + redact(secret)},
		{"error", slog.Any("k", errors.New("failed with "+secret)), "k=failed with " + redact(secret)},
		{"group", slog.Group("g", slog.String("k", secret)), "g.k=" + redact(secret)},
		{"number", slog.Int("k", 7), "k=7"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got := redactAttr(redact, test.attr)

			assert.Equal(t, test.attr.Key, got.Key)
			assert.NotContains(t, got.String(), secret)
			assert.Equal(t, test.want, flatten(got))
		})
	}
}

// flatten prints an attribute the way a text log does: a group prefixes the key of each member.
func flatten(attr slog.Attr) string {
	if attr.Value.Kind() != slog.KindGroup {
		return attr.Key + "=" + attr.Value.String()
	}

	member := attr.Value.Group()[0]

	return attr.Key + "." + flatten(member)
}

// stuckQuestion sets a question that is not text, then waits for the owner.
func stuckQuestion(ctx dbos.Context, _ string) (string, error) {
	if err := dbos.SetEvent(ctx, questionKey, 42); err != nil {
		return "", err
	}

	return dbos.Recv[string](ctx, ownerTopic, time.Minute)
}

func TestClient_StatusReportsAQuestionThatIsNotText(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	state, err := openStore(harness.opts)
	require.NoError(t, err)

	t.Cleanup(func() { _ = state.log.Close() })

	flowCtx, err := dbos.NewContext(t.Context(), dbos.Config{
		AppName: appName, DatabaseURL: state.url, ApplicationVersion: testVersion, ExecutorID: executorName,
	})
	require.NoError(t, err)

	dbos.RegisterWorkflow(flowCtx, stuckQuestion, dbos.WithWorkflowName(itemName))
	require.NoError(t, dbos.Launch(flowCtx))

	_, err = dbos.RunWorkflow(flowCtx, stuckQuestion, stuckItemName, dbos.WithWorkflowID(stuckItemID))
	require.NoError(t, err)

	client := harness.client(t, testVersion)

	eventually(t, func() bool {
		_, err := client.Status()

		return err != nil
	})

	_, err = client.Status()
	require.ErrorContains(t, err, "read the question of "+stuckItemID)
	require.ErrorContains(t, client.Answer(stuckItemName, "go on"), "read the question of "+stuckItemID)
	require.NoError(t, dbos.CancelWorkflow(flowCtx, stuckItemID))
	require.NoError(t, dbos.Shutdown(flowCtx, shutdownTimeout))
	t.Log("the run is canceled and the context is shut down")
}

// stuckMerge sets a merge state that is not text, then waits for the owner.
func stuckMerge(ctx dbos.Context, _ string) (string, error) {
	if err := dbos.SetEvent(ctx, mergeKey, 42); err != nil {
		return "", err
	}

	return dbos.Recv[string](ctx, ownerTopic, time.Minute)
}

func TestClient_StatusReportsAMergeStateThatIsNotText(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	state, err := openStore(harness.opts)
	require.NoError(t, err)

	t.Cleanup(func() { _ = state.log.Close() })

	flowCtx, err := dbos.NewContext(t.Context(), dbos.Config{
		AppName: appName, DatabaseURL: state.url, ApplicationVersion: testVersion, ExecutorID: executorName,
	})
	require.NoError(t, err)

	dbos.RegisterWorkflow(flowCtx, stuckMerge, dbos.WithWorkflowName(itemName))
	require.NoError(t, dbos.Launch(flowCtx))

	_, err = dbos.RunWorkflow(flowCtx, stuckMerge, stuckItemName, dbos.WithWorkflowID(stuckItemID))
	require.NoError(t, err)

	client := harness.client(t, testVersion)

	eventually(t, func() bool {
		_, err := client.Status()

		return err != nil
	})

	_, err = client.Status()
	require.ErrorContains(t, err, "read the merge state of "+stuckItemID)
	require.NoError(t, dbos.CancelWorkflow(flowCtx, stuckItemID))
	require.NoError(t, dbos.Shutdown(flowCtx, shutdownTimeout))
}

func TestClient_CommandsReportAStoreThatIsGone(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	client, err := OpenClient(t.Context(), harness.opts, ReleaseDeps{})
	require.NoError(t, err)
	require.NoError(t, client.Close())

	require.ErrorContains(t, client.Cancel(testItem), "list the runs of item 12")
}
