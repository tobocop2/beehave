package flow

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/model"
)

func TestItemList_ReturnsTheItemsOfTheLabelAsOneRecordedStep(t *testing.T) {
	t.Parallel()

	tracked := queueItem("21", model.SeverityP1)
	tracked.Labels, tracked.Parent, tracked.Hold = []string{"round"}, "9", true

	h, q := queueHarness(t, []model.Item{tracked, queueItem("22", model.SeverityP2)})

	file := `def main(item):
    got = item_list(label = "round", name = "i")
    command(name = "seen", run = ["seen"] + [i["id"] + ":" + i["severity"] + ":" + i["parent"] + ":" + str(i["hold"]) + ":" + ",".join(i["labels"]) for i in got.items])
`

	engine := h.engine(t)
	_, err := engine.RunFile(testItem, testProject, writeFlow(t, file), modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{"21:P1:9:True:round"}, seenBy(t, h), "the tracker applies the label and the item keeps its fields")
	assert.Equal(t, []string{"round"}, q.labelsAsked)
	assert.Contains(t, strings.Join(stepNames(t, engine), " "), "item_list:i#1")
}

func TestItemList_AnItemWithNoLabelHasAnEmptyListOfLabels(t *testing.T) {
	t.Parallel()

	h, _ := queueHarness(t, []model.Item{queueItem("21", model.SeverityP1)})

	file := `def main(item):
    command(name = "seen", run = ["seen", str(len(item_list().items[0]["labels"]))])
`

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, file), modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{"0"}, seenBy(t, h), "a list with no labels is an empty list and not None")
}

func TestItemList_AFailedReadFailsTheStep(t *testing.T) {
	t.Parallel()

	h, q := queueHarness(t, nil)
	q.itemsErr = errFake

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, "def main(item):\n    item_list()\n"), modeOff())

	require.ErrorIs(t, err, errFake)
	require.ErrorContains(t, err, "read the open items of project")
}

func TestItemList_ABuildWithNoQueueReaderFailsTheCall(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, "def main(item):\n    item_list()\n"), modeOff())

	require.ErrorIs(t, err, errNoItemList)
}

func TestPRList_ReturnsTheOpenPullRequestsOfTheBaseAsOneRecordedStep(t *testing.T) {
	t.Parallel()

	h, q := queueHarness(t, nil, openPullOf(t, 7, "21", "main", "bob", "t7", true), openPullOf(t, 8, "22", "dev", "amy", "t8", false))

	file := `def main(item):
    got = pr_list(base = "main", name = "p")
    command(name = "seen", run = ["seen"] + ["%d|%s|%s|%s|%s|%s|%d" % (p["number"], p["branch"], p["base"], p["author"], p["draft"], p["title"], len(p["labels"])) for p in got.pulls])
`

	engine := h.engine(t)
	_, err := engine.RunFile(testItem, testProject, writeFlow(t, file), modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{"7|21|main|bob|True|t7|0"}, seenBy(t, h), "the forge applies the base")
	assert.Equal(t, []string{"main"}, q.basesAsked)
	assert.Contains(t, strings.Join(stepNames(t, engine), " "), "pr_list:p#1")
}

func TestPRList_AFailedReadFailsTheStep(t *testing.T) {
	t.Parallel()

	h, q := queueHarness(t, nil)
	q.pullsErr = errFake

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, "def main(item):\n    pr_list()\n"), modeOff())

	require.ErrorIs(t, err, errFake)
	require.ErrorContains(t, err, "read the open pull requests of project")
}

func TestPRList_ABuildWithNoQueueReaderFailsTheCall(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, "def main(item):\n    pr_list()\n"), modeOff())

	require.ErrorIs(t, err, errNoPullList)
}

func TestRunRead_ReturnsTheLastRunOfEachItemAndTheFreeSlotsInTheOrderAsked(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	file := `def main(item):
    got = run_read(items = ["31", "30"], name = "r")
    command(name = "seen", run = ["seen", str(got.free)] + [r["item"] + ":" + r["state"] for r in got.runs])
`

	engine := h.engine(t)
	_, err := engine.RunFile(testItem, testProject, writeFlow(t, file), modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{"2", "31:none", "30:none"}, seenBy(t, h), "an item with no run is none, and the rows keep the order of the items")
	assert.Contains(t, strings.Join(stepNames(t, engine), " "), "run_read:r#1")
}

func TestRunRead_AnItemsListThatIsNotAListOfItemsFailsTheCall(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, "def main(item):\n    run_read(items = [1])\n"), modeOff())

	require.Error(t, err)
}

func TestListReads_AnArgumentOfTheWrongTypeThatTheCheckCannotSeeFailsTheCall(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"item_list label":    `item_list(label = [1][0])`,
		"pr_list base":       `pr_list(base = [1][0])`,
		"run_read items":     `run_read(items = [1][0])`,
		"run_read item type": `run_read(items = [[1][0]])`,
		"run_read name":      `run_read(items = ["1"], name = [1][0])`,
	}

	for name, call := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h, _ := queueHarness(t, nil)

			_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, "def main(item):\n    "+call+"\n"), modeOff())

			require.Error(t, err)
		})
	}
}
