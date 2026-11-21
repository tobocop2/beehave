package flow

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/proc"
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

	forget := engine.holds.hold(&attempt{}, testProject, testOwner)
	forget()

	assert.Empty(t, engine.holds.rows)

	var runCtx dbos.Context

	_, err := engine.track(engine.ctx, ItemInput{}, func(ctx dbos.Context, a *attempt) (ItemResult, error) {
		runCtx = ctx

		assert.Empty(t, a.id, "a call outside a workflow has no run id")

		return ItemResult{}, nil
	})
	require.NoError(t, err)

	require.ErrorIs(t, runCtx.Err(), context.Canceled)
	assert.Empty(t, engine.cancels.attempts(), "a call outside a workflow is not listed")
}

func TestHolds_TheEndOfAnEarlierAttemptKeepsTheClaimOfTheResumedRun(t *testing.T) {
	t.Parallel()

	var held holds

	earlier, resumed := &attempt{id: "run-1"}, &attempt{id: "run-1"}

	forgetEarlier := held.hold(earlier, testProject, testOwner)
	earlierClaim := held.rows[earlier].ctx
	forgetResumed := held.hold(resumed, testProject, testOwner)

	require.ErrorIs(t, earlierClaim.Err(), context.Canceled, "the claim of the resumed run ends the claim of the earlier attempt")
	_, found := held.rows[earlier]
	assert.False(t, found, "one run id holds one claim row")

	forgetEarlier()
	_, found = held.rows[resumed]
	assert.True(t, found, "the earlier attempt does not forget the claim that the resumed run holds")
	require.NoError(t, held.rows[resumed].ctx.Err(), "the claim of the resumed run stays live")

	forgetResumed()
	_, found = held.rows[resumed]
	assert.False(t, found, "the resumed run forgets its own claim")

	forgetResumed()
	assert.Empty(t, held.rows, "a second forget finds nothing")
}

func TestHolds_FailKeepsTheReasonAndASnapshotDropsTheLostClaim(t *testing.T) {
	t.Parallel()

	var held holds

	first, third := &attempt{id: "run-1"}, &attempt{id: "run-3"}
	held.add(first, claimHold{project: testProject, owner: testOwner, renewed: time.Unix(100, 0)})
	held.add(third, claimHold{renewed: time.Unix(100, 0)})

	_, claim, _, _ := held.distrust("run-1", clock{Now: func() time.Time { return time.Unix(100, 0) }})
	require.NoError(t, claim.Err(), "a claim that is trusted has a live context")

	held.fail(&attempt{id: "run-1"}, "an earlier attempt")
	require.NoError(t, claim.Err(), "an attempt that holds no row leaves the claim of the run alone")
	assert.Empty(t, held.rows[first].lost)

	held.fail(first, "the tracker said no")

	require.ErrorIs(t, claim.Err(), context.Canceled, "a loss ends the context of the claim")
	assert.Len(t, held.snapshot(), 1)
	assert.Equal(t, "the tracker said no", held.rows[first].lost)
	assert.Len(t, held.rows, 2)
}

func TestHolds_DropEndsTheContextOfTheClaimAndForgetsIt(t *testing.T) {
	t.Parallel()

	var held holds

	first := &attempt{id: "run-1"}
	held.add(first, claimHold{renewed: time.Unix(100, 0)})

	_, claim, _, _ := held.distrust("run-1", clock{Now: func() time.Time { return time.Unix(100, 0) }})

	held.dropOwn(first)
	held.dropOwn(&attempt{id: "run-2"})

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

			held.add(&attempt{id: "run-1"}, tc.row)

			holder, claim, reason, early := held.distrust(cmp.Or(tc.find, "run-1"), clock{Now: func() time.Time { return now }})

			assert.Contains(t, reason, tc.reason)
			assert.Equal(t, tc.early, early)
			assert.Equal(t, tc.reason == "", claim != nil, "a trusted claim comes with its context, and nothing else does")
			assert.Equal(t, tc.reason == "", holder != nil, "a trusted claim comes with the attempt that holds it, and nothing else does")
		})
	}
}

func TestHolds_ASnapshotSkipsAClaimThatHasNoFirstTrustYet(t *testing.T) {
	t.Parallel()

	var held holds

	held.mu.Lock()
	held.rows = map[*attempt]claimHold{{id: "run-1"}: {project: testProject, owner: testOwner}}
	held.mu.Unlock()

	assert.Empty(t, held.snapshot())
}

