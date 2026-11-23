package flow

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
)

// fakeQueue is the open work of a project: the items of the tracker and the open pull requests of the forge.
// afterFirstRead runs once, after the first read of the items, so a test can change the tracker between two reads.
type fakeQueue struct {
	mu             sync.Mutex
	items          []model.Item
	pulls          []model.PullRequest
	itemsErr       error
	pullsErr       error
	afterFirstRead func(q *fakeQueue)
	reads          int
	states         map[int]string
	stateErr       error
	stateAsked     []int
}

func (q *fakeQueue) OpenItems(context.Context, string) ([]model.Item, error) {
	q.mu.Lock()
	q.reads++
	first := q.reads == 1
	out, err := slices.Clone(q.items), q.itemsErr
	q.mu.Unlock()

	if first && q.afterFirstRead != nil {
		q.afterFirstRead(q)
	}

	return out, err
}

func (q *fakeQueue) OpenPulls(context.Context, string) ([]model.PullRequest, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	return slices.Clone(q.pulls), q.pullsErr
}

// PullState answers with the state that the test set for the pull request number, and records each number it was asked about.
func (q *fakeQueue) PullState(_ context.Context, _ string, pr int) (string, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	q.stateAsked = append(q.stateAsked, pr)

	return q.states[pr], q.stateErr
}

// hold sets the hold of one item in the tracker.
func (q *fakeQueue) hold(id string, on bool) {
	q.mu.Lock()
	defer q.mu.Unlock()

	for i := range q.items {
		if q.items[i].ID == id {
			q.items[i].Hold = on
		}
	}
}

func queueItem(id string, severity model.Severity) model.Item {
	return model.Item{ID: id, Severity: severity}
}

// queuePull is an open pull request on a branch, with the named labels.
func queuePull(t *testing.T, number int, branch string, labels ...string) model.PullRequest {
	t.Helper()

	named := make([]map[string]string, len(labels))
	for i, name := range labels {
		named[i] = map[string]string{"name": name}
	}

	text, err := json.Marshal(map[string]any{"number": number, "headRefName": branch, "labels": named})
	require.NoError(t, err)

	var pull model.PullRequest
	require.NoError(t, json.Unmarshal(text, &pull))

	return pull
}

// seenLine is a command that prints the rows and the free slots of the queue q, one word for each row.
const seenLine = `    command(name = "seen", run = ["seen"] + [r.item + ":" + r.state + ":" + r.severity + ":" + str(r.pr) for r in q.rows] + ["free=" + str(q.free)])
`

func queueFlow(arguments string) string {
	return "def main(item):\n    q = queue(" + arguments + ")\n" + seenLine
}

// seenBy returns the words of the one seen command of the parent.
func seenBy(t *testing.T, h *harness) []string {
	t.Helper()

	ran := h.commands.ranBy(testItem)
	require.Len(t, ran, 1)

	return strings.Fields(ran[0])[1:]
}

func queueHarness(t *testing.T, items []model.Item, pulls ...model.PullRequest) (*harness, *fakeQueue) {
	t.Helper()

	h := newHarness(t)
	q := &fakeQueue{items: items, pulls: pulls}
	h.queue = q

	return h, q
}

func TestQueue_AnItemOnHoldIsNeverReady(t *testing.T) {
	t.Parallel()

	held := queueItem("21", model.SeverityP1)
	held.Hold = true

	h, _ := queueHarness(t, []model.Item{held, queueItem("22", model.SeverityP1), queueItem("23", model.SeverityP1)}, queuePull(t, 8, "22", "hold"))

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, queueFlow(`name = "q", hold = "hold"`)), modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{"21:hold:P1:0", "22:hold:P1:8", "23:ready:P1:0", "free=2"}, seenBy(t, h), "a tracker hold and a hold label on the pull request both hold the item")
}

