package flow

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/roundcut"
	trackerstore "github.com/tobocop2/beehest/internal/store"
)

// claimLoser loses the claim of the run of the test item, as the renewal does when the tracker says that the claim is gone.
type claimLoser struct{ engine *Engine }

func (l *claimLoser) lose() {
	loseClaim(l.engine, runID(testItem, 1), "a test lost the claim")
}

// loseCommand is the program name that makes the fake command runner lose the claim, at the point of the flow where the test wants the loss.
const loseCommand = "lose"

// loseWhenAsked makes the command runner of the harness lose the claim when a flow runs the command named loseCommand.
func loseWhenAsked(h *harness, l *claimLoser) {
	h.commands.before = func(_ context.Context, request CommandRequest) error {
		if request.Run[0] == loseCommand {
			l.lose()
		}

		return nil
	}
}

// stepWrite is one write that a step of a workflow file makes: the call, and what the fake of the forge or of the tracker recorded.
type stepWrite struct {
	name    string
	call    string
	written func(h *harness, w *fakeWriter) []string
}

func stepWrites() []stepWrite {
	return []stepWrite{
		{"pr_comment", `pr_comment(text = "hello", key = "k", name = "c")`, func(h *harness, _ *fakeWriter) []string { return h.forge.notes }},
		{"pr_open", `pr_open(title = "t", base = "main", name = "o")`, func(h *harness, _ *fakeWriter) []string { return h.forge.opened }},
		{"item_create", `item_create(title = "t", key = "k", name = "i")`, func(_ *harness, w *fakeWriter) []string { return w.created }},
		{"item_status", `item_status(id = "n-1", status = "closed", name = "s")`, func(_ *harness, w *fakeWriter) []string { return w.statuses }},
		{"item_comment", `item_comment(id = "n-1", text = "hello", key = "k", name = "c")`, func(_ *harness, w *fakeWriter) []string { return w.notes }},
		{"round_sync", `round_sync(base = "round-i", trunk = "main", name = "rs")`, roundCalls},
		{"round_land", `round_land(pr = 7, head = "h", base_tip = "t", name = "rl")`, roundCalls},
		{"round_cut", `round_cut(old = "round-i", new = "round-ii", pr = 9, trunk = "main", name = "rc")`, roundCalls},
		{"integration_pull", `integration_pull(base = "round-i", into = "main", pr = 9, name = "ip")`, roundCalls},
		{"board_card", `board_card(board = 3, status = "Done", pr = 7, name = "bc")`, roundCalls},
		{"pr_take", `pr_take(pr = 7, me = "me", idle = "24", grace = "1", step = "apply", name = "pt")`, roundCalls},
	}
}

// roundCalls lists the calls that the round writer of the harness received, each as the line of the tool that it stands for.
func roundCalls(h *harness, _ *fakeWriter) []string {
	h.commands.mu.Lock()
	defer h.commands.mu.Unlock()

	var calls []string

	for _, request := range h.commands.requests {
		if len(request.Run) > 0 && slices.Contains([]string{boardProgram, takeProgram, syncProgram, landProgram, cutProgram, integrationProgram}, request.Run[0]) {
			calls = append(calls, strings.Join(request.Run, " "))
		}
	}

	return calls
}

// writeAfter runs the call after a command that loses the claim when lost is true. It returns what the fakes recorded for the call, and the error of the run.
func writeAfter(t *testing.T, w stepWrite, lost bool) ([]string, error) {
	t.Helper()

	h := newHarness(t)
	writer := newFakeWriter()
	h.writer = writer
	loser := &claimLoser{}
	loseWhenAsked(h, loser)

	loser.engine = launchedWith(t, h, func(*Deps) {})

	before := `command(name = "before", run = ["` + loseCommand + `"])`
	if !lost {
		before = `command(name = "before", run = ["keep"])`
	}

	_, err := loser.engine.RunFile(testItem, testProject, writeFlow(t, bodyFlow(before+"\n"+w.call)), modeOff())

	return w.written(h, writer), err
}