func TestHolds_RenewMovesTheStartOfTheTrustOfTheNamedAttemptOnlyAndSwapsItsTimer(t *testing.T) {
	t.Parallel()

	var held holds

	start := time.Unix(100, 0)
	stopped := 0

	first, lost := &attempt{id: "run-1"}, &attempt{id: "run-3"}

	held.mu.Lock()
	held.rows = map[*attempt]claimHold{first: {project: testProject, owner: testOwner, failure: "old", stop: func() { stopped++ }}, lost: {lost: "gone"}}
	held.mu.Unlock()

	assert.True(t, held.renew(first, start, func() {}))
	assert.False(t, held.renew(&attempt{id: "run-1"}, start, func() {}), "another attempt of the run holds no row")
	assert.False(t, held.renew(lost, start, func() {}), "a lost claim is not renewed")

	assert.Equal(t, start, held.rows[first].renewed)
	assert.Empty(t, held.rows[first].failure, "a good renewal forgets the earlier failure")
	assert.Len(t, held.rows, 2)
	assert.Equal(t, 1, stopped, "the timer of the earlier renewal stops")
}

func TestHolds_ExpiredNamesTheClaimOnlyForTheStampThatArmedTheTimer(t *testing.T) {
	t.Parallel()

	var held holds

	start := time.Unix(100, 0)

	first, lost := &attempt{id: "run-1"}, &attempt{id: "run-2"}

	held.mu.Lock()
	held.rows = map[*attempt]claimHold{first: {renewed: start}, lost: {renewed: start, lost: "gone"}}
	held.mu.Unlock()

	_, expired := held.expired(first, start)
	assert.True(t, expired)

	_, expired = held.expired(first, start.Add(time.Second))
	assert.False(t, expired, "a later renewal makes the timer void")

	_, expired = held.expired(lost, start)
	assert.False(t, expired)

	_, expired = held.expired(&attempt{id: "run-1"}, start)
	assert.False(t, expired, "another attempt of the run holds no row")
}

func TestHolds_NoteKeepsTheFailureOfTheNamedAttemptOnly(t *testing.T) {
	t.Parallel()

	var held holds

	first := &attempt{id: "run-1"}

	held.mu.Lock()
	held.rows = map[*attempt]claimHold{first: {}}
	held.mu.Unlock()

	held.note(first, "down")
	held.note(&attempt{id: "run-1"}, "late")

	assert.Equal(t, "down", held.rows[first].failure, "another attempt of the run writes no failure into the row")
	assert.Len(t, held.rows, 1)
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
	receive(t, fired)

	stop := clock{}.after(time.Hour, func() { t.Error("a stopped timer fires") })
	stop()
}

func TestTick_TheRealTickerRenewsAHeldClaim(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)

	engine, err := NewEngine(t.Context(), harness.opts, Deps{Beats: harness.beats})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	holdClaim(engine, "run-1", claimHold{project: testProject, owner: testOwner + testItem, renewed: time.Now()})

	stop := make(chan struct{})
	ended := make(chan struct{})

	go func() {
		engine.tick(tickEvery, stop)
		close(ended)
	}()

	waitOn(t, condition(func() bool { return len(harness.beats.seen()) > 0 }))
	close(stop)
	receive(t, ended)

	assert.Equal(t, testProject+"/"+testOwner+testItem, harness.beats.seen()[0])
}

func TestShutdown_StopsTheTrustTimerOfAClaimThatIsStillHeld(t *testing.T) {
	t.Parallel()

	var (
		stopped atomic.Bool
		fire    func()
	)

	harness := newHarness(t)
	deps := harness.deps()
	deps.Clock.After = func(_ time.Duration, f func()) func() {
		fire = f

		return func() { stopped.Store(true) }
	}

	engine, err := NewEngine(t.Context(), harness.opts, deps)
	require.NoError(t, err)

	engine.trust(holdClaim(engine, "run-1", claimHold{renewed: time.Now()}), time.Now())

	require.NoError(t, engine.Shutdown())

	assert.True(t, stopped.Load(), "a timer must not outlive the engine")
	assert.NotPanics(t, fire, "a timer that fires after the shutdown finds no claim")
}

