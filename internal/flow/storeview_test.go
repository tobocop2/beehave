package flow

import (
	"errors"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var errSpy = errors.New("the store failed")

// moveSpy is a client of the store that fails the calls that a move makes, and tells a test when the move cancels.
type moveSpy struct {
	dbos.Client

	onCancel    func(id string)
	failSet     bool
	failClear   bool
	failCancel  bool
	failRewind  bool
	listFailAt  int
	listEmptyAt int
	listed      int
}

func (s *moveSpy) ListWorkflows(_ dbos.Client, opts ...dbos.ListWorkflowsOption) ([]dbos.WorkflowStatus, error) {
	s.listed++

	switch s.listed {
	case s.listFailAt:
		return nil, errSpy
	case s.listEmptyAt:
		return nil, nil
	}

	return s.Client.ListWorkflows(s.Client, opts...)
}

func (s *moveSpy) SetWorkflowAttributes(_ dbos.Client, id string, attributes map[string]any, opts ...dbos.SetWorkflowAttributesOption) error {
	if (attributes[moveAttribute] == nil && s.failClear) || (attributes[moveAttribute] != nil && s.failSet) {
		return errSpy
	}

	return s.Client.SetWorkflowAttributes(s.Client, id, attributes, opts...)
}

func (s *moveSpy) CancelWorkflow(_ dbos.Client, id string, opts ...dbos.CancelWorkflowOption) error {
	if s.onCancel != nil {
		s.onCancel(id)
	}

	if s.failCancel {
		return errSpy
	}

	return s.Client.CancelWorkflow(s.Client, id, opts...)
}

func (s *moveSpy) RewindWorkflow(_ dbos.Client, id string, opts ...dbos.RewindWorkflowOption) (dbos.WorkflowHandle[any], error) {
	if s.failRewind {
		return nil, errSpy
	}

	return s.Client.RewindWorkflow(s.Client, id, opts...)
}

// spyOn puts a spy between the client and its store for the rest of the test.
func spyOn(t *testing.T, client *Client) *moveSpy {
	t.Helper()

	spy := &moveSpy{Client: client.flow}
	client.flow = spy

	t.Cleanup(func() { client.flow = spy.Client })

	return spy
}

// markedMove does in the store what a mover does before its rewind: it marks the run and the open runs below it, and cancels the run. It returns the run as it was.
func markedMove(t *testing.T, client *Client) run {
	t.Helper()

	last, err := client.newest(testItem)
	require.NoError(t, err)

	below, err := client.openRowsUnder(testRun)
	require.NoError(t, err)
	require.NoError(t, client.markMove(append([]dbos.WorkflowStatus{last.Status}, below...)))
	require.NoError(t, dbos.CancelWorkflow(client.flow, testRun, dbos.WithCancelChildren()))

	return last
}

// exactRun reads the run as the store holds it.
func exactRun(t *testing.T, client *Client) dbos.WorkflowStatus {
	t.Helper()

	rows, err := client.store().Exact(dbos.WithFilterWorkflowIDs(testRun))
	require.NoError(t, err)
	require.Len(t, rows, 1)

	return rows[0]
}

// holdMoveLock holds move.lock of the client for the rest of the test, as a live mover does.
func holdMoveLock(t *testing.T, client *Client) {
	t.Helper()

	held := lockFile(client.dir, moveLockName)
	require.NoError(t, held.Lock())

	t.Cleanup(func() { require.NoError(t, held.Unlock()) })
}

// requireMoveLockFree requires that no process holds move.lock of the client.
func requireMoveLockFree(t *testing.T, client *Client) {
	t.Helper()

	probe := lockFile(client.dir, moveLockName)
	taken, err := probe.TryLock()

	require.NoError(t, err)
	require.True(t, taken, "a process holds move.lock")
	require.NoError(t, probe.Unlock())
}

// requireNoMarks requires that no run in the tree of the item carries the move mark.
func requireNoMarks(t *testing.T, client *Client) {
	t.Helper()

	below, err := client.rowsUnder(testRun)
	require.NoError(t, err)

	for _, row := range append([]dbos.WorkflowStatus{exactRun(t, client)}, below...) {
		_, marked := moveMarkOf(row)
		assert.False(t, marked, "run %s still carries the move mark", row.ID)
	}
}

func TestStoreView_ACanceledRunWithTheMarkAndAHeldMoveLockReadsAsOpenOnTheTargetVersion(t *testing.T) {
	t.Parallel()

	h, _ := otherBuildRun(t)
	client := h.client(t, otherBuild)

	markedMove(t, client)
	holdMoveLock(t, client)

	rows, err := client.store().List(dbos.WithFilterWorkflowIDs(testRun))

	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, dbos.WorkflowStatusPending, rows[0].Status, "a live mover rewinds the run to an open run")
	assert.Equal(t, client.version, rows[0].ApplicationVersion, "the rewind restamps the run with the version of the mover")
	assert.Equal(t, dbos.WorkflowStatusCancelled, exactRun(t, client).Status, "the store still holds the cancel")
}

