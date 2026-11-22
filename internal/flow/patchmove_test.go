package flow

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
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
		result, err := client.JoinRun(t.Context(), testItem, testProject, file, modeOff())
		done <- runOutcome{result: result, err: err}
	}()

	waitOn(t, waiting[struct{}]{what: "the moved run to take the answer", run: runOfClient(t, client), read: func() (struct{}, bool, error) {
		return struct{}{}, client.Reply(testItem, Reply{Text: "yes"}) == nil, nil
	}})

	outcome := receive(t, done)

	require.NoError(t, outcome.err, "a client that joins the engine of this build moves the open run of the older release build")
	assert.Equal(t, []string{"old", "old", "new"}, seen.all())
}

func TestPatch_AWaitDoesNotTakeTheCancelOfAMoveForTheEndOfTheRun(t *testing.T) {
	t.Parallel()

	h, _ := otherBuildRun(t)
	client := h.client(t, otherBuild)
	run := runOfClient(t, client)

	// The move takes the fresh claim after the cancel and before the rewind, so the claim holds the move between the two.
	paused, resume := make(chan struct{}), make(chan struct{})

	var once sync.Once

	h.life.onClaim = func() { once.Do(func() { close(paused); <-resume }) }

	moved := make(chan error, 1)

	go func() {
		moved <- client.moveIfOtherBuild(t.Context(), attemptPlan{Join: testRun, Move: true}, testItem)
	}()

	select {
	case <-paused:
	case err := <-moved:
		t.Fatalf("the move ended before it took the claim: %v", err)
	}

	last, err := client.newest(testItem)
	require.NoError(t, err)
	require.Equal(t, dbos.WorkflowStatusCancelled, last.Status.Status, "the move canceled the run and has not rewound it yet")
	assert.Empty(t, run.stalled(time.Now()), "the move holds probe.lock, so the wait does not take its cancel for the end of the run")

	close(resume)
	require.NoError(t, receive(t, moved))
	assert.Empty(t, run.stalled(time.Now()), "the moved run is open on this build")

	require.NoError(t, client.Cancel(testItem))
	assert.Contains(t, run.stalled(time.Now()), "ended with status "+string(dbos.WorkflowStatusCancelled), "a cancel that no move follows is the end of the run")
}

func TestPatch_AWaitDoesNotTakeTheOpenRunOfAnotherBuildForOneThatParks(t *testing.T) {
	t.Parallel()

	h, _ := otherBuildRun(t)
	client := h.client(t, otherBuild)
	run := runOfClient(t, client)

	last, err := client.newest(testItem)
	require.NoError(t, err)
	require.NotEqual(t, otherBuild, last.Status.ApplicationVersion, "the run is open on the older build, and no move has put it on this build")
	require.NotEmpty(t, run.parked(time.Now()), "the run sits in a receive window that ends after the bound")

	assert.Empty(t, run.stalled(time.Now()), "the run takes no answer until a move, so the wait does not take it for a stall")

	owner := runOfClient(t, h.client(t, testVersion))
	assert.Contains(t, owner.stalled(time.Now()), "waits in step", "a wait on the build that started the run still sees it park")

	require.NoError(t, client.moveIfOtherBuild(t.Context(), attemptPlan{Join: testRun, Move: true}, testItem))
	assert.Empty(t, run.stalled(time.Now()), "the moved run is open on this build")
}

// gapReading is what a client reads of the run while a move has canceled it and not yet reopened it.
type gapReading struct {
	state     RunState
	replyErr  error
	pollEnded bool
	pollErr   error
}

// readInGap reads the run as status, reply and the wait of a join read it. It returns when all three have read.
func readInGap(client *Client) gapReading {
	var got gapReading

	infos, err := client.Status()
	if err != nil {
		got.state = RunState(err.Error())
	}

	for _, info := range infos {
		if info.ID == testRun {
			got.state = info.State
		}
	}

	got.replyErr = client.Reply(testItem, Reply{Text: "yes"})

	handle, err := dbos.RetrieveWorkflow[ItemResult](client.flow, testRun)
	if err != nil {
		got.pollErr = err

		return got
	}

	watch := engineWatch{dir: client.dir, item: testItem, grace: time.Hour, build: func() error { return nil }}
	_, got.pollEnded, got.pollErr = watch.poll(context.Background(), handle)

	return got
}