func TestQueue_AHoldSetAfterTheQueueCallStopsTheRunAtItsStart(t *testing.T) {
	t.Parallel()

	h, q := queueHarness(t, []model.Item{queueItem("21", model.SeverityP1)})
	q.afterFirstRead = func(q *fakeQueue) { q.hold("21", true) }

	file := `def work(item):
    command(name = "w", run = ["w"])

def main(item):
    q = queue(name = "q")
    command(name = "seen", run = ["seen", q.rows[0].state])
    done = gather(spawn(items = [q.rows[0].item], entry = "work", name = "s"), name = "g")
    command(name = "after", run = ["after", "stopped" if done.results[0].stopped != None else "ran"])
`

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, file), modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{"seen ready", "after stopped"}, h.commands.ranBy(testItem), "the row was ready when the call read it")
	assert.Empty(t, h.commands.ranBy("21"), "the held item started no step")
}

// blockedChild is a workflow file whose child 21 waits until the parent runs its seen command, so the run of 21 is open while queue reads.
const blockedChild = `def work(item):
    command(name = "w", run = ["w"])

def main(item):
    runs = spawn(items = ["21"], entry = "work", name = "s")
    q = queue(name = "q")
    command(name = "seen", run = ["seen"] + [r.item + ":" + r.state + ":" + r.severity + ":" + str(r.pr) for r in q.rows] + ["free=" + str(q.free)])
    gather(runs, name = "g")
`

// holdChildUntilSeen blocks the commands of item 21 until the parent starts its seen command.
func holdChildUntilSeen(h *harness) {
	release := make(chan struct{})

	var once sync.Once

	h.commands.before = func(ctx context.Context, request CommandRequest) error {
		if request.Item == testItem && request.Run[0] == "seen" {
			once.Do(func() { close(release) })

			return nil
		}

		if request.Item == "21" {
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		return nil
	}
}

func TestQueue_AnItemWithAnOpenRunIsRunning(t *testing.T) {
	t.Parallel()

	h, _ := queueHarness(t, []model.Item{queueItem("21", model.SeverityP1)})
	holdChildUntilSeen(h)

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, blockedChild), modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{"21:running:P1:0", "free=1"}, seenBy(t, h))
}

func TestQueue_FreeFallsWhenARunIsEnqueued(t *testing.T) {
	t.Parallel()

	h, _ := queueHarness(t, []model.Item{queueItem("21", model.SeverityP1)})
	holdChildUntilSeen(h)

	file := `def work(item):
    command(name = "w", run = ["w"])

def main(item):
    before = queue(name = "before")
    runs = spawn(items = ["21"], entry = "work", name = "s")
    after = queue(name = "after")
    command(name = "seen", run = ["seen", str(before.free), str(after.free)])
    gather(runs, name = "g")
`

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, file), modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{"2", "1"}, seenBy(t, h), "max_concurrent is 2, and one run is open after the spawn")
}

// endedChild runs a child, waits for it, and then reads the queue.
const endedChild = `def work(item):
    command(name = "w", run = ["w"])

def main(item):
    gather(spawn(items = ["21"], entry = "work", name = "s"), name = "g")
    q = queue(name = "q")
` + seenLine

func TestQueue_AFailedLastAttemptIsFailed(t *testing.T) {
	t.Parallel()

	h, _ := queueHarness(t, []model.Item{queueItem("21", model.SeverityP1)})
	h.commands.before = func(_ context.Context, request CommandRequest) error {
		if request.Item == "21" {
			return errFake
		}

		return nil
	}

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, endedChild), modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{"21:failed:P1:0", "free=2"}, seenBy(t, h))
}

func TestQueue_ASucceededItemIsDone(t *testing.T) {
	t.Parallel()

	h, _ := queueHarness(t, []model.Item{queueItem("21", model.SeverityP1)})

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, endedChild), modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{"21:done:P1:0", "free=2"}, seenBy(t, h))
}

// foreignChild runs a child that ends as a foreign review does: with the reason, the recorded pull request and the recorded findings. Then it reads the queue.
func foreignChild(reason string, pr int, findings string) string {
	return `def work(item):
    command(name = "w", run = ["w"])
    return {"reason": "` + reason + `", "pr": ` + strconv.Itoa(pr) + `, "findings": ` + findings + `}

def main(item):
    gather(spawn(items = ["21"], entry = "work", name = "s"), name = "g")
    q = queue(name = "q")
` + seenLine
}

const openFinding = `[{"id": "f-1", "severity": "blocking", "path": "a.go", "text": "x"}]`