func TestClaimWrites_AStepWritesWhileTheRunHoldsItsClaim(t *testing.T) {
	t.Parallel()

	for _, w := range stepWrites() {
		t.Run(w.name, func(t *testing.T) {
			t.Parallel()

			written, err := writeAfter(t, w, false)

			require.NoError(t, err)
			assert.Len(t, written, 1, "control: with the claim kept, the write reaches the fake")
		})
	}
}

func TestClaimWrites_AStepThatWritesAfterTheRunLostItsClaimWritesNothing(t *testing.T) {
	t.Parallel()

	for _, w := range stepWrites() {
		t.Run(w.name, func(t *testing.T) {
			t.Parallel()

			written, err := writeAfter(t, w, true)

			require.ErrorIs(t, err, errClaimLost)
			assert.Empty(t, written, "the claim was lost before the write, so nothing reaches the forge or the tracker")
		})
	}
}

func TestClaimWrites_AMergeOfARunThatNeverHeldAClaimLandsNothing(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	deps := rig.deps()
	deps.Clock.Grace = 20 * time.Millisecond

	engine, err := NewEngine(t.Context(), rig.h.opts, deps)
	require.NoError(t, err)
	require.NoError(t, engine.Launch())
	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	// The merge runs as a child of its run and can wait behind a gate for a long time, so the claim is read again when the child reaches the merge call.
	handle, err := dbos.RunWorkflow(engine.ctx, engine.exclusive, ExclusiveInput{Kind: kindMerge, Run: "item-12-r9", RunItem: testItem, Project: testProject, Item: "1", Head: rig.heads["1"]},
		dbos.WithQueue(engine.exclusiveQueue), dbos.WithQueuePartitionKey(testProject))
	require.NoError(t, err)

	out, err := handle.GetResult()

	require.NoError(t, err, "the exclusive run records a failed call as its failure")
	require.NotNil(t, out.Failure)
	assert.Contains(t, out.Failure.Text, errClaimLost.Error())
	assert.Empty(t, rig.merger.asked, "the run holds no claim, so the merger is never asked")
}

// heldRun is a run of the test item that holds its claim and waits in a command, so a test can lose the claim or queue a merge while the run is open.
type heldRun struct {
	engine   *Engine
	release  chan struct{}
	done     chan error
	waiting  chan struct{} // closed when the command starts to wait
	canceled chan struct{} // closed when the context of the waiting command ends
	once     sync.Once
	waited   sync.Once
	ended    sync.Once
}

// startHeldRun starts the run on an engine of deps and returns when the run holds its claim.
func startHeldRun(t *testing.T, h *harness, deps Deps) *heldRun {
	t.Helper()

	engine, err := NewEngine(t.Context(), h.opts, deps)
	require.NoError(t, err)
	require.NoError(t, engine.Launch())
	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	return holdRun(t, h, engine, func(context.Context, CommandRequest) error { return nil })
}

// holdRun starts the run on a launched engine of h and returns when the run holds its claim. others runs for each command but the wait of the run.
// The wait of a run that a later engine of h recovers waits again.
func holdRun(t *testing.T, h *harness, engine *Engine, others func(context.Context, CommandRequest) error) *heldRun {
	t.Helper()

	r := &heldRun{engine: engine, release: make(chan struct{}), done: make(chan error, 1), canceled: make(chan struct{}), waiting: make(chan struct{})}
	h.commands.before = func(ctx context.Context, request CommandRequest) error {
		if len(request.Run) == 0 || request.Run[0] != "wait" {
			return others(ctx, request)
		}

		r.waited.Do(func() { close(r.waiting) })

		select {
		case <-r.release:
			return nil
		case <-ctx.Done():
			r.ended.Do(func() { close(r.canceled) })

			return ctx.Err()
		}
	}

	file := writeFlow(t, bodyFlow(`command(name = "w", run = ["wait"])`))

	go func() {
		_, err := engine.RunFile(testItem, testProject, file, modeOff())
		r.done <- err
	}()

	require.Eventually(t, func() bool { return len(engine.holds.snapshot()) == 1 }, 10*time.Second, 10*time.Millisecond)

	select {
	case <-r.waiting:
	case <-time.After(10 * time.Second):
		require.Fail(t, "the command of the run never started")
	}

	return r
}