func TestStoreView_ACanceledRunWithTheMarkAndAFreeMoveLockWhoseMoverFinishedReadsAsTheOpenRow(t *testing.T) {
	t.Parallel()

	h, _ := otherBuildRun(t)
	client := h.client(t, otherBuild)

	last := markedMove(t, client)
	stale := exactRun(t, client)
	mark, marked := moveMarkOf(stale)

	require.True(t, marked)
	require.NoError(t, client.reopenHere(testItem, last))

	read, err := client.store().moving(stale, mark)

	require.NoError(t, err)
	assert.NotEqual(t, dbos.WorkflowStatusCancelled, read.Status, "the mover rewound the run after the view listed it")
	assert.Equal(t, client.version, read.ApplicationVersion)
	requireMoveLockFree(t, client)
}

func TestStoreView_ACanceledRunWithTheMarkAndAFreeMoveLockWhoseMoverDiedReadsAsCanceled(t *testing.T) {
	t.Parallel()

	h, _ := otherBuildRun(t)
	client := h.client(t, otherBuild)

	markedMove(t, client)

	rows, err := client.store().List(dbos.WithFilterWorkflowIDs(testRun))

	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, dbos.WorkflowStatusCancelled, rows[0].Status)
	assert.Equal(t, otherBuildVersion(t, client), rows[0].ApplicationVersion, "the run stays on the build that started it")
	requireMoveLockFree(t, client)
}

// otherBuildVersion is the version that the run of otherBuildRun is stored under.
func otherBuildVersion(t *testing.T, client *Client) string {
	t.Helper()

	return exactRun(t, client).ApplicationVersion
}

func TestStoreView_ACanceledRunWithNoMarkNeverTakesTheMoveLock(t *testing.T) {
	t.Parallel()

	h, _ := otherBuildRun(t)
	client := h.client(t, otherBuild)

	require.NoError(t, h.client(t, testVersion).Cancel(testItem))
	// A folder in place of the lock file makes every take of the lock fail, so a read that takes it fails.
	require.NoError(t, os.Mkdir(filepath.Join(client.dir, moveLockName), 0o700))

	rows, err := client.store().List(dbos.WithFilterWorkflowIDs(testRun))

	require.NoError(t, err, "a run that no move marked reads with no lock")
	require.Len(t, rows, 1)
	assert.Equal(t, dbos.WorkflowStatusCancelled, rows[0].Status)

	open, err := client.store().Open("", dbos.WithFilterName(itemName))

	require.NoError(t, err)
	assert.Empty(t, open, "a canceled run with no mark is not open")
}