func TestQueue_AForeignRowWhosePullRequestMergedWithFindingsOpenIsOfferedAgain(t *testing.T) {
	t.Parallel()

	h, q := queueHarness(t, []model.Item{queueItem("21", model.SeverityP1)})
	q.states = map[int]string{7: "MERGED"}

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, foreignChild("foreign", 7, openFinding)), modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{"21:ready:P1:0", "free=2"}, seenBy(t, h), "a merged pull request with findings open brings the row back")
	assert.Equal(t, []int{7}, q.stateAsked, "the state is read for the recorded pull request number")
}

func TestQueue_AForeignRowThatIsNotOwedAnotherVisitStaysDone(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		reason, findings, state string
	}{
		"the pull request is still open":        {"foreign", openFinding, "OPEN"},
		"the pull request closed unmerged":      {"foreign", openFinding, "CLOSED"},
		"the findings are closed":               {"foreign", "[]", "MERGED"},
		"the last run ended for another reason": {"other", openFinding, "MERGED"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h, q := queueHarness(t, []model.Item{queueItem("21", model.SeverityP1)})
			q.states = map[int]string{7: test.state}

			_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, foreignChild(test.reason, 7, test.findings)), modeOff())

			require.NoError(t, err)
			assert.Equal(t, []string{"21:done:P1:0", "free=2"}, seenBy(t, h))
		})
	}
}

func TestQueue_AForeignRowThatRecordedNoPullRequestStaysDoneAndReadsNoState(t *testing.T) {
	t.Parallel()

	h, q := queueHarness(t, []model.Item{queueItem("21", model.SeverityP1)})
	q.states = map[int]string{0: "MERGED"}

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, foreignChild("foreign", 0, openFinding)), modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{"21:done:P1:0", "free=2"}, seenBy(t, h))
	assert.Empty(t, q.stateAsked, "a record with no pull request number reads no pull request state")
}

func TestQueue_AFailedReadOfTheRecordedPullRequestFailsTheStepAndReturnsNoGuess(t *testing.T) {
	t.Parallel()

	h, q := queueHarness(t, []model.Item{queueItem("21", model.SeverityP1)})
	q.stateErr = errFake

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, foreignChild("foreign", 7, openFinding)), modeOff())

	require.ErrorIs(t, err, errFake)
	assert.Empty(t, h.commands.ranBy(testItem), "no row reached the file")
}

func TestQueue_RowsSortBySeverityThenByItemID(t *testing.T) {
	t.Parallel()

	h, _ := queueHarness(t, []model.Item{
		queueItem("30", model.SeverityP2), queueItem("7", model.SeverityUnknown), queueItem("5", model.SeverityP1),
		queueItem("9", model.SeverityP0), queueItem("11", model.SeverityP1),
	})

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, queueFlow(`name = "q"`)), modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{"9:ready:P0:0", "11:ready:P1:0", "5:ready:P1:0", "30:ready:P2:0", "7:ready:P?:0", "free=2"}, seenBy(t, h),
		"P0 first, P? last, and an equal severity by the bytes of the item id")
}

func TestQueue_AFailedReadFailsTheStepAndReturnsNoGuess(t *testing.T) {
	t.Parallel()

	for name, set := range map[string]func(*fakeQueue){
		"tracker": func(q *fakeQueue) { q.itemsErr = errFake },
		"forge":   func(q *fakeQueue) { q.pullsErr = errFake },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h, q := queueHarness(t, []model.Item{queueItem("21", model.SeverityP1)})
			set(q)

			_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, queueFlow(`name = "q"`)), modeOff())

			require.ErrorIs(t, err, errFake)
			assert.Empty(t, h.commands.ranBy(testItem), "no row reached the file")
		})
	}
}

func TestQueue_APullRequestJoinsItsItemByBranchName(t *testing.T) {
	t.Parallel()

	h, _ := queueHarness(t, []model.Item{queueItem("21", model.SeverityP1), queueItem("22", model.SeverityP1)}, queuePull(t, 7, "21"), queuePull(t, 8, "feature-x"))

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, queueFlow(`name = "q"`)), modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{"21:ready:P1:7", "22:ready:P1:0", "free=2"}, seenBy(t, h))
}