// finish lets the waiting command end and returns the error of the run.
func (r *heldRun) finish() error {
	r.once.Do(func() { close(r.release) })

	return <-r.done
}

// queueMerge queues the merge of a member as a child that names the run of the test item, and returns the failure that the child recorded.
func queueMerge(t *testing.T, engine *Engine, run, member, head string) *Failure {
	t.Helper()

	handle, err := dbos.RunWorkflow(engine.ctx, engine.exclusive, ExclusiveInput{Kind: kindMerge, Run: run, RunItem: testItem, Project: testProject, Item: member, Head: head},
		dbos.WithQueue(engine.exclusiveQueue), dbos.WithQueuePartitionKey(testProject))
	require.NoError(t, err)

	out, err := handle.GetResult()
	require.NoError(t, err, "the exclusive run records a failed call as its failure")

	return out.Failure
}

func TestClaimWrites_AMergeOfARunThatHeldItsClaimAndLostItLandsNothing(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	run := startHeldRun(t, rig.h, rig.deps())

	loseClaim(run.engine, runID(testItem, 1), "a test lost the claim")
	failure := queueMerge(t, run.engine, runID(testItem, 1), "1", rig.heads["1"])
	require.Error(t, run.finish(), "the run ends on the loss")

	require.NotNil(t, failure)
	assert.Contains(t, failure.Text, errClaimLost.Error())
	assert.Empty(t, rig.merger.asked, "the run held its claim and lost it, so the merger is never asked")
}

func TestClaimWrites_AMergeChildWithNoRunIsRefusedWhileOneRunIsOpen(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	deps := rig.deps()
	deps.Clock.Grace = 20 * time.Millisecond
	run := startHeldRun(t, rig.h, deps)

	// A child that an older build recorded names no run. The one open run is no claim of the child.
	failure := queueMerge(t, run.engine, "", "1", rig.heads["1"])
	require.NoError(t, run.finish())

	require.NotNil(t, failure)
	assert.Contains(t, failure.Text, errClaimLost.Error())
	assert.Empty(t, rig.merger.asked)
}

func TestClaimWrites_ACommandStepIsCancelledWhenTheRunLosesItsClaim(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	run := startHeldRun(t, h, h.deps())

	loseClaim(run.engine, runID(testItem, 1), "a test lost the claim")

	select {
	case <-run.canceled:
	case <-time.After(10 * time.Second):
		require.Fail(t, "the command kept running after the claim was lost")
	}

	require.ErrorIs(t, run.finish(), errClaimLost)
}

// markCommand is the program of a step that must not run after the loss.
const markCommand = "mark"

func TestClaimWrites_AStepThatRunsAProgramAfterTheLossNeverRunsIt(t *testing.T) {
	t.Parallel()

	for _, builtin := range []string{"command", "write"} {
		call := builtin + `(name = "after", run = ["` + markCommand + `"])`

		t.Run(builtin, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			loser := &claimLoser{}
			loseWhenAsked(h, loser)
			loser.engine = launchedWith(t, h, func(*Deps) {})

			_, err := loser.engine.RunFile(testItem, testProject, writeFlow(t, bodyFlow(`command(name = "before", run = ["`+loseCommand+`"])`+"\n"+call)), modeOff())

			require.ErrorIs(t, err, errClaimLost)
			assert.Equal(t, []string{loseCommand}, h.commands.ran(), "the program of the step that starts after the loss never runs: a command can write to the forge")
		})
	}
}

// lapseCommand is the program name that makes the fake command runner move the lease clock past the trust limit, with no tick and so no loss.
const lapseCommand = "lapse"

