package flow

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/exectest"
	"github.com/tobocop2/beehave/internal/model"
)

const privateMode = os.FileMode(0o600)

func TestOptions_RefuseEachDBOSVariableTheRuleNames(t *testing.T) {
	t.Parallel()

	names := []string{
		"DBOS__CLOUD", "DBOS__CONDUCTOR_APP_NAME", "DBOS__CONDUCTOR_KEY", "DBOS__CONDUCTOR_URL",
		"DBOS__APPVERSION", "DBOS__VMID", "DBOS__APPID", "DBOS_DOMAIN", "DBOS_SYSTEM_DATABASE_URL",
	}

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			opts := newHarness(t).opts
			opts.Env = map[string]string{name: "x"}

			_, err := NewEngine(t.Context(), opts, Deps{})
			require.ErrorContains(t, err, name)

			_, err = OpenClient(t.Context(), opts, ReleaseDeps{})
			require.ErrorContains(t, err, name)
		})
	}
}

func TestOptions_RefuseUnusableSettingsForTheEngineAndTheClient(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		change func(*Options)
		want   string
	}{
		{"an_empty_version", func(o *Options) { o.Version = "" }, "build version is empty"},
		{"a_relative_state_folder", func(o *Options) { o.StateDir = "state" }, "not an absolute path"},
		{"no_agent_slot", func(o *Options) { o.MaxConcurrent = 0 }, "agents.max_concurrent is 0: set it to 1 or more"},
		{"a_negative_per_login", func(o *Options) { o.PerLogin = -1 }, "agents.per_login is -1: set it to 1 or more"},
		{"a_per_login_above_a_queue_cap", func(o *Options) { o.MaxConcurrent, o.MaxReviewConcurrent, o.PerLogin = 3, 1, 2 }, "agents.per_login is 2: set it to agents.max_concurrent (3) or agents.max_review_concurrent (1), whichever is lower, or less"},
		{"an_unknown_concurrency", func(o *Options) { o.Concurrency = "turbo" }, "agents.concurrency is \"turbo\": set it to \"static\" or \"adaptive\". Set it in runners.star or in BEEHAVE_AGENTS_CONCURRENCY"},
		{"a_negative_review_lookback", func(o *Options) { o.ReviewLookbackDays = -1 }, "agents.review_lookback_days is -1: set it to 1 or more"},
		{"a_negative_review_poll", func(o *Options) { o.ReviewPoll = -time.Minute }, "review poll interval is -1m0s: set it to 0 or more"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			opts := newHarness(t).opts
			test.change(&opts)

			_, err := NewEngine(t.Context(), opts, Deps{})
			require.ErrorContains(t, err, test.want)

			_, err = OpenClient(t.Context(), opts, ReleaseDeps{})
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestNewEngine_AnEngineWithNoFactsReadsNoFactsForEveryProject(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	deps := h.deps()
	deps.Facts = nil

	engine, err := NewEngine(t.Context(), h.opts, deps)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	assert.Empty(t, engine.requiredSteps(testProject), "a gate records no required steps")
	assert.Equal(t, config.DefaultLostJobWait, engine.lostJobWait(testProject), "a lost job waits for the default")
	assert.Equal(t, model.Project{}, (&script{e: engine, in: ItemInput{Project: testProject}}).Project(), "the facts builtin reads an empty project")
}

func TestOpenStore_RefusesAFolderOthersCanUse(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.fresh(t)
	require.NoError(t, os.Mkdir(harness.opts.StateDir, 0o700))
	require.NoError(t, exectest.ChmodFolder(harness.opts.StateDir, 0o777)) // the test needs an open folder to prove the refusal

	_, err := NewEngine(t.Context(), harness.opts, harness.deps())
	require.Error(t, err)
}

func TestOpenStore_ReportsAnUnwritableLogAndAnUnopenableDatabase(t *testing.T) {
	t.Parallel()

	for _, name := range []string{logFileName, databaseFile} {
		t.Run("a_folder_named_"+name, func(t *testing.T) {
			t.Parallel()

			harness := newHarness(t)
			harness.fresh(t)
			require.NoError(t, os.MkdirAll(filepath.Join(harness.opts.StateDir, name), 0o700))

			_, err := NewEngine(t.Context(), harness.opts, harness.deps())
			require.Error(t, err)

			_, err = OpenClient(t.Context(), harness.opts, ReleaseDeps{})
			require.Error(t, err)
		})
	}
}

func TestRun_AFileRunClaimsRunsItsStepsReleasesAndKeepsTheFilesPrivate(t *testing.T) { //nolint:paralleltest // the umask is set for the whole process
	previous := syscall.Umask(0o077) // the composition root sets this umask before it opens the store
	t.Cleanup(func() { syscall.Umask(previous) })

	harness := newHarness(t)
	harness.fresh(t)
	engine := harness.engine(t)

	result, err := engine.Run(testItem, testProject)

	require.NoError(t, err)
	assert.Equal(t, ItemResult{Item: testItem}, result)
	assert.Equal(t, []string{"claim:12", "release:12"}, harness.life.seen())
	require.Len(t, harness.agents.seen(), 1)
	assert.Equal(t, testLogin, harness.agents.seen()[0].Login)
	assert.Contains(t, harness.agents.seen()[0].Prompt, "Work on the item.")

	for _, name := range []string{databaseFile, logFileName} {
		info, statErr := os.Stat(filepath.Join(harness.opts.StateDir, name))
		require.NoError(t, statErr)
		assert.Equal(t, privateMode, info.Mode().Perm(), name)
	}

	for _, suffix := range []string{"-wal", "-shm"} {
		if info, statErr := os.Stat(filepath.Join(harness.opts.StateDir, databaseFile+suffix)); statErr == nil {
			assert.Equal(t, privateMode, info.Mode().Perm(), suffix)
		}
	}
}

func TestRun_StartsAnotherAttemptAfterTheRunEnded(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	engine := harness.engine(t)
	client := harness.client(t, testVersion)

	_, err := engine.Run(testItem, testProject)
	require.NoError(t, err)
	_, err = engine.Run(testItem, testProject)
	require.NoError(t, err)

	infos, err := client.Status()
	require.NoError(t, err)

	var ids []string
	for _, info := range infos {
		ids = append(ids, info.ID)
		assert.Equal(t, runDone, info.State)
	}

	assert.ElementsMatch(t, []string{"item-12-r1", "item-12-r2"}, ids)
}

func TestRun_FailuresNameTheStepAndKeepTheRelease(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		setup func(*harness)
		want  string
		steps []string
	}{
		{"the_login_pick_fails", func(h *harness) { h.logins.err = errFake }, "pick the login of item 12", []string{"claim:12", "release:12"}},
		{"no_login_is_set", func(h *harness) { h.logins.login = "" }, "no agent login for this step", []string{"claim:12", "release:12"}},
		{"the_agent_fails", func(h *harness) { h.agents.err = errFake }, "fake failure", []string{"claim:12", "release:12"}},
		{"the_agent_exits_nonzero", func(h *harness) {
			h.agents.result = func(AgentRequest) agent.Result {
				return agent.Result{Outcome: agent.OutcomeExited, ExitCode: 3, Note: "bad exit"}
			}
		}, "the agent step of item 12 ended as exited (exit 3): bad exit. Fix the cause, then run beehave resume 12", []string{"claim:12", "release:12"}},
		{"the_agent_times_out", func(h *harness) {
			h.agents.result = func(AgentRequest) agent.Result { return agent.Result{Outcome: agent.OutcomeTimedOut, ExitCode: -1} }
		}, "ended as timed-out", []string{"claim:12", "release:12"}},
		{"the_claim_fails", func(h *harness) { h.life.failing["claim"] = true }, "claim of item 12", []string{"claim:12"}},
		{"the_release_fails", func(h *harness) { h.life.failing["release"] = true }, "release of item 12", []string{"claim:12", "release:12"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			harness := newHarness(t)
			test.setup(harness)
			engine := harness.engine(t)

			_, err := engine.Run(testItem, testProject)

			require.ErrorContains(t, err, test.want)
			assert.Equal(t, test.steps, harness.life.seen())
		})
	}
}

func TestRun_RefusesAnItemThatIsNeitherAForgeNumberNorABeadsID(t *testing.T) {
	t.Parallel()

	engine := newHarness(t).engine(t)

	_, err := engine.Run("not an id", testProject)

	require.ErrorIs(t, err, ErrBadItem)
	require.ErrorContains(t, err, `"not an id": use an item id such as 12 or fix-login`)
}

func TestRun_AnOpenRunOfTheItemIsJoinedNotStartedAgain(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.flowIs(t, waitFlow)
	engine := harness.engine(t)
	client := harness.client(t, testVersion)

	first := runInBackground(engine, testItem)
	waitOn(t, ownerAsks(t, harness))

	second := runInBackground(engine, testItem)

	infos, err := client.Status()
	require.NoError(t, err)
	require.Len(t, infos, 1)

	require.NoError(t, client.Reply(testItem, Reply{Text: "go"}))

	firstOutcome, secondOutcome := receive(t, first), receive(t, second)
	require.NoError(t, firstOutcome.err)
	require.NoError(t, secondOutcome.err)
	assert.Equal(t, firstOutcome.result, secondOutcome.result)
}

// enqueuedPhases counts the phases the agents queue holds back.
func enqueuedPhases(t *testing.T, engine *Engine) int {
	t.Helper()

	rows, err := dbos.ListWorkflows(engine.ctx, dbos.WithFilterStatus(dbos.WorkflowStatusEnqueued), dbos.WithFilterQueueName(queueName))
	require.NoError(t, err)

	return len(rows)
}

func TestRun_OneAgentPerLoginAndAtMostMaxConcurrentInAll(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		maxConcurrent int
		logins        map[string]string
		wantLogin     int
		wantTotal     int
	}{
		{"two_items_of_one_login_never_overlap", 2, map[string]string{"1": "a", "2": "a"}, 1, 1},
		{"two_logins_overlap_when_the_limit_allows", 2, map[string]string{"1": "a", "2": "b"}, 1, 2},
		{"the_global_limit_holds_across_logins", 1, map[string]string{"1": "a", "2": "b"}, 1, 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			harness := newHarness(t)
			harness.opts.MaxConcurrent = test.maxConcurrent
			harness.logins = fakeLogins{byItem: test.logins}
			harness.agents.gate = make(chan struct{})
			release := sync.OnceFunc(func() { close(harness.agents.gate) })
			t.Cleanup(release)

			engine := harness.engine(t)

			first, second := runInBackground(engine, "1"), runInBackground(engine, "2")

			// Both items reach their first phase. The store shows whether the second phase waits in the queue or runs.
			waitOn(t, condition(func() bool { return harness.agents.running() == test.wantTotal }))
			waitOn(t, condition(func() bool { return enqueuedPhases(t, engine) == 2-test.wantTotal }))
			release()

			require.NoError(t, receive(t, first).err)
			require.NoError(t, receive(t, second).err)
			assert.Equal(t, test.wantLogin, harness.agents.maxLogin)
			assert.Equal(t, test.wantTotal, harness.agents.maxTotal)
		})
	}
}