func TestEngine_AStoreThatIsShutDownRefusesEachRead(t *testing.T) {
	t.Parallel()

	engine, err := NewEngine(t.Context(), newHarness(t).opts, Deps{})
	require.NoError(t, err)
	require.NoError(t, engine.Shutdown())

	engine.cancels.add(&attempt{id: "item-12-r1", ended: make(chan struct{})})
	engine.cancelCancelled(nil)

	require.ErrorContains(t, engine.deleteOldRuns(), "list the finished runs")
	engine.retain()

	_, err = engine.handleFor(ItemInput{Item: testItem, Project: testProject})
	require.ErrorContains(t, err, "list the runs of item 12")

	// Outside a workflow the run id is empty: hold a trusted claim under it, so the guard on the phase start passes.
	holdClaim(engine, "", claimHold{renewed: time.Now()})

	_, err = engine.queuePhase(engine.ctx, PhaseInput{Item: testItem, Project: testProject, Login: testLogin})
	require.ErrorContains(t, err, "queue the agent step of item 12")

	_, err = engine.runOpen(testItem)
	require.ErrorContains(t, err, "read run 12")

	_, _, err = engine.awaitClaim(t.Context(), PhaseInput{Run: "run-gone", Item: testItem})
	require.ErrorContains(t, err, "read run run-gone")
}

// stepsOfOneFail is a store whose list of the steps of one workflow fails. Each other read goes to the store.
type stepsOfOneFail struct {
	dbos.Context

	id string
}

func (s *stepsOfOneFail) GetWorkflowSteps(_ dbos.Client, id string, opts ...dbos.GetWorkflowStepsOption) ([]dbos.StepInfo, error) {
	if id == s.id {
		return nil, errors.New("the store failed")
	}

	return dbos.GetWorkflowSteps(s.Context, id, opts...)
}

func TestPhase_AListOfEarlierStartsThatCannotBeReadFailsThePhaseAfterItsLookAtTheCaps(t *testing.T) {
	t.Parallel()

	const phaseID = "item-12-r1-phase"

	var store dbos.Context

	// No cap is set, so the look reads nothing. The list of the earlier starts of this one phase fails.
	engine := newHarness(t).engineWith(t, func(e *Engine) {
		store = e.ctx
		e.ctx = &stepsOfOneFail{Context: e.ctx, id: phaseID}
		holdClaim(e, runID(testItem, 1), claimHold{renewed: time.Now()})
	})

	handle, err := dbos.RunWorkflow(engine.ctx, engine.phase, PhaseInput{Run: runID(testItem, 1), Item: testItem, Project: testProject, Login: testLogin},
		dbos.WithWorkflowID(phaseID), dbos.WithQueue(engine.queue), dbos.WithQueuePartitionKey(testLogin))
	require.NoError(t, err)

	_, err = handle.GetResult()
	require.ErrorContains(t, err, "read the steps of the agent step")

	steps, err := dbos.GetWorkflowSteps(store, phaseID)
	require.NoError(t, err)

	names := make([]string, 0, len(steps))
	for _, step := range steps {
		names = append(names, step.StepName)
	}

	assert.Equal(t, []string{capLookStepName}, names, "the look ran as a step of the phase, and no start mark followed")
}

func TestMarkStarts_ReportsAStartThatIsNotInAWorkflow(t *testing.T) {
	t.Parallel()

	engine, err := NewEngine(t.Context(), newHarness(t).opts, Deps{})
	require.NoError(t, err)

	t.Cleanup(func() { assert.NoError(t, engine.Shutdown()) })

	_, _, err = engine.markStarts(engine.ctx, PhaseInput{Item: testItem})
	require.Error(t, err, "outside a workflow the start mark is not a step")
}