func TestQueue_ABuildWithNoQueueReaderFailsTheCall(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, queueFlow(`name = "q"`)), modeOff())

	require.ErrorIs(t, err, errNoQueue)
}

func TestQueue_AReplayReturnsTheRecordedRowsNotAFreshRead(t *testing.T) {
	t.Parallel()

	file := writeFlow(t, `def main(item):
    q = queue(name = "q")
    answer = wait(name = "ask", question = "go?").answer
    command(name = "seen", run = ["seen"] + [r.item + ":" + r.state for r in q.rows])
`)
	h, q := queueHarness(t, []model.Item{queueItem("21", model.SeverityP1)})
	interrupted(t, h, file)

	q.hold("21", true)
	q.mu.Lock()
	q.items = append(q.items, queueItem("22", model.SeverityP1))
	q.mu.Unlock()

	next := h.engine(t)
	client := h.client(t, testVersion)
	done := make(chan runOutcome, 1)

	go func() {
		result, err := next.RunFile(testItem, testProject, file, modeOff())
		done <- runOutcome{result: result, err: err}
	}()

	waitOn(t, waiting[struct{}]{what: "the run to take the answer", run: runOfClient(t, client), read: func() (struct{}, bool, error) {
		return struct{}{}, client.Reply(testItem, Reply{Text: "yes"}) == nil, nil
	}})

	require.NoError(t, receive(t, done).err)
	assert.Equal(t, []string{"seen 21:ready"}, h.commands.ran(), "the rows recorded before the stop come back, whatever the tracker says now")
}

func TestQueue_JoinPullsSkipsABranchlessPullKeepsTheLowestNumberAndMatchesLabelsByName(t *testing.T) {
	t.Parallel()

	joined := joinPulls([]model.PullRequest{
		{Number: 3},
		queuePull(t, 9, "21", "other"),
		queuePull(t, 5, "21", "other"),
		queuePull(t, 6, "21", "hold"),
	}, "hold")

	assert.Equal(t, map[string]joinedPull{"21": {number: 5, held: true}}, joined, "the lowest number counts, one hold label holds, and a pull with no branch joins nothing")
	assert.False(t, joinPulls([]model.PullRequest{queuePull(t, 9, "21", "other")}, "hold")["21"].held)
}

func TestQueue_AStoreThatIsClosedFailsTheReadsOfTheRunsAndTheSlots(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.queue = &fakeQueue{items: []model.Item{queueItem("21", model.SeverityP1)}}
	engine, err := NewEngine(t.Context(), h.opts, h.deps())
	require.NoError(t, err)
	require.NoError(t, engine.Shutdown())

	_, err = engine.readQueue(t.Context(), testProject, "", "")
	require.ErrorContains(t, err, "count the open runs", "the slots are read before the rows, so the slots fail first")

	_, err = engine.freeSlots()
	require.ErrorContains(t, err, "count the open runs")

	_, err = engine.queueRows(t.Context(), []model.Item{queueItem("21", model.SeverityP1)}, nil)
	require.ErrorContains(t, err, "list the runs of item 21", "the run of an item cannot be read")

	_, err = engine.queueRows(t.Context(), nil, nil)
	require.NoError(t, err, "no item has no run to read")
}

func TestQueue_AFailedReadOfTheHoldAtStartStopsTheRunWithTheError(t *testing.T) {
	t.Parallel()

	h, q := queueHarness(t, []model.Item{queueItem("21", model.SeverityP1)})
	q.afterFirstRead = func(q *fakeQueue) {
		q.mu.Lock()
		defer q.mu.Unlock()

		q.itemsErr = errFake
	}

	file := `def work(item):
    command(name = "w", run = ["w"])

def main(item):
    q = queue(name = "q")
    done = gather(spawn(items = [q.rows[0].item], entry = "work", name = "s"), name = "g")
    command(name = "after", run = ["after", str(done.results[0].error)])
`

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, file), modeOff())

	require.NoError(t, err)
	assert.Empty(t, h.commands.ranBy("21"), "the item started no step")
	assert.Contains(t, h.commands.ranBy(testItem)[0], "read the hold of item 21")
}
