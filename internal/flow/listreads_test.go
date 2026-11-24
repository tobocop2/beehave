package flow

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
)

// listFlow calls one typed read and prints one word for each element of its list. The word ROW stands for the expression of the test.
func listFlow(call, list, row string) string {
	return "def main(item):\n    got = " + call + "\n    command(name = \"seen\", run = [\"seen\"] + [" + row + " for r in got." + list + "])\n"
}

func TestItemList_ReturnsTheOpenItemsWithTheirFields(t *testing.T) {
	t.Parallel()

	one := queueItem("21", model.SeverityP1)
	one.Hold, one.Parent, one.Assignee, one.Title, one.Labels = true, "20", "ann", "fix_it", []string{"bug", "ui"}

	h, _ := queueHarness(t, []model.Item{one, queueItem("22", model.SeverityP2)})
	row := `r.item + "|" + r.severity + "|" + str(r.hold) + "|" + r.parent + "|" + r.assignee + "|" + r.title + "|" + ",".join(r.labels)`

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, listFlow(`item_list(name = "l")`, "items", row)), modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{"21|P1|True|20|ann|fix_it|bug,ui", "22|P2|False||||"}, seenBy(t, h))
}

func TestItemList_WithALabelReturnsOnlyTheItemsOfThatLabel(t *testing.T) {
	t.Parallel()

	tagged := queueItem("21", model.SeverityP1)
	tagged.Labels = []string{"bug"}

	h, q := queueHarness(t, []model.Item{tagged, queueItem("22", model.SeverityP1)})

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, listFlow(`item_list(label = "bug", name = "l")`, "items", "r.item")), modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{"21"}, seenBy(t, h))
	assert.Equal(t, []string{"bug"}, q.labelsAsked, "the tracker gets the label")
}

func TestItemList_AFailedReadFailsTheStep(t *testing.T) {
	t.Parallel()

	h, q := queueHarness(t, nil)
	q.itemsErr = errFake

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, listFlow(`item_list(name = "l")`, "items", "r.item")), modeOff())

	require.ErrorIs(t, err, errFake)
	assert.Empty(t, h.commands.ranBy(testItem))
}

func TestItemList_ABuildWithNoQueueReaderFailsTheCall(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, listFlow(`item_list(name = "l")`, "items", "r.item")), modeOff())

	require.ErrorIs(t, err, errNoItemList)
}

func TestItemList_ABadKeywordFailsTheCall(t *testing.T) {
	t.Parallel()

	h, _ := queueHarness(t, nil)

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, "def main(item):\n    n = 3\n    item_list(label = n)\n"), modeOff())

	require.Error(t, err)
}

func TestPrList_ReturnsTheOpenPullRequestsWithTheirFields(t *testing.T) {
	t.Parallel()

	h, _ := queueHarness(t, nil, queuePull(t, 7, "21", "hold"), queuePull(t, 8, "feature-x"))
	row := `str(r.number) + "|" + r.branch + "|" + r.base + "|" + r.author + "|" + str(r.draft) + "|" + r.title + "|" + ",".join(r.labels) + "|" + r.review_decision`

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, listFlow(`pr_list(name = "p")`, "pulls", row)), modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{"7|21|||False||hold|", "8|feature-x|||False|||"}, seenBy(t, h))
}

func TestPrList_WithABaseAsksTheForgeForThatBase(t *testing.T) {
	t.Parallel()

	h, q := queueHarness(t, nil, queuePull(t, 7, "21"))

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, listFlow(`pr_list(base = "main", name = "p")`, "pulls", "r.number")), modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{"main"}, q.basesAsked)
	assert.Empty(t, seenBy(t, h), "the fake pull request has no base, so none is on main")
}

func TestPrList_AFailedReadFailsTheStep(t *testing.T) {
	t.Parallel()

	h, q := queueHarness(t, nil)
	q.pullsErr = errFake

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, listFlow(`pr_list(name = "p")`, "pulls", "r.number")), modeOff())

	require.ErrorIs(t, err, errFake)
}

func TestPrList_ABuildWithNoQueueReaderFailsTheCall(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, listFlow(`pr_list(name = "p")`, "pulls", "r.number")), modeOff())

	require.ErrorIs(t, err, errNoPrList)
}

func TestPrList_ABadKeywordFailsTheCall(t *testing.T) {
	t.Parallel()

	h, _ := queueHarness(t, nil)

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, "def main(item):\n    n = 3\n    pr_list(base = n)\n"), modeOff())

	require.Error(t, err)
}

// runReadChild runs a child, waits for it, and prints the state of each listed item and the free slots.
const runReadChild = `def work(item):
    command(name = "w", run = ["w"])

def main(item):
    gather(spawn(items = ["21"], entry = "work", name = "s"), name = "g")
    got = run_read(items = ["21", "22"], name = "r")
    command(name = "seen", run = ["seen"] + [r.item + ":" + r.state + ":" + r.stop_step + ":" + r.stop_reason + ":" + str(r.value) for r in got.runs] + ["free=" + str(got.free)])
`