func TestPhase_ARenewalThatTheStoreCannotReadIsLoggedAndGivesAnUnknownRunTime(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	loseAgent(t, harness, 5*time.Minute)
	harness.beats.failRenewed(errFake)

	_, err := harness.engine(t).Run(testItem, testProject)
	require.NoError(t, err)

	requests := harness.agents.seen()
	require.Len(t, requests, 2)
	assert.Contains(t, requests[1].Prompt, "ran before for an unknown time and was lost")

	log, err := os.ReadFile(filepath.Join(harness.opts.StateDir, logFileName))
	require.NoError(t, err)
	assert.Contains(t, string(log), "cannot read the last renewal of the claim")
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

func TestWaitOwner_ReportsAQuestionItCannotShow(t *testing.T) {
	t.Parallel()

	engine := idleEngine(t, newHarness(t))

	_, err := waiter(engine).waitOwner("which way?")

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

	return awaitInbox(ctx)
}

func TestClient_StatusReportsAQuestionThatIsNotText(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	state, err := openStore(harness.opts)
	require.NoError(t, err)

	t.Cleanup(func() { _ = state.log.Close() })

	flowCtx, err := dbos.NewContext(t.Context(), dbos.Config{
		AppName: appName, DatabaseURL: state.url, ApplicationVersion: applicationVersion(testVersion), ExecutorID: executorName,
	})
	require.NoError(t, err)

	dbos.RegisterWorkflow(flowCtx, stuckQuestion, dbos.WithWorkflowName(itemName))
	require.NoError(t, dbos.Launch(flowCtx))

	_, err = dbos.RunWorkflow(flowCtx, stuckQuestion, stuckItemName, dbos.WithWorkflowID(stuckItemID))
	require.NoError(t, err)

	client := harness.client(t, testVersion)

	waitOn(t, condition(func() bool {
		_, err := client.Status()

		return err != nil
	}))

	_, err = client.Status()
	require.ErrorContains(t, err, "read the question of "+stuckItemID)
	require.ErrorContains(t, client.Reply(stuckItemName, Reply{Text: "go on"}), "read the question of "+stuckItemID)
	require.NoError(t, dbos.CancelWorkflow(flowCtx, stuckItemID))
	require.NoError(t, dbos.Shutdown(flowCtx, shutdownTimeout))
	t.Log("the run is canceled and the context is shut down")
}

// stuckEvent is a run that sets the event key to a number, which is not the shape that status reads, then waits for the owner.
func stuckEvent(key string) func(dbos.Context, string) (string, error) {
	return func(ctx dbos.Context, _ string) (string, error) {
		if err := dbos.SetEvent(ctx, key, 42); err != nil {
			return "", err
		}

		return awaitInbox(ctx)
	}
}

func TestResultOf_ARunWithNoOutputHasAnEmptyResult(t *testing.T) {
	t.Parallel()

	got, err := resultOf(dbos.WorkflowStatus{ID: stuckItemID})

	require.NoError(t, err)
	assert.Equal(t, ItemResult{}, got)
}

func TestClient_StatusReportsAnEventThatIsNotInItsShape(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		key  string
		want string
	}{
		{"a_merge_state_that_is_not_text", mergeKey, "read the merge state of " + stuckItemID},
		{"a_lost_job_that_is_not_an_object", lostJobKey, "read the lost gate job of " + stuckItemID},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			harness := newHarness(t)
			state, err := openStore(harness.opts)
			require.NoError(t, err)

			t.Cleanup(func() { _ = state.log.Close() })

			flowCtx, err := dbos.NewContext(t.Context(), dbos.Config{
				AppName: appName, DatabaseURL: state.url, ApplicationVersion: applicationVersion(testVersion), ExecutorID: executorName,
			})
			require.NoError(t, err)

			stuck := stuckEvent(tc.key)
			dbos.RegisterWorkflow(flowCtx, stuck, dbos.WithWorkflowName(itemName))
			require.NoError(t, dbos.Launch(flowCtx))

			_, err = dbos.RunWorkflow(flowCtx, stuck, stuckItemName, dbos.WithWorkflowID(stuckItemID))
			require.NoError(t, err)

			client := harness.client(t, testVersion)

			waitOn(t, condition(func() bool {
				_, err := client.Status()

				return err != nil
			}))

			_, err = client.Status()
			require.ErrorContains(t, err, tc.want)
			require.NoError(t, dbos.CancelWorkflow(flowCtx, stuckItemID))
			require.NoError(t, dbos.Shutdown(flowCtx, shutdownTimeout))
		})
	}
}

func TestClient_CommandsReportAStoreThatIsGone(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	client, err := OpenClient(t.Context(), harness.opts, ReleaseDeps{})
	require.NoError(t, err)
	require.NoError(t, client.Close())

	require.ErrorContains(t, client.Cancel(testItem), "list the runs of item 12")
}