func TestStoreView_AMarkedRunThatCannotTakeTheMoveLockIsAnError(t *testing.T) {
	t.Parallel()

	h, _ := otherBuildRun(t)
	client := h.client(t, otherBuild)

	markedMove(t, client)
	require.NoError(t, os.Mkdir(filepath.Join(client.dir, moveLockName), 0o700))

	_, err := client.store().List(dbos.WithFilterWorkflowIDs(testRun))
	require.ErrorContains(t, err, "lock "+moveLockName)

	_, err = client.store().Open("", dbos.WithFilterName(itemName))
	require.ErrorContains(t, err, "lock "+moveLockName)
}

func TestStoreView_OpenAddsTheMovingRunsOfTheQueueOnce(t *testing.T) {
	t.Parallel()

	h, _ := otherBuildRun(t)
	client := h.client(t, otherBuild)

	markedMove(t, client)
	// A canceled run has lost its queue, so the mark names the queue that the run left.
	require.NoError(t, dbos.SetWorkflowAttributes(client.flow, testRun, map[string]any{moveAttribute: moveMark{To: client.version, Queue: itemsQueueName}}, dbos.WithMergeAttributes()))
	holdMoveLock(t, client)

	inQueue, err := client.store().Open(itemsQueueName)
	require.NoError(t, err)
	require.Len(t, inQueue, 1, "the run under a move counts in the queue that it left")
	assert.Equal(t, testRun, inQueue[0].ID)

	elsewhere, err := client.store().Open("another-queue")
	require.NoError(t, err)
	assert.Empty(t, elsewhere, "the run under a move does not count in a queue that it never held")

	anywhere, err := client.store().Open("", dbos.WithFilterName(itemName))
	require.NoError(t, err)
	assert.Len(t, anywhere, 1)
}

func TestStoreView_OpenNamesTheReadThatFailed(t *testing.T) {
	t.Parallel()

	for _, at := range []int{1, 2} {
		h, _ := otherBuildRun(t)
		client := h.client(t, otherBuild)
		view := viewOf(&moveSpy{Client: client.flow, listFailAt: at}, client.dir)

		_, err := view.Open("", dbos.WithFilterName(itemName))

		require.ErrorIs(t, err, errSpy, "read %d", at)
	}
}

func TestStoreView_AListThatFailsIsAnError(t *testing.T) {
	t.Parallel()

	h, _ := otherBuildRun(t)
	client := h.client(t, otherBuild)

	_, err := viewOf(&moveSpy{Client: client.flow, listFailAt: 1}, client.dir).List()

	require.ErrorIs(t, err, errSpy)
}

func TestStoreView_ARowThatTheStoreLostBetweenTheTwoReadsReadsAsItWas(t *testing.T) {
	t.Parallel()

	h, _ := otherBuildRun(t)
	client := h.client(t, otherBuild)

	markedMove(t, client)

	// The second read of the view is the read of the row under the free lock.
	view := viewOf(&moveSpy{Client: client.flow, listEmptyAt: 2}, client.dir)
	rows, err := view.List(dbos.WithFilterWorkflowIDs(testRun))

	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, dbos.WorkflowStatusCancelled, rows[0].Status)

	_, err = viewOf(&moveSpy{Client: client.flow, listFailAt: 2}, client.dir).List(dbos.WithFilterWorkflowIDs(testRun))
	require.ErrorIs(t, err, errSpy, "the read under the lock failed")
	requireMoveLockFree(t, client)
}

// statusHandle is a handle whose status the test sets.
type statusHandle struct {
	dbos.WorkflowHandle[ItemResult]

	status dbos.WorkflowStatus
	err    error
}

func (h statusHandle) GetStatus() (dbos.WorkflowStatus, error) { return h.status, h.err }

func TestStoreView_TheStatusOfAHandleReadsAMovingRunAsOpen(t *testing.T) {
	t.Parallel()

	h, _ := otherBuildRun(t)
	client := h.client(t, otherBuild)

	markedMove(t, client)
	holdMoveLock(t, client)

	handle, err := retrieve[ItemResult](client.store(), testRun)
	require.NoError(t, err)

	status, err := settledStatus(client.store(), handle)

	require.NoError(t, err)
	assert.Equal(t, dbos.WorkflowStatusPending, status.Status)

	_, err = retrieve[ItemResult](client.store(), "item-99-r1")
	require.Error(t, err, "a run that the store does not hold has no handle")
}