func TestClaimWrites_AStepThatRunsAProgramAfterTheLeaseLapsedWithNoTickNeverRunsIt(t *testing.T) {
	t.Parallel()

	mark := `["` + markCommand + `"]`
	calls := map[string]string{
		"command":         `command(name = "after", run = ` + mark + `)`,
		"write":           `write(name = "after", run = ` + mark + `)`,
		"write_read_back": `write(name = "after", run = ["other"], read_back = ` + mark + `)`,
	}

	for step, call := range calls {
		for name, lapsed := range map[string]bool{"the_claim_is_trusted": false, "the_lease_lapsed": true} {
			t.Run(step+"/"+name, func(t *testing.T) {
				t.Parallel()

				lapsedStep(t, call, lapsed)
			})
		}
	}
}

func TestClaimWrites_ARoundWriteAfterTheLeaseLapsedWithNoTickWritesNothing(t *testing.T) {
	t.Parallel()

	for _, w := range stepWrites() {
		if !strings.HasPrefix(w.name, "round_") && w.name != "integration_pull" && w.name != "board_card" && w.name != "pr_take" {
			continue
		}

		for name, lapsed := range map[string]bool{"the_claim_is_trusted": false, "the_lease_lapsed": true} {
			t.Run(w.name+"/"+name, func(t *testing.T) {
				t.Parallel()

				written, err := roundWriteAfterLapse(t, w, lapsed)

				if !lapsed {
					require.NoError(t, err)
					assert.Len(t, written, 1, "control: with the claim trusted, the write reaches the round writer")

					return
				}

				require.ErrorIs(t, err, errClaimLost)
				assert.Empty(t, written, "the write reads the claim at its start: the context of the run is still live, so only that read sees the lapse")
			})
		}
	}
}

// roundWriteAfterLapse runs the round write after a command that moves the lease clock past the trust limit when lapsed is true. It returns the calls that the round writer got.
func roundWriteAfterLapse(t *testing.T, w stepWrite, lapsed bool) ([]string, error) {
	t.Helper()

	h := newHarness(t)
	clk := &leaseClock{now: time.Unix(1_000_000, 0)}
	h.commands.before = func(_ context.Context, request CommandRequest) error {
		if request.Run[0] == lapseCommand {
			clk.advance(trustLimit)
		}

		return nil
	}
	engine := launchedWith(t, h, func(deps *Deps) { clk.install(&deps.Clock) })

	before := `command(name = "before", run = ["keep"])`
	if lapsed {
		before = `command(name = "before", run = ["` + lapseCommand + `"])`
	}

	_, err := engine.RunFile(testItem, testProject, writeFlow(t, bodyFlow(before+"\n"+w.call)), modeOff())

	return w.written(h, nil), err
}

// lapsedStep runs call after a command that moves the lease clock past the trust limit when lapsed is true. No tick runs, so the context of the run stays live.
func lapsedStep(t *testing.T, call string, lapsed bool) {
	t.Helper()

	h := newHarness(t)
	clk := &leaseClock{now: time.Unix(1_000_000, 0)}
	h.commands.before = func(_ context.Context, request CommandRequest) error {
		if request.Run[0] == lapseCommand {
			clk.advance(trustLimit)
		}

		return nil
	}
	engine := launchedWith(t, h, func(deps *Deps) { clk.install(&deps.Clock) })

	before := `command(name = "before", run = ["keep"])`
	if lapsed {
		before = `command(name = "before", run = ["` + lapseCommand + `"])`
	}

	_, err := engine.RunFile(testItem, testProject, writeFlow(t, bodyFlow(before+"\n"+call)), modeOff())

	if !lapsed {
		require.NoError(t, err)
		assert.Contains(t, h.commands.ran(), markCommand, "control: the program of the step runs while the claim is trusted")

		return
	}

	require.ErrorIs(t, err, errClaimLost)
	assert.NotContains(t, h.commands.ran(), markCommand, "the step reads the claim at the program start: the context of the run is still live, so only that read sees the lapse")
}

