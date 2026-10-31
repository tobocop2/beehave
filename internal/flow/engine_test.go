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

	"github.com/tobocop2/obee/internal/agent"
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

func TestOpenStore_RefusesAFolderOthersCanUse(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	require.NoError(t, os.Mkdir(harness.opts.StateDir, 0o700))
	require.NoError(t, os.Chmod(harness.opts.StateDir, 0o777)) //nolint:gosec // the test needs an open folder to prove the refusal

	_, err := NewEngine(t.Context(), harness.opts, harness.deps())
	require.Error(t, err)
}

func TestOpenStore_ReportsAnUnwritableLogAndAnUnopenableDatabase(t *testing.T) {
	t.Parallel()

	for _, name := range []string{logFileName, databaseFile} {
		t.Run("a_folder_named_"+name, func(t *testing.T) {
			t.Parallel()

			harness := newHarness(t)
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
	harness.freshStore = true
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
		{"the_login_pick_fails", func(h *harness) { h.logins.err = errFake }, "pick the login of item 12", nil},
		{"no_login_is_set", func(h *harness) { h.logins.login = "" }, "no agent login for this step", nil},
		{"the_agent_fails", func(h *harness) { h.agents.err = errFake }, "fake failure", []string{"claim:12", "release:12"}},
		{"the_agent_exits_nonzero", func(h *harness) {
			h.agents.result = func(AgentRequest) agent.Result {
				return agent.Result{Outcome: agent.OutcomeExited, ExitCode: 3, Note: "bad exit"}
			}
		}, "the agent step of item 12 ended as exited (exit 3): bad exit. Fix the cause, then run obee resume 12", []string{"claim:12", "release:12"}},
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
	waitForTheOwner(t, harness)

	second := runInBackground(engine, testItem)

	infos, err := client.Status()
	require.NoError(t, err)
	require.Len(t, infos, 1)

	require.NoError(t, client.Answer(testItem, "go"))

	firstOutcome, secondOutcome := <-first, <-second
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
			eventually(t, func() bool { return harness.agents.running() == test.wantTotal })
			eventually(t, func() bool { return enqueuedPhases(t, engine) == 2-test.wantTotal })
			release()

			require.NoError(t, (<-first).err)
			require.NoError(t, (<-second).err)
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
	waitForTheOwner(t, harness)

	infos, err := other.Status()
	require.NoError(t, err)
	require.Len(t, infos, 1)
	assert.Equal(t, RunInfo{ID: "item-12-r1", State: runRunning, Version: testVersion, Stranded: true, Question: "What now?"}, infos[0])

	infos, err = same.Status()
	require.NoError(t, err)
	assert.False(t, infos[0].Stranded)

	require.ErrorIs(t, other.Answer(testItem, "go"), errStranded)
	require.ErrorIs(t, other.Cancel(testItem), errStranded)
	require.ErrorContains(t, other.Answer(testItem, "go"), "Start version v1 to finish it")

	require.NoError(t, same.Answer(testItem, "go"))
	require.NoError(t, (<-done).err)
}

func TestEngine_PinsTheApplicationVersionToTheBuildVersion(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.opts.Version = "v1.2.3-pinned"
	engine := harness.engine(t)

	_, err := engine.Run(testItem, testProject)
	require.NoError(t, err)

	assert.Equal(t, "v1.2.3-pinned+"+stepsVersion, dbos.GetApplicationVersion(engine.ctx), "the build version and the version of the builtin steps")

	infos, err := harness.client(t, "v1.2.3-pinned").Status()
	require.NoError(t, err)
	require.Len(t, infos, 1)
	assert.Equal(t, "v1.2.3-pinned", infos[0].Version)
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

	require.ErrorContains(t, engine.registerQueue(-1), "register the agents queue")
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

	want, err := engine.RunFile(testItem, testProject, file)
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