func TestStoreView_TheStatusOfAHandleNamesTheReadThatFailed(t *testing.T) {
	t.Parallel()

	h, _ := otherBuildRun(t)
	client := h.client(t, otherBuild)

	_, err := settledStatus(client.store(), statusHandle{err: errSpy})
	require.ErrorIs(t, err, errSpy)

	markedMove(t, client)
	require.NoError(t, os.Mkdir(filepath.Join(client.dir, moveLockName), 0o700))

	_, err = settledStatus(client.store(), statusHandle{status: exactRun(t, client)})
	require.ErrorContains(t, err, "lock "+moveLockName)
}

func TestStoreView_OnlyAWellFormedMarkOnTheRowIsAMark(t *testing.T) {
	t.Parallel()

	for name, attributes := range map[string]map[string]any{
		"no attributes":              nil,
		"another attribute":          {"customer": "acme"},
		"a cleared mark":             {moveAttribute: nil},
		"a mark of a string":         {moveAttribute: "to"},
		"a mark with no to":          {moveAttribute: map[string]any{"queue": "q"}},
		"a value that no JSON holds": {moveAttribute: make(chan int)},
	} {
		_, marked := moveMarkOf(dbos.WorkflowStatus{Attributes: attributes})

		assert.False(t, marked, name)
	}

	mark, marked := moveMarkOf(dbos.WorkflowStatus{Attributes: map[string]any{moveAttribute: map[string]any{"to": "v2", "queue": "q"}}})

	require.True(t, marked)
	assert.Equal(t, moveMark{To: "v2", Queue: "q"}, mark)
}

var holdMoveLockOf = flag.String("hold-move-lock-of", "", "the state folder whose move.lock the helper process of a test holds")

// TestHelperHoldsMoveLock is the process that TestStoreView_AMoverThatDiesLeavesARunThatReadsAsCanceled starts. It holds move.lock until it is killed.
func TestHelperHoldsMoveLock(t *testing.T) {
	if *holdMoveLockOf == "" {
		t.Skip("this test is the helper process of another test")
	}

	held := lockFile(*holdMoveLockOf, moveLockName)

	require.NoError(t, held.Lock())
	require.NoError(t, os.WriteFile(filepath.Join(*holdMoveLockOf, "mover.ready"), nil, 0o600))

	// The parent kills this process. The sleep ends the helper if the parent never does.
	time.Sleep(time.Minute)
	require.NoError(t, held.Unlock())
}

func TestStoreView_AMoverThatDiesLeavesARunThatReadsAsCanceled(t *testing.T) {
	t.Parallel()

	h, _ := otherBuildRun(t)
	client := h.client(t, otherBuild)

	// The mover marks and cancels, then a second process holds move.lock in its place and dies.
	markedMove(t, client)

	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestHelperHoldsMoveLock$", "-hold-move-lock-of="+client.dir) //nolint:gosec // G204: the test binary starts itself
	require.NoError(t, cmd.Start())

	ready := filepath.Join(client.dir, "mover.ready")

	waitOn(t, condition(func() bool { _, err := os.Stat(ready); return err == nil }))

	infos, err := client.Status()
	require.NoError(t, err)
	require.Len(t, infos, 1)
	assert.Equal(t, runRunning, infos[0].State, "the mover lives")

	require.NoError(t, cmd.Process.Kill())
	_ = cmd.Wait()

	infos, err = client.Status()
	require.NoError(t, err)
	require.Len(t, infos, 1)
	assert.Equal(t, runCancelled, infos[0].State, "the OS freed move.lock, so the run reads as canceled")

	require.NoError(t, client.ResumeOnThisBuild(testItem), "the next resume on this build moves the run")
	requireNoMarks(t, client)
	assert.False(t, exactRun(t, client).Status == dbos.WorkflowStatusCancelled, "the run is open again")
}