func TestRun_AnOtherVersionStrandsTheOpenRunAndEveryCommandSaysSo(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.flowIs(t, waitFlow)
	engine := harness.engine(t)
	same, other := harness.client(t, testVersion), harness.client(t, "v2")

	done := runInBackground(engine, testItem)
	waitOn(t, ownerAsks(t, harness))

	infos, err := other.Status()
	require.NoError(t, err)
	require.Len(t, infos, 1)
	assert.Equal(t, RunInfo{ID: "item-12-r1", Item: testItem, Project: testProject, State: runRunning, Version: testVersion, Stranded: true, Question: "What now?"}, infos[0])

	infos, err = same.Status()
	require.NoError(t, err)
	assert.False(t, infos[0].Stranded)

	require.ErrorIs(t, other.Reply(testItem, Reply{Text: "go"}), errStranded)
	require.ErrorIs(t, other.Cancel(testItem), errStranded)
	require.ErrorContains(t, other.Reply(testItem, Reply{Text: "go"}), "Start version v1 to finish it")

	require.NoError(t, same.Reply(testItem, Reply{Text: "go"}))
	require.NoError(t, receive(t, done).err)
}

func TestEngine_PinsTheApplicationVersionToTheBuildVersion(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.opts.Version = "v1.2.3-pinned"
	engine := harness.engine(t)

	_, err := engine.Run(testItem, testProject)
	require.NoError(t, err)

	assert.Equal(t, "v1.2.3-pinned+core20.agent15.tracker4.forge12.land16.gate8.host1.round7", dbos.GetApplicationVersion(engine.ctx), "the build version and one version for each step module")

	infos, err := harness.client(t, "v1.2.3-pinned").Status()
	require.NoError(t, err)
	require.Len(t, infos, 1)
	assert.Equal(t, "v1.2.3-pinned", infos[0].Version)
}

