package flow

import (
	"context"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// failFlow calls now twice and waits for no one.
const failFlow = "def main(item):\n    now()\n    now()\n"

// oldProbeFailingSecond is the old build whose second call fails, so the run ends as failed after one recorded step.
func oldProbeFailingSecond() func(*script) (string, error) {
	calls := 0

	return func(s *script) (string, error) {
		calls++
		if calls == 2 {
			return countedStep(s, "now", "", func(context.Context) (string, error) { return "", errFake })
		}

		return oldProbe(s)
	}
}

func TestPatch_AnOpenRunOfAnotherReleaseBuildJoinsUnderRunWhenNoModuleChanged(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	seen := &probes{}

	h.opts.Modules = probeModules(oldProbe, seen)
	file := writeFlow(t, probeFlow)
	interrupted(t, h, file)
	expireLease(h)

	h.opts.Modules = probeModules(newProbeBehindPatch, seen)
	h.opts.Version = otherBuild
	engine := h.engine(t)
	client := h.client(t, otherBuild)
	done := runInBackground2(engine, file)

	waitOn(t, waiting[struct{}]{what: "the moved run to take the answer", run: runOfClient(t, client), read: func() (struct{}, bool, error) {
		return struct{}{}, client.Reply(testItem, Reply{Text: "yes"}) == nil, nil
	}})

	outcome := receive(t, done)

	require.NoError(t, outcome.err, "the modules are equal, so run moves the open run of the older release build and finishes it")
	assert.Equal(t, []string{"old", "old", "new"}, seen.all(), "call 1 replays its old step, and call 2 is new code")

	last, found, err := latestRun(engine.ctx, testItem)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, engine.version, last.Status.ApplicationVersion, "the run is stored under this build")
}

func TestPatch_AnOpenRunOfAnotherReleaseBuildJoinsUnderServeWhenNoModuleChanged(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	seen := &probes{}

	h.opts.Modules = probeModules(oldProbe, seen)
	file := writeFlow(t, probeFlow)
	interrupted(t, h, file)
	expireLease(h)

	h.opts.Modules = probeModules(newProbeBehindPatch, seen)
	h.opts.Version = otherBuild
	h.engine(t)

	client := h.client(t, otherBuild)
	done := make(chan runOutcome, 1)

	go func() {
		result, err := client.JoinRun(t.Context(), testItem, testProject, file)
		done <- runOutcome{result: result, err: err}
	}()

	waitOn(t, waiting[struct{}]{what: "the moved run to take the answer", run: runOfClient(t, client), read: func() (struct{}, bool, error) {
		return struct{}{}, client.Reply(testItem, Reply{Text: "yes"}) == nil, nil
	}})

	outcome := receive(t, done)

	require.NoError(t, outcome.err, "a client that joins the engine of this build moves the open run of the older release build")
	assert.Equal(t, []string{"old", "old", "new"}, seen.all())
}

func TestPatch_AnOpenRunOfAnotherReleaseBuildIsRefusedByRunAndNamesTheModuleThatChanged(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	seen := &probes{}

	h.opts.Modules = probeModules(oldProbe, seen)
	file := writeFlow(t, probeFlow)
	interrupted(t, h, file)
	expireLease(h)

	_, err := sqlOf(t, h).ExecContext(t.Context(), "UPDATE workflow_status SET application_version = ? WHERE workflow_uuid = ?",
		versionWithModule(testVersion, moduleCore, coreVersion+1), testRun)
	require.NoError(t, err)

	h.opts.Modules = probeModules(newProbeBehindPatch, seen)
	h.opts.Version = otherBuild
	engine := h.engine(t)

	_, err = runFileWithin(t, engine, file)

	require.ErrorIs(t, err, errStranded)
	require.ErrorContains(t, err, "core module at version")
	assert.Equal(t, []string{"old"}, seen.all(), "no call ran on the new build")
}