func TestMove_TheMarkAndTheLockAreInPlaceBeforeTheCancel(t *testing.T) {
	t.Parallel()

	h, _ := otherBuildRun(t)
	client := h.client(t, otherBuild)
	spy := spyOn(t, client)

	var (
		seen   moveMark
		marked bool
		locked bool
	)

	spy.onCancel = func(id string) {
		if id != testRun {
			return
		}

		seen, marked = moveMarkOf(exactRun(t, client))

		probe := lockFile(client.dir, moveLockName)
		taken, err := probe.TryLock()
		require.NoError(t, err)

		locked = !taken
		if taken {
			require.NoError(t, probe.Unlock())
		}
	}

	require.NoError(t, client.ResumeOnThisBuild(testItem))

	assert.True(t, marked, "the move cancels a run that carries no mark")
	assert.Equal(t, client.version, seen.To)
	assert.True(t, locked, "the move cancels without move.lock")
}

func TestMove_ASuccessfulMoveClearsTheMarksAndFreesTheLock(t *testing.T) {
	t.Parallel()

	h, _ := otherBuildRun(t)
	client := h.client(t, otherBuild)

	require.NoError(t, client.ResumeOnThisBuild(testItem))

	requireNoMarks(t, client)
	requireMoveLockFree(t, client)
	assert.Equal(t, dbos.WorkflowStatusEnqueued, exactRun(t, client).Status)
}

func TestMove_AMoveThatFailsAfterItsCancelLeavesNoMarkAndNoLock(t *testing.T) {
	t.Parallel()

	for name, arrange := range map[string]func(h *harness, spy *moveSpy){
		"the claim fails":  func(h *harness, _ *moveSpy) { h.life.failing = map[string]bool{"claim": true} },
		"the rewind fails": func(_ *harness, spy *moveSpy) { spy.failRewind = true },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h, _ := otherBuildRun(t)
			client := h.client(t, otherBuild)
			arrange(h, spyOn(t, client))

			err := client.ResumeOnThisBuild(testItem)

			require.ErrorContains(t, err, "now canceled")
			requireNoMarks(t, client)
			requireMoveLockFree(t, client)

			infos, statusErr := client.Status()
			require.NoError(t, statusErr)
			require.Len(t, infos, 1)
			assert.Equal(t, runCancelled, infos[0].State, "the run reads as canceled")
		})
	}
}

func TestMove_AMoveThatFailsAtTheCancelLeavesTheRunOpenWithNoMarkAndNoLock(t *testing.T) {
	t.Parallel()

	h, _ := otherBuildRun(t)
	client := h.client(t, otherBuild)
	spyOn(t, client).failCancel = true

	err := client.ResumeOnThisBuild(testItem)

	require.ErrorIs(t, err, errSpy)
	requireNoMarks(t, client)
	requireMoveLockFree(t, client)
	assert.NotEqual(t, dbos.WorkflowStatusCancelled, exactRun(t, client).Status, "the cancel did not happen")
}

func TestMove_AMoveThatCannotMarkTheRunDoesNotCancelIt(t *testing.T) {
	t.Parallel()

	h, _ := otherBuildRun(t)
	client := h.client(t, otherBuild)
	spyOn(t, client).failSet = true

	err := client.ResumeOnThisBuild(testItem)

	require.ErrorContains(t, err, "mark run "+testRun)
	requireNoMarks(t, client)
	requireMoveLockFree(t, client)
	assert.NotEqual(t, dbos.WorkflowStatusCancelled, exactRun(t, client).Status)
}

func TestMove_AMoveThatCannotReadTheRunsBelowDoesNotCancelIt(t *testing.T) {
	t.Parallel()

	h, _ := otherBuildRun(t)
	client := h.client(t, otherBuild)
	last, err := client.newest(testItem)
	require.NoError(t, err)

	spyOn(t, client).listFailAt = 1

	_, err = client.beginMove(last)

	require.ErrorIs(t, err, errSpy)
	requireMoveLockFree(t, client)
}