func TestResumeOnThisBuild_StatusReplyAndJoinWaitNeverSeeTheRunCanceledWhileItMoves(t *testing.T) {
	t.Parallel()

	h, _ := otherBuildRun(t)
	client := h.client(t, otherBuild)

	// The cancel releases the claim of the run at its end, after the store holds the cancel and before the reopen.
	// Three readers start there. A reader that waits for the move has not read when the window of the test ends.
	var once sync.Once

	read := make(chan gapReading, 1)
	finished := make(chan struct{})

	h.life.onRelease = func() {
		once.Do(func() {
			go func() {
				read <- readInGap(client)
				close(finished)
			}()

			select {
			case <-finished:
			case <-time.After(time.Second):
			}
		})
	}

	require.NoError(t, client.ResumeOnThisBuild(testItem))

	got := receive(t, read)
	assert.NotEqual(t, runCancelled, got.state, "status shows the moving run as canceled")
	assert.NotContains(t, fmt.Sprint(got.replyErr), "waits for no answer", "a reply to the moving run is refused as to a canceled run")
	assert.False(t, got.pollEnded, "the wait of a join takes the cancel of the move for the end of the run")
	require.NoError(t, got.pollErr)

	last, err := client.newest(testItem)
	require.NoError(t, err)
	assert.Equal(t, client.version, last.Status.ApplicationVersion)
	assert.False(t, last.terminal(), "the run is open on this build: %s", last.Status.Status)
}

func TestReaders_ACanceledRunWhoseProbeLockCannotBeTakenIsRefusedWithTheNameOfTheLock(t *testing.T) {
	t.Parallel()

	h, _ := otherBuildRun(t)
	client := h.client(t, otherBuild)

	require.NoError(t, h.client(t, testVersion).Cancel(testItem))

	path := filepath.Join(client.dir, probeLockName)
	require.NoError(t, os.Remove(path))
	require.NoError(t, os.Mkdir(path, 0o700))

	_, err := client.Status()
	require.ErrorContains(t, err, probeLockName)

	require.ErrorContains(t, client.Reply(testItem, Reply{Text: "yes"}), probeLockName)

	handle, err := dbos.RetrieveWorkflow[ItemResult](client.flow, testRun)
	require.NoError(t, err)

	watch := engineWatch{dir: client.dir, item: testItem, grace: time.Hour, build: func() error { return nil }}
	_, ended, err := watch.poll(t.Context(), handle)
	require.ErrorContains(t, err, probeLockName)
	assert.False(t, ended)
}

func TestReaders_ACanceledRunThatNoMoveHoldsReadsAsCanceled(t *testing.T) {
	t.Parallel()

	h, _ := otherBuildRun(t)
	client := h.client(t, otherBuild)

	require.NoError(t, h.client(t, testVersion).Cancel(testItem))

	got := readInGap(client)
	assert.Equal(t, runCancelled, got.state)
	require.ErrorContains(t, got.replyErr, "waits for no answer")
	assert.True(t, got.pollEnded)
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

	_, err = h.client(t, otherBuild).JoinRun(t.Context(), testItem, testProject, file, modeOff())

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

	_, err := first.RunFile(testItem, testProject, file, modeOff())
	require.Error(t, err, "the second call of the old build fails")
	require.NoError(t, first.Shutdown())

	h.opts.Modules = probeModules(newProbeBehindPatch, seen)
	next := h.engine(t)

	_, err = resumeAndJoin(t, next, h.client(t, testVersion))

	require.NoError(t, err)
	assert.Equal(t, []string{"old", "old", "new"}, seen.all(), "call 1 replays its old step, and the failed call 2 runs again as new code")
}