func TestEngine_ARunAfterTheShutdownIsRefusedByTheEngine(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	engine := harness.engine(t)
	require.NoError(t, engine.Shutdown())

	_, err := engine.Run(testItem, testProject)

	require.ErrorIs(t, err, errEngineShutDown)
	require.ErrorContains(t, err, "item 12 was not started: run beehave run 12 again")
	assert.Empty(t, rowsOf(t, harness.client(t, testVersion), testItem), "a refused run stores no row")
}

func TestEngine_TheShutdownWaitsForAStartInFlight(t *testing.T) {
	t.Parallel()

	engine := newHarness(t).engine(t)

	// Hold the start as a run does between its store read and the hand-off of the run to the library.
	engine.starts.RLock()

	// The cleanup releases the start before the cleanup of the engine shuts it down, so a failed check does not hang the test.
	release := sync.OnceFunc(engine.starts.RUnlock)
	t.Cleanup(release)

	down := make(chan error, 1)

	go func() { down <- engine.Shutdown() }()

	// A new reader fails only while a writer waits for the lock: the shutdown has begun and waits for the start.
	// A stopped library also ends the poll, so a shutdown that does not wait fails the check below at once.
	waitOn(t, condition(func() bool {
		if engine.ctx.Err() != nil {
			return true
		}

		if engine.starts.TryRLock() {
			engine.starts.RUnlock()

			return false
		}

		return true
	}))

	whileHeld := engine.ctx.Err()

	release()

	require.NoError(t, whileHeld, "the library is not stopped while a start is in flight")
	require.NoError(t, receive(t, down))
	assert.True(t, engine.stopped)
}

