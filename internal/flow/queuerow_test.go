package flow

import (
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
)

// rowFieldsFlow prints the container, the author and the change request of each row.
const rowFieldsFlow = `def main(item):
    q = queue(name = "q")
    command(name = "seen", run = ["seen"] + [r.item + ":" + r.parent + ":" + r.author + ":" + str(r.changes_requested) for r in q.rows])
`

func TestQueue_ARowCarriesItsContainerAndTheAuthorAndReviewStateOfItsPullRequest(t *testing.T) {
	t.Parallel()

	child := queueItem("21", model.SeverityP1)
	child.Parent = "9"

	h, _ := queueHarness(t,
		[]model.Item{child, queueItem("22", model.SeverityP1), queueItem("23", model.SeverityP1)},
		pullBy(t, 5, "22", "someone", "REVIEW_REQUIRED"), pullBy(t, 6, "23", "owner", "CHANGES_REQUESTED"),
	)

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, rowFieldsFlow))

	require.NoError(t, err)
	assert.Equal(t, []string{"21:9::False", "22::someone:False", "23::owner:True"}, seenBy(t, h),
		"the parent comes from the item, the author and the change request from its pull request")
}

// pullsFlow prints the labels of each row and each open pull request of the queue, field by field.
const pullsFlow = `def main(item):
    q = queue(name = "q", label = "tracked")
    rows = [r.item + "=" + ",".join(r.labels) for r in q.rows]
    pulls = ["%d|%s|%s|%s|%s|%s|%s" % (p.number, p.branch, p.base, p.author, p.draft, p.title, p.item) for p in q.pulls]
    command(name = "seen", run = ["seen"] + rows + pulls)
`

// openPullOf is an open pull request with every field that the queue keeps of it.
func openPullOf(t *testing.T, number int, branch, base, author, title string, draft bool) model.PullRequest {
	t.Helper()

	text, err := json.Marshal(map[string]any{"number": number, "headRefName": branch, "baseRefName": base, "author": map[string]string{"login": author}, "title": title, "isDraft": draft})
	require.NoError(t, err)

	var pull model.PullRequest
	require.NoError(t, json.Unmarshal(text, &pull))

	return pull
}

func TestQueue_TheResultHoldsTheOpenPullRequestsAndTheLabelsOfEachRow(t *testing.T) {
	t.Parallel()

	tracked := queueItem("22", model.SeverityP1)
	tracked.Labels = []string{"tracked", "blocked"}
	untracked := queueItem("23", model.SeverityP1)
	untracked.Labels = []string{"other"}

	h, _ := queueHarness(t,
		[]model.Item{tracked, untracked},
		openPullOf(t, 5, "22", "main", "someone", "fix-22", false),
		openPullOf(t, 6, "23", "main", "owner", "fix-23", true),
		openPullOf(t, 7, "loose", "dev", "owner", "no-issue", true),
	)

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, pullsFlow))

	require.NoError(t, err)
	assert.Equal(t, []string{
		"22=tracked,blocked",
		"5|22|main|someone|False|fix-22|22",
		"6|23|main|owner|True|fix-23|23",
		"7|loose|dev|owner|True|no-issue|",
	}, seenBy(t, h), "each row carries its tracker labels; every open pull request comes back, with the open item its branch joins, also one the label leaves out of the rows")
}

func TestQueue_ARowTakesTheAuthorOfTheLowestPullRequestOfItsBranch(t *testing.T) {
	t.Parallel()

	h, _ := queueHarness(t,
		[]model.Item{queueItem("24", model.SeverityP1)},
		pullBy(t, 9, "24", "later", "CHANGES_REQUESTED"), pullBy(t, 4, "24", "first", ""),
	)

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, rowFieldsFlow))

	require.NoError(t, err)
	assert.Equal(t, []string{"24::first:False"}, seenBy(t, h), "the lowest number counts, for the author and for the review state")
}

// stoppedChild runs a child whose command ends it with a stop code, then prints the row of each item. The word ROW stands for the expression of the test.
const stoppedChild = `def work(item):
    command(name = "lint", run = ["lint"], stop_on_rc = [2])

def main(item):
    gather(spawn(items = ["21"], entry = "work", name = "s"), name = "g")
    q = queue(name = "q")
    command(name = "seen", run = ["seen"] + [ROW for r in q.rows])
`

// stopAtLint makes the command lint of item 21 print the reason and end with the stop code.
func stopAtLint(h *harness) { stopAtLintWith(h, "base-moved") }

// stopAtLintWith makes the command lint of item 21 print the output and end with the stop code.
func stopAtLintWith(h *harness, output string) {
	h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		if request.Item == "21" {
			return CommandOutput{Stdout: output, RC: 2}, nil
		}

		return CommandOutput{}, nil
	}
}