func TestClaimWrites_ACommandChildOfARunThatLostItsClaimNeverRunsItsProgram(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	run := startHeldRun(t, h, h.deps())

	loseClaim(run.engine, runID(testItem, 1), "a test lost the claim")

	handle, err := dbos.RunWorkflow(run.engine.ctx, run.engine.exclusive, ExclusiveInput{Kind: kindCommand, Run: runID(testItem, 1), RunItem: testItem, Project: testProject, Item: testItem, Request: CommandRequest{Project: testProject, Item: testItem, Run: []string{markCommand}}},
		dbos.WithQueue(run.engine.exclusiveQueue), dbos.WithQueuePartitionKey(testProject))
	require.NoError(t, err)

	out, err := handle.GetResult()
	require.NoError(t, err)
	require.Error(t, run.finish())

	require.NotNil(t, out.Failure)
	assert.Contains(t, out.Failure.Text, errClaimLost.Error())
	assert.NotContains(t, h.commands.ran(), markCommand)
}

func TestClaimWrites_ALossWhileTheMergeRunsCancelsTheContextOfTheMerge(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	rig.merger.hold = make(chan context.Context, 1)
	run := startHeldRun(t, rig.h, rig.deps())

	handle, err := dbos.RunWorkflow(run.engine.ctx, run.engine.exclusive, ExclusiveInput{Kind: kindMerge, Run: runID(testItem, 1), RunItem: testItem, Project: testProject, Item: "1", Head: rig.heads["1"]},
		dbos.WithQueue(run.engine.exclusiveQueue), dbos.WithQueuePartitionKey(testProject))
	require.NoError(t, err)

	var mergeCtx context.Context

	select {
	case mergeCtx = <-rig.merger.hold:
	case <-time.After(10 * time.Second):
		require.Fail(t, "the merge never started")
	}

	loseClaim(run.engine, runID(testItem, 1), "a test lost the claim")

	select {
	case <-mergeCtx.Done():
	case <-time.After(10 * time.Second):
		require.Fail(t, "the merge kept running after the claim was lost: it runs under a context that the claim does not own")
	}

	_, err = handle.GetResult()
	require.NoError(t, err)
	require.Error(t, run.finish())
}

// holdCut is a round writer whose cut waits for the end of its context, and tells the test which context it got.
type holdCut struct {
	rounds

	got chan context.Context
}

func (h holdCut) Cut(ctx context.Context, _ string, _ roundcut.Request) (roundcut.Result, error) {
	h.got <- ctx

	<-ctx.Done()

	return roundcut.Result{}, ctx.Err()
}

func TestClaimWrites_ALossWhileARoundWriteRunsCancelsTheContextOfTheWrite(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	hold := holdCut{got: make(chan context.Context, 1)}
	h.rounds = hold
	engine := launchedWith(t, h, func(*Deps) {})
	ended := make(chan error, 1)
	file := writeFlow(t, bodyFlow(`round_cut(old = "round-i", new = "round-ii", pr = 9, trunk = "main", name = "rc")`))

	go func() {
		_, err := engine.RunFile(testItem, testProject, file, modeOff())
		ended <- err
	}()

	var writeCtx context.Context

	select {
	case writeCtx = <-hold.got:
	case err := <-ended:
		require.Fail(t, "the run ended before the cut", "%v", err)
	case <-time.After(10 * time.Second):
		require.Fail(t, "the cut never started")
	}

	loseClaim(engine, runID(testItem, 1), "a test lost the claim")

	select {
	case <-writeCtx.Done():
	case <-time.After(10 * time.Second):
		require.Fail(t, "the cut kept running after the claim was lost: it runs under a context that the claim does not own")
	}

	require.Error(t, <-ended)
}

func TestClaimWrites_AMergeWhileTheRunHoldsItsClaimLands(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)

	_, err := rig.walk(t, rig.call("1", ""))

	require.NoError(t, err)
	assert.Equal(t, []string{"1"}, rig.merger.landed(), "control: with the claim kept, the same call lands the member")
}

func TestClaimWrites_AHandOffThatLostItsClaimMarksNothingReady(t *testing.T) {
	t.Parallel()

	rig := newHandRig(t)
	rig.script("1", rig.view("1", "OPEN", true), rig.view("1", "OPEN", false), rig.view("1", "MERGED", false))

	loser := &claimLoser{}
	rig.host.pr.onRead = func(string, int) { loser.lose() }

	engine := rig.engine(t)
	loser.engine = engine

	_, err := engine.RunFile(testItem, testProject, writeFlow(t, bodyFlow(rig.body("1", ""))), modeOff())

	require.ErrorIs(t, err, errClaimLost)
	assert.NotContains(t, rig.host.pr.marked(), "1:false", "the claim was lost before the ready mark, so the pull request is never marked ready (a return to draft is no ready mark)")
}