func TestPatch_AnOpenRunOfAnotherReleaseBuildIsRefusedByServeAndNamesTheModuleThatChanged(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	seen := &probes{}

	h.opts.Modules = probeModules(oldProbe, seen)
	file := writeFlow(t, probeFlow)
	interrupted(t, h, file)
	expireLease(h)

	_, err := sqlOf(t, h).ExecContext(t.Context(), "UPDATE workflow_status SET application_version = ? WHERE workflow_uuid = ?",
		versionWithModule(testVersion, moduleCore, coreVersion+1), testRun)
	require.NoError(t, err)

	h.opts.Modules = probeModules(newProbeBehindPatch, seen)
	h.opts.Version = otherBuild
	h.engine(t)

	_, err = h.client(t, otherBuild).JoinRun(t.Context(), testItem, testProject, file)

	require.ErrorIs(t, err, errStranded)
	require.ErrorContains(t, err, "core module at version")
	assert.Equal(t, []string{"old"}, seen.all(), "no call ran on the new build")
}

func TestPatch_AnOpenRunWhoseHolderStillRunsIsNotMovedByRun(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	seen := &probes{}

	h.opts.Modules = probeModules(oldProbe, seen)
	file := writeFlow(t, probeFlow)
	interrupted(t, h, file)
	h.beats.setRenewed(time.Now())

	h.opts.Modules = probeModules(newProbeBehindPatch, seen)
	h.opts.Version = otherBuild
	engine := h.engine(t)

	_, err := runFileWithin(t, engine, file)

	require.ErrorIs(t, err, errStranded)
	require.ErrorContains(t, err, "claim lease")
	assert.Equal(t, []string{"old"}, seen.all())
}

func TestPatch_ARunThatHoldsAMarkerMovesToAnotherReleaseBuild(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	seen := &probes{}

	h.opts.Modules = probeModules(newProbeBehindPatch, seen)
	file := writeFlow(t, probeFlow)
	interrupted(t, h, file)
	expireLease(h)

	h.opts.Version = otherBuild
	engine := h.engine(t)
	client := h.client(t, otherBuild)

	last, found, err := latestRun(engine.ctx, testItem)
	require.NoError(t, err)
	require.True(t, found)

	names, err := client.stepNames(last.Status.ID)
	require.NoError(t, err)
	t.Logf("steps before the move: %v", names)
	require.Equal(t, 1, markers(names), "the run holds a marker before the move")

	require.NoError(t, client.ResumeOnThisBuild(testItem), "a step with the DBOS prefix is a step of the engine, so the marker does not block the move")

	handle, err := dbos.RetrieveWorkflow[ItemResult](engine.ctx, last.Status.ID)
	require.NoError(t, err)

	waitOn(t, waiting[struct{}]{what: "the moved run to take the answer", run: runOfClient(t, client), read: func() (struct{}, bool, error) {
		return struct{}{}, client.Reply(testItem, Reply{Text: "yes"}) == nil, nil
	}})

	_, err = resultOfHandle(t, handle)

	require.NoError(t, err)
	assert.Equal(t, []string{"new", "new", "new"}, seen.all())
}

func TestPatch_AFailedRunRewoundByResumeAfterTheChangeKeepsItsOldCallAndTakesTheNewNext(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	seen := &probes{}

	h.opts.Modules = probeModules(oldProbeFailingSecond(), seen)
	file := writeFlow(t, failFlow)
	first := h.engine(t)

	_, err := first.RunFile(testItem, testProject, file)
	require.Error(t, err, "the second call of the old build fails")
	require.NoError(t, first.Shutdown())

	h.opts.Modules = probeModules(newProbeBehindPatch, seen)
	next := h.engine(t)

	_, err = resumeAndJoin(t, next, h.client(t, testVersion))

	require.NoError(t, err)
	assert.Equal(t, []string{"old", "old", "new"}, seen.all(), "call 1 replays its old step, and the failed call 2 runs again as new code")
}