func TestQueue_AStoppedRowKeepsOneLineOfTheStopOutputUpToTheLimit(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct{ output, want string }{
		"output of many lines is one line":         {"line-one\n  line-two\n", "line-one_line-two"},
		"output at the limit is whole":             {strings.Repeat("a", StoppedOutputLimit), strings.Repeat("a", StoppedOutputLimit)},
		"output over the limit is cut and marked":  {strings.Repeat("a", StoppedOutputLimit+1), strings.Repeat("a", StoppedOutputLimit) + "…"},
		"a cut counts characters and not bytes":    {strings.Repeat("é", StoppedOutputLimit+5), strings.Repeat("é", StoppedOutputLimit) + "…"},
		"output with a period and a bar is intact": {"no.|x.", "no.|x."},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h, _ := queueHarness(t, []model.Item{queueItem("21", model.SeverityP1)})
			stopAtLintWith(h, test.output)

			_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, strings.ReplaceAll(stoppedChild, "ROW", `r.stop_reason.replace(" ", "_")`)))

			require.NoError(t, err)
			assert.Equal(t, []string{test.want}, seenBy(t, h))
		})
	}
}

// stopOrWork is the flow of an item whose command lint stops it while stopping is true and passes after that.
const stopOrWork = `def main(item):
    command(name = "lint", run = ["lint"], stop_on_rc = [2])
`

// rowsFlow reads the queue and prints the state of each row.
const rowsFlow = `def main(item):
    q = queue(name = "q")
    command(name = "seen", run = ["seen"] + [r.item + "|" + r.state for r in q.rows])
`

// rowsSeenBy reads the queue through a run of the item observer and returns the rows it printed.
func rowsSeenBy(t *testing.T, h *harness, engine *Engine, observer string) ([]string, error) {
	t.Helper()

	_, err := engine.RunFile(observer, testProject, writeFlow(t, rowsFlow))

	ran := h.commands.ranBy(observer)
	if len(ran) == 0 {
		return nil, err
	}

	return strings.Fields(ran[0])[1:], err
}

func TestQueue_ABeehaveRunOfAStoppedItemStartsANewAttemptAndTheRowLeavesStopped(t *testing.T) {
	t.Parallel()

	h, _ := queueHarness(t, []model.Item{queueItem("21", model.SeverityP1)})

	var stopping atomic.Bool

	stopping.Store(true)

	h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		if request.Item == "21" && stopping.Load() {
			return CommandOutput{Stdout: "base-moved", RC: 2}, nil
		}

		return CommandOutput{}, nil
	}

	engine := h.engine(t)
	_, err := engine.RunFile("21", testProject, writeFlow(t, stopOrWork))
	require.NoError(t, err)

	rows, err := rowsSeenBy(t, h, engine, "7")
	require.NoError(t, err)
	assert.Equal(t, []string{"21|stopped"}, rows)

	cancel := h.client(t, testVersion).Cancel("21")
	require.ErrorContains(t, cancel, "item-21-r1 is stopped and has nothing to cancel")
	require.ErrorContains(t, cancel, "run beehave run 21 to start a new attempt")
	require.ErrorContains(t, h.client(t, testVersion).CancelRun(runID("21", 1)), "is stopped and has nothing to cancel. Fix the cause, then run beehave run 21")

	stopping.Store(false)

	_, err = engine.RunFile("21", testProject, writeFlow(t, stopOrWork))
	require.NoError(t, err)

	rows, err = rowsSeenBy(t, h, engine, "8")
	require.NoError(t, err)
	assert.Equal(t, []string{"21|done"}, rows, "the second run is the last run of the item, and it ended without a stop")
}

func TestQueue_ABeehaveRunOfAnItemWhoseResultCannotBeReadReplacesThatRun(t *testing.T) {
	t.Parallel()

	h, _ := queueHarness(t, []model.Item{queueItem("21", model.SeverityP1)})
	engine := h.engine(t)

	_, err := engine.RunFile("21", testProject, writeFlow(t, stopOrWork))
	require.NoError(t, err)
	storeExec(t, h, `UPDATE workflow_output SET output = 'not json' WHERE workflow_uuid = '`+runID("21", 1)+`'`)

	_, err = rowsSeenBy(t, h, engine, "7")
	require.ErrorContains(t, err, "read the result of "+runID("21", 1))
	require.ErrorContains(t, err, "Run beehave run 21 to start a new attempt")

	_, err = engine.RunFile("21", testProject, writeFlow(t, stopOrWork))
	require.NoError(t, err)

	rows, err := rowsSeenBy(t, h, engine, "8")
	require.NoError(t, err)
	assert.Equal(t, []string{"21|done"}, rows, "the queue reads only the newest run of an item, so the new run replaces the unreadable one")
}

func TestQueue_ARunThatEndedWithAStopIsNotDone(t *testing.T) {
	t.Parallel()

	h, _ := queueHarness(t, []model.Item{queueItem("21", model.SeverityP1)})
	stopAtLint(h)

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, strings.ReplaceAll(stoppedChild, "ROW", `r.item + "|" + r.state`)))

	require.NoError(t, err)
	assert.Equal(t, []string{"21|stopped"}, seenBy(t, h), "a run whose result records a stop is stopped in the queue, as status shows it")
}

func TestQueue_AStoppedRowNamesTheStepAndTheReasonOfTheStop(t *testing.T) {
	t.Parallel()

	h, _ := queueHarness(t, []model.Item{queueItem("21", model.SeverityP1), queueItem("22", model.SeverityP1)})
	stopAtLint(h)

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, strings.ReplaceAll(stoppedChild, "ROW", `r.item + "|" + r.stop_step + "|" + r.stop_reason`)))

	require.NoError(t, err)
	assert.Equal(t, []string{"21|command:lint#1|base-moved", "22||"}, seenBy(t, h), "the stopped row names the step and the output of the stop; a row that is not stopped names neither")
}