func TestClaimWrites_APushAfterTheLossPushesNothing(t *testing.T) {
	t.Parallel()

	push := stepWrite{"push", `push(name = "p", branch = "fix-7", head = "aaa")`, func(h *harness, _ *fakeWriter) []string { return h.forge.pushed }}

	written, err := writeAfter(t, push, true)

	require.ErrorIs(t, err, errClaimLost)
	assert.Empty(t, written, "the claim was lost before the push, so the forge pushes nothing")

	written, err = writeAfter(t, push, false)

	require.NoError(t, err)
	assert.Len(t, written, 1, "control: with the claim kept, the same push reaches the forge")
}

// draftMarks is a batch host that records each change of the draft flag as item:draft.
type draftMarks struct {
	batchHost

	mu    sync.Mutex
	marks []string
}

func (d *draftMarks) SetDraft(_ context.Context, _, item, _ string, draft bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.marks = append(d.marks, item+":"+strconv.FormatBool(draft))

	return nil
}

// handleWrite is one method of the write handle, and the count of the writes that the fakes recorded for it.
type handleWrite struct {
	name    string
	write   func(ctx context.Context, w runWrites) error
	written func() int
}

func handleWrites(h *harness, writer *fakeWriter, marks *draftMarks) []handleWrite {
	count := func(mu *sync.Mutex, records *[]string) func() int {
		return func() int {
			mu.Lock()
			defer mu.Unlock()

			return len(*records)
		}
	}
	forgeCount := func(records *[]string) func() int { return count(&h.forge.mu, records) }

	return []handleWrite{
		{"pr_comment", func(ctx context.Context, w runWrites) error {
			_, err := w.PRComment(ctx, testProject, testItem, "k", "x")

			return err
		}, forgeCount(&h.forge.notes)},
		{"pr_open", func(ctx context.Context, w runWrites) error {
			_, err := w.Open(ctx, testProject, testItem, OpenRequest{Title: "t", Base: "main"})

			return err
		}, forgeCount(&h.forge.opened)},
		{"push", func(ctx context.Context, w runWrites) error {
			return w.Push(ctx, testProject, testItem, "fix-7", "aaa", "")
		}, forgeCount(&h.forge.pushed)},
		{"ready", func(ctx context.Context, w runWrites) error { return w.MarkReady(ctx, testProject, testItem, "") }, count(&marks.mu, &marks.marks)},
		{"item_create", func(ctx context.Context, w runWrites) error {
			_, _, err := w.Create(ctx, testProject, "k", trackerstore.NewItem{Title: "t"})

			return err
		}, count(&writer.mu, &writer.created)},
		{"item_status", func(ctx context.Context, w runWrites) error {
			return w.SetStatus(ctx, testProject, "n-1", trackerstore.StatusClosed)
		}, count(&writer.mu, &writer.statuses)},
		{"item_comment", func(ctx context.Context, w runWrites) error { return w.ItemComment(ctx, testProject, "n-1", "k", "x") }, count(&writer.mu, &writer.notes)},
		{"program", func(ctx context.Context, w runWrites) error {
			_, err := w.Run(ctx, CommandRequest{Project: testProject, Item: testItem, Run: []string{markCommand}})

			return err
		}, func() int { return len(h.commands.ran()) }},
	}
}

func TestClaimWrites_AWriteAfterTheLeaseLapsedWithNoTickIsRefused(t *testing.T) {
	t.Parallel()

	for name, lapsed := range map[string]bool{"the_claim_is_trusted": false, "the_lease_lapsed": true} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			lapsedWrites(t, lapsed)
		})
	}
}