func TestEngine_TheShutdownWaitsForACallOfRunThatReadsTheStore(t *testing.T) {
	t.Parallel()

	engine := newHarness(t).engine(t)

	// Count a call as run does between its entry and its return.
	require.NoError(t, engine.enter(testItem))

	release := sync.OnceFunc(engine.callers.Done)
	t.Cleanup(release)

	down := make(chan error, 1)

	go func() { down <- engine.Shutdown() }()

	waitOn(t, condition(func() bool { return engine.ctx.Err() != nil }))
	staysTrue(t, func() bool { return len(down) == 0 }, "the shutdown returned while a call of run was in flight")

	release()

	require.NoError(t, receive(t, down))
}

func TestEngine_ACountedCallThatReachesTheStartAfterTheShutdownBeganIsRefused(t *testing.T) {
	t.Parallel()

	engine := newHarness(t).engine(t)

	// A call of run that passed enter before the shutdown began reaches its start after the shutdown began.
	require.NoError(t, engine.enter(testItem))

	release := sync.OnceFunc(engine.callers.Done)
	t.Cleanup(release)

	down := make(chan error, 1)

	go func() { down <- engine.Shutdown() }()

	// The library stops after the shutdown refused the starts.
	waitOn(t, condition(func() bool { return engine.ctx.Err() != nil }))

	handle, err := engine.joinOrStart(ItemInput{Item: testItem})

	release()

	require.ErrorIs(t, err, errEngineShutDown)
	assert.Nil(t, handle)
	require.NoError(t, receive(t, down))
}