func TestRunRead_ReturnsTheLastRunOfEachItemInTheOrderOfTheCall(t *testing.T) {
	t.Parallel()

	h, _ := queueHarness(t, nil)

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, runReadChild), modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{"21:done:::None", "22:none:::None", "free=2"}, seenBy(t, h), "an item that never ran has the state none")
}

func TestRunRead_AFailedLastRunIsFailed(t *testing.T) {
	t.Parallel()

	h, _ := queueHarness(t, nil)
	h.commands.before = func(_ context.Context, request CommandRequest) error {
		if request.Item == "21" {
			return errFake
		}

		return nil
	}

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, runReadChild), modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{"21:failed:::None", "22:none:::None", "free=2"}, seenBy(t, h))
}

func TestRunRead_AStoppedRunNamesTheStepAndTheReason(t *testing.T) {
	t.Parallel()

	h, _ := queueHarness(t, nil)
	stopAtLint(h)

	file := strings.ReplaceAll(strings.ReplaceAll(runReadChild, `run = ["w"])`, `run = ["lint"], stop_on_rc = [2])`), `command(name = "w", run = ["lint"]`, `command(name = "lint", run = ["lint"]`)

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, file), modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{"21:stopped:command:lint#1:base-moved:None", "22:none:::None", "free=2"}, seenBy(t, h))
}

func TestRunRead_AnOpenRunIsRunningAndTakesASlot(t *testing.T) {
	t.Parallel()

	h, _ := queueHarness(t, nil)
	holdChildUntilSeen(h)

	file := `def work(item):
    command(name = "w", run = ["w"])

def main(item):
    runs = spawn(items = ["21"], entry = "work", name = "s")
    got = run_read(items = ["21"], name = "r")
    command(name = "seen", run = ["seen", got.runs[0].state, str(got.free)])
    gather(runs, name = "g")
`

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, file), modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{"running", "1"}, seenBy(t, h))
}

func TestRunRead_ADoneRunReturnsItsValue(t *testing.T) {
	t.Parallel()

	h, _ := queueHarness(t, nil)

	file := `def work(item):
    return {"answer": 42}

def main(item):
    gather(spawn(items = ["21"], entry = "work", name = "s"), name = "g")
    got = run_read(items = ["21"], name = "r")
    command(name = "seen", run = ["seen", str(got.runs[0].value["answer"])])
`

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, file), modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{"42"}, seenBy(t, h))
}

func TestRunRead_AnIdThatIsNoItemIdFailsTheCall(t *testing.T) {
	t.Parallel()

	h, _ := queueHarness(t, nil)
	file := "def main(item):\n    run_read(items = [\"not an id\"], name = \"r\")\n"

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, file), modeOff())

	require.ErrorIs(t, err, ErrBadItem)
}

func TestRunRead_ItemsThatAreNotAListFailTheCall(t *testing.T) {
	t.Parallel()

	for name, file := range map[string]string{
		"a number":   "def main(item):\n    n = 3\n    run_read(items = n, name = \"r\")\n",
		"no items":   "def main(item):\n    run_read(name = \"r\")\n",
		"a bad name": "def main(item):\n    n = 3\n    run_read(items = [], name = n)\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h, _ := queueHarness(t, nil)

			_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, file), modeOff())

			require.Error(t, err)
		})
	}
}

func TestRunRead_AStoreThatIsClosedFailsTheRead(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	engine, err := NewEngine(t.Context(), h.opts, h.deps())
	require.NoError(t, err)
	require.NoError(t, engine.Shutdown())

	_, _, err = engine.lastRuns([]string{"21"})

	require.ErrorContains(t, err, "count the open runs", "the slots are read first")

	_, err = engine.runOf("21")
	require.ErrorContains(t, err, "list the runs of item 21")
}

func TestRunRead_ARunWhoseResultCannotBeReadFailsTheStep(t *testing.T) {
	t.Parallel()

	h, _ := queueHarness(t, nil)
	engine := h.engine(t)

	_, err := engine.RunFile("21", testProject, writeFlow(t, "def main(item):\n    command(name = \"w\", run = [\"w\"])\n"), modeOff())
	require.NoError(t, err)
	storeExec(t, h, `UPDATE workflow_output SET output = 'not json' WHERE workflow_uuid = '`+runID("21", 1)+`'`)

	_, err = engine.RunFile(testItem, testProject, writeFlow(t, "def main(item):\n    run_read(items = [\"21\"], name = \"r\")\n"), modeOff())

	require.ErrorContains(t, err, "Run beehave run 21 to start a new attempt")
}

func TestRunRead_AValueThatIsNotJSONFailsTheConversion(t *testing.T) {
	t.Parallel()

	_, err := runsReadValue(runsRead{Runs: []runRecord{{Item: "21", State: queueDone, Value: []byte("{")}}})

	require.ErrorContains(t, err, "read the value of the last run of item 21")
}

func TestRunRead_ARunResultThatCannotBeReadFailsClosed(t *testing.T) {
	t.Parallel()

	view, err := settledView("21", succeededRun(`{"item": "21", "project": "`+testProject+`"}`, "not json"))

	require.ErrorContains(t, err, "Run beehave run 21 to start a new attempt")
	assert.Equal(t, runNone, view.State)
}