func TestMove_AMarkThatCannotBeClearedIsAnErrorAndTheErrorsJoin(t *testing.T) {
	t.Parallel()

	h, _ := otherBuildRun(t)
	client := h.client(t, otherBuild)
	spy := spyOn(t, client)
	spy.failClear = true

	err := client.ResumeOnThisBuild(testItem)

	require.ErrorContains(t, err, "clear the move mark of run "+testRun, "the move rewound the run and could not clear its mark")
	requireMoveLockFree(t, client)
	assert.NotEqual(t, dbos.WorkflowStatusCancelled, exactRun(t, client).Status, "the run is open")

	spy.failCancel = true
	spy.failClear = true

	h2, _ := otherBuildRun(t)
	other := h2.client(t, otherBuild)
	otherSpy := spyOn(t, other)
	otherSpy.failCancel = true
	otherSpy.failClear = true

	err = other.ResumeOnThisBuild(testItem)

	require.ErrorIs(t, err, errSpy, "the cancel failed")
	require.ErrorContains(t, err, "clear the move mark", "the failed move could not clear its mark")
	requireMoveLockFree(t, other)
}

func TestMove_ClearMarksNamesTheReadThatFailed(t *testing.T) {
	t.Parallel()

	for at, want := range map[int]string{1: "read workflow " + testRun, 2: "list the children of run " + testRun} {
		h, _ := otherBuildRun(t)
		client := h.client(t, otherBuild)

		markedMove(t, client)
		spyOn(t, client).listFailAt = at

		err := client.clearMarks(testRun)

		require.ErrorContains(t, err, want, "read %d", at)
		require.ErrorIs(t, err, errSpy)
	}
}

func TestMove_AReopenThatCannotClearTheMarksOfAMoverThatDiedIsAnError(t *testing.T) {
	t.Parallel()

	h, _ := otherBuildRun(t)
	client := h.client(t, otherBuild)

	markedMove(t, client)
	spyOn(t, client).failClear = true

	err := client.ResumeOnThisBuild(testItem)

	require.ErrorContains(t, err, "clear the move mark")
	assert.NotEqual(t, dbos.WorkflowStatusCancelled, exactRun(t, client).Status, "the rewind ran")
}

func TestMove_AReopenThatFailsLeavesTheMarksOfAMoverThatDied(t *testing.T) {
	t.Parallel()

	h, _ := otherBuildRun(t)
	client := h.client(t, otherBuild)

	markedMove(t, client)

	h.life.failing = map[string]bool{"claim": true}

	require.ErrorContains(t, client.ResumeOnThisBuild(testItem), "claim of item")
	assert.Equal(t, dbos.WorkflowStatusCancelled, exactRun(t, client).Status)
}

func TestMove_AMoveThatCannotTakeTheMoveLockDoesNotCancelTheRun(t *testing.T) {
	t.Parallel()

	h, _ := otherBuildRun(t)
	client := h.client(t, otherBuild)

	require.NoError(t, os.Mkdir(filepath.Join(client.dir, moveLockName), 0o700))

	err := client.ResumeOnThisBuild(testItem)

	require.ErrorContains(t, err, "lock "+moveLockName)
	assert.NotEqual(t, dbos.WorkflowStatusCancelled, exactRun(t, client).Status)
}

func TestStoreView_ARunThatTheOpenReadListedIsNotAddedAgain(t *testing.T) {
	t.Parallel()

	h, _ := otherBuildRun(t)
	client := h.client(t, otherBuild)

	markedMove(t, client)
	holdMoveLock(t, client)

	listed := []dbos.WorkflowStatus{{ID: testRun, Status: dbos.WorkflowStatusPending}}

	open, err := client.store().addMoving(listed, "", []dbos.ListWorkflowsOption{dbos.WithFilterWorkflowIDs(testRun)})

	require.NoError(t, err)
	assert.Len(t, open, 1, "the run counts once")
}
