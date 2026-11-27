package flow

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/model"
)

// prHistoryFlow reads the pull requests of the item and passes each, field by field, to the command seen. The command after it shows that the run went on.
const prHistoryFlow = `def main(item):
    h = pr_history(name = "history")
    command(name = "seen", run = ["seen"] + ["%d|%s|%s|%s|%s|%s|%s" % (p["number"], p["title"], p["state"], p["branch"], p["base"], p["merged_at"], p["closing_comment"]) for p in h.pulls])
`

func TestPRHistory_ReadsThePullRequestsOfTheItemInEveryState(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.forge.history = []model.PriorPull{
		{Number: 3, Title: "first", State: "CLOSED", Branch: testItem, Base: "main", ClosingComment: "refuted:slower"},
		{Number: 5, Title: "second", State: "MERGED", Branch: "other", Base: "main", MergedAt: "2026-11-01T00:00:00Z", ClosingComment: "landed"},
		{Number: 8, Title: "third", State: "OPEN", Branch: testItem, Base: "main"},
	}
	engine := h.engine(t)

	_, err := engine.RunFile(testItem, testProject, writeFlow(t, prHistoryFlow), modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{
		"3|first|CLOSED|" + testItem + "|main||refuted:slower",
		"5|second|MERGED|other|main|2026-11-01T00:00:00Z|landed",
		"8|third|OPEN|" + testItem + "|main||",
	}, seenBy(t, h), "every state comes back, with the closing comment of each closed or merged one")
	assert.Equal(t, []string{testProject + "|" + testItem}, h.forge.historyAsked, "the call reads the pull requests of the item of the run, once")
	assert.Contains(t, stepNames(t, engine), "pr_history:history#1")
}

func TestPRHistory_TheKeywordItemReadsThePullRequestsOfThatItem(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.forge.history = []model.PriorPull{{Number: 3, Title: "parent", State: "OPEN", Branch: "bb-4", Base: "main"}}
	flow := "def main(item):\n    h = pr_history(name = \"parent-history\", item = \"bb-4\")\n    command(name = \"seen\", run = [\"seen\"] + [p[\"title\"] for p in h.pulls])\n"

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, flow), modeOff())

	require.NoError(t, err)
	assert.Equal(t, []string{testProject + "|bb-4"}, h.forge.historyAsked, "the read names the item of the keyword, not the item of the run")
	assert.Equal(t, []string{"parent"}, seenBy(t, h))
}

func TestPRHistory_AnItemWithNoPullRequestGetsAnEmptyList(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, prHistoryFlow), modeOff())

	require.NoError(t, err)
	assert.Empty(t, seenBy(t, h))
}

func TestPRHistory_AFailedReadStopsTheStep(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.forge.historyErr = errFake

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, prHistoryFlow), modeOff())

	require.ErrorIs(t, err, errFake)
	require.ErrorContains(t, err, "read the pull requests of item "+testItem)
	assert.Empty(t, h.commands.ranBy(testItem), "no step after the failed read runs on a guess")
}

func TestPRHistory_ANameThatIsNotTextIsRefusedBeforeAnyRead(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, "def main(item):\n    label = 41\n    pr_history(name = label)\n"), modeOff())

	require.ErrorContains(t, err, `pr_history: for parameter "name": got int, want string`)
	assert.Empty(t, h.forge.historyAsked, "a refused call reads nothing")
}