// lapsedWrites calls each method of the write handle of a run that holds a trusted claim, after its lease lapsed when lapsed is true.
func lapsedWrites(t *testing.T, lapsed bool) {
	t.Helper()

	h := newHarness(t)
	writer := newFakeWriter()
	h.writer = writer
	marks := &draftMarks{}
	clk := &leaseClock{now: time.Unix(1_000_000, 0)}
	engine := launchedWith(t, h, func(deps *Deps) {
		clk.install(&deps.Clock)
		deps.Batch = marks
	})

	// A run that holds a trusted claim. No tick runs in the test, so nothing calls lose and the context of the claim stays live.
	const run = "lapse-run"

	held := holdClaim(engine, run, claimHold{project: testProject, owner: "owner"})
	require.True(t, engine.holds.renew(held, clk.Now(), func() {}))

	claim, err := engine.claimed(run, testItem)
	require.NoError(t, err)

	if lapsed {
		clk.advance(trustLimit)
	}

	for _, write := range handleWrites(h, writer, marks) {
		err := write.write(t.Context(), runWrites{e: engine, run: run, item: testItem})

		if !lapsed {
			require.NoError(t, err, write.name)
			assert.Equal(t, 1, write.written(), "control: %s writes once while the claim is trusted", write.name)

			continue
		}

		require.ErrorIs(t, err, errClaimLost, write.name)
		assert.Zero(t, write.written(), "%s writes nothing once the lease lapsed", write.name)
		require.NoError(t, claim.Err(), "the context of the claim is still live: only the trust read sees the lapse")
	}
}

func TestClaimWrites_TheLostClaimOfAMergeChildNamesTheItemOfTheRun(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	run := startHeldRun(t, rig.h, rig.deps())

	loseClaim(run.engine, runID(testItem, 1), "a test lost the claim")
	failure := queueMerge(t, run.engine, runID(testItem, 1), "1", rig.heads["1"])
	require.Error(t, run.finish())

	require.NotNil(t, failure)
	assert.Contains(t, failure.Text, "item "+testItem+":", "the claim is on the item of the run, not on the member")
	assert.Contains(t, failure.Text, "Run beehest run "+testItem+" again")
	assert.NotContains(t, failure.Text, "item 1:", "the member holds no claim, so the advice names the item of the run")
}

func TestClaimWrites_AQueuedMergeAndASlotCommandRecordTheRunAndTheItemOfTheRun(t *testing.T) {
	t.Parallel()

	t.Run("the_merge_of_member_1", func(t *testing.T) {
		t.Parallel()

		rig := newBatchRig(t)
		engine := rig.engine(t)

		_, err := engine.RunFile(testItem, testProject, writeFlow(t, bodyFlow(rig.call("1", ""))), modeOff())
		require.NoError(t, err)

		assert.Equal(t, []string{runID(testItem, 1) + " " + testItem}, recordedRuns(t, engine, kindMerge), "the merge names its run and the item of that run, not the member")
	})

	t.Run("the_slot_command", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		h.slots = func(string, string, string) (SlotGrant, error) { return SlotGrant{Host: "box", Count: 1}, nil }
		engine := h.engine(t)

		_, err := engine.RunFile(testItem, testProject, writeFlow(t, bodyFlow(`command(name = "mutate", run = ["x"], host = "build", slots = "mutation")`)), modeOff())
		require.NoError(t, err)

		assert.Equal(t, []string{runID(testItem, 1) + " " + testItem}, recordedRuns(t, engine, kindCommand), "the host job names its run and the item of that run")
	})
}

// recordedRuns lists the run and the run item that each recorded exclusive call of one kind names, as "run item".
func recordedRuns(t *testing.T, engine *Engine, kind string) []string {
	t.Helper()

	var runs []string

	for _, name := range []string{exclusiveName, hostJobName} {
		rows, err := dbos.ListWorkflows(engine.ctx, dbos.WithFilterName(name), dbos.WithFilterLoadInput(true))
		require.NoError(t, err)

		for _, row := range rows {
			var in ExclusiveInput

			text, isText := row.Input.(string)
			require.True(t, isText)
			require.NoError(t, json.Unmarshal([]byte(text), &in))

			if in.Kind == kind {
				runs = append(runs, in.Run+" "+in.RunItem)
			}
		}
	}

	return runs
}