func TestEngine_ACallOfRunThatWaitsForItsItemIsCountedUntilItReturns(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.flowIs(t, waitFlow)

	engine := h.engine(t)
	done := runInBackground(engine, testItem)

	waitOn(t, ownerAsks(t, h))

	counted := make(chan struct{})

	go func() {
		engine.callers.Wait()
		close(counted)
	}()

	staysTrue(t, func() bool {
		select {
		case <-counted:
			return false
		default:
			return true
		}
	}, "the call of run waits for its item and is not counted")

	require.NoError(t, engine.Shutdown())
	receive(t, counted)

	// This test proves the count: the call ends when the library stops, before the shutdown returns. TheShutdownWaitsForACallOfRunThatReadsTheStore proves the wait.
	assert.Len(t, done, 1, "the call of run is counted until it returns, and it returned before the shutdown did")
}

func TestEngine_ARunAfterTheShutdownIsRefusedAndCountsNoCall(t *testing.T) {
	t.Parallel()

	engine := newHarness(t).engine(t)
	require.NoError(t, engine.Shutdown())

	require.ErrorIs(t, engine.enter(testItem), errEngineShutDown)

	counted := make(chan struct{})

	go func() {
		engine.callers.Wait()
		close(counted)
	}()

	receive(t, counted)
}

func TestEngine_RunsAsTheLocalExecutor(t *testing.T) {
	t.Parallel()

	engine := newHarness(t).engine(t)

	assert.Equal(t, executorName, dbos.GetExecutorID(engine.ctx))
	assert.Equal(t, queueName, engine.queue.GetName())
}

func TestLaunch_ReportsASecondLaunch(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	engine := harness.engine(t)

	require.ErrorContains(t, engine.Launch(), "start the workflow engine")
}

func TestRegister_ReportsAQueueThatTheStoreRefuses(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	engine, err := NewEngine(t.Context(), harness.opts, harness.deps())
	require.NoError(t, err)

	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	var into dbos.Queue

	require.ErrorContains(t, engine.registerQueue("agents-again", -1, &into), "register the agents-again queue")
}

// stoppedHandle is a run handle whose wait returns the stop cause of the engine, as the library does when the engine stops in the instant the run completes.
type stoppedHandle struct {
	dbos.WorkflowHandle[ItemResult]
}

func (stoppedHandle) GetResult(...dbos.GetResultOption) (ItemResult, error) {
	return ItemResult{}, errEngineStopped
}

var errEngineStopped = errors.New("the engine stopped")

func TestResult_ARunThatCompletedInTheInstantTheEngineStoppedReportsItsResult(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	engine := harness.engine(t)
	file := writeFlow(t, bodyFlow("command(run = [\"c\"])\n"))

	want, err := engine.RunFile(testItem, testProject, file, modeOff())
	require.NoError(t, err)

	got, err := engine.result(stoppedHandle{}, testItem)

	require.NoError(t, err, "the run row is SUCCESS, so the stop is not the answer")
	assert.Equal(t, want, got)
}

func TestResult_ARunThatIsNotFinishedReportsTheStop(t *testing.T) {
	t.Parallel()

	engine := newHarness(t).engine(t)

	_, err := engine.result(stoppedHandle{}, testItem)

	require.ErrorIs(t, err, errEngineStopped)
}
