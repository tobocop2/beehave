package flow

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/model"
)

// pullBy is an open pull request on a branch, by an author, with the review decision of the forge.
func pullBy(t *testing.T, number int, branch, author, decision string) model.PullRequest {
	t.Helper()

	text, err := json.Marshal(map[string]any{"number": number, "headRefName": branch, "reviewDecision": decision, "author": map[string]string{"login": author}})
	require.NoError(t, err)

	var pull model.PullRequest
	require.NoError(t, json.Unmarshal(text, &pull))

	return pull
}

// rankedRows runs rank of the example workflow over a Starlark expression for the rows, for the login "owner" (the login of the example rig), and returns the items in the order it gives.
func rankedRows(t *testing.T, items []model.Item, pulls []model.PullRequest, rows string) []string {
	t.Helper()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	rig.h.queue = &fakeQueue{items: items, pulls: pulls}

	// The engine checks that the file of the run defines every entry that a loaded function spawns, so the file defines the pipeline entry too.
	source := "load(\"//workflow.star\", \"rank\")\n\ndef pipeline(item):\n    pass\n\ndef main(item):\n" +
		"    q = queue(name = \"q\")\n    command(name = \"order\", run = [\"order\"] + [r.item for r in rank(" + rows + ", env(\"OWNER_LOGIN\"))])\n"

	order := filepath.Join(filepath.Dir(file), "order.star")
	require.NoError(t, os.WriteFile(order, []byte(source), 0o600))

	_, err := rig.h.engine(t).RunFile(testItem, testProject, order)
	require.NoError(t, err)

	ran := rig.h.commands.ranBy(testItem)
	require.Len(t, ran, 1)

	return strings.Fields(ran[0])[1:]
}

// rankedBy runs rank over the rows of the queue step.
func rankedBy(t *testing.T, items []model.Item, pulls []model.PullRequest) []string {
	t.Helper()

	return rankedRows(t, items, pulls, "q.rows")
}

func TestExampleRank_OthersWorkThenChangeRequestsThenInFlightThenNewByContainerAndSeverity(t *testing.T) {
	t.Parallel()

	child := func(id string, severity model.Severity) model.Item {
		item := queueItem(id, severity)
		item.Parent = "9"

		return item
	}

	got := rankedBy(t,
		[]model.Item{
			queueItem("44", model.SeverityP0), child("45", model.SeverityP2), queueItem("46", model.SeverityP1), child("47", model.SeverityP3), child("48", model.SeverityP1),
			queueItem("43", model.SeverityP3), queueItem("42", model.SeverityP3), queueItem("41", model.SeverityP3),
		},
		[]model.PullRequest{pullBy(t, 3, "41", "owner", ""), pullBy(t, 2, "42", "owner", "CHANGES_REQUESTED"), pullBy(t, 1, "43", "someone", "")},
	)

	assert.Equal(t, []string{"43", "42", "41", "48", "45", "47", "44", "46"}, got,
		"a pull request of another author, then one of ours with requested changes, then our work in flight, then new work of a container before loose work, each by severity")
}

func TestExampleRank_AnAuthorThatDiffersFromTheLoginMakesOthersWork(t *testing.T) {
	t.Parallel()

	got := rankedBy(t,
		[]model.Item{queueItem("61", model.SeverityP0), queueItem("62", model.SeverityP3)},
		[]model.PullRequest{pullBy(t, 1, "62", "someone", ""), pullBy(t, 2, "61", "owner", "CHANGES_REQUESTED")},
	)

	assert.Equal(t, []string{"62", "61"}, got, "a pull request of another author has no review request and still goes before our change requests")
}

func TestExampleRank_OurOwnPullRequestWithAReviewRequestIsNotOthersWork(t *testing.T) {
	t.Parallel()

	var own model.PullRequest
	require.NoError(t, json.Unmarshal([]byte(`{"number":1,"headRefName":"72","reviewDecision":"REVIEW_REQUIRED","author":{"login":"owner"},"reviewRequests":[{"login":"someone"}]}`), &own))

	got := rankedBy(t,
		[]model.Item{queueItem("71", model.SeverityP3), queueItem("72", model.SeverityP3), queueItem("73", model.SeverityP3)},
		[]model.PullRequest{own, pullBy(t, 2, "73", "owner", "CHANGES_REQUESTED")},
	)

	assert.Equal(t, []string{"73", "72", "71"}, got, "a review request on our own pull request does not move it to the work of others: it is work in flight, after the change requests")
}

func TestExampleRank_ChangesRequestedOnAPullRequestOfAnotherAuthorStaysWithOthersWork(t *testing.T) {
	t.Parallel()

	got := rankedBy(t,
		[]model.Item{queueItem("81", model.SeverityP0), queueItem("82", model.SeverityP3), queueItem("83", model.SeverityP3)},
		[]model.PullRequest{pullBy(t, 1, "81", "owner", "CHANGES_REQUESTED"), pullBy(t, 2, "82", "someone", "CHANGES_REQUESTED"), pullBy(t, 3, "83", "someone", "")},
	)

	assert.Equal(t, []string{"82", "83", "81"}, got,
		"others first: a pull request of another author with requested changes stays in the group of others work, ahead of our own change requests")
}

func TestExampleRank_RowsWithEqualKeysKeepOneOrderWhateverOrderTheyArriveIn(t *testing.T) {
	t.Parallel()

	row := func(id string) string {
		return `struct(item = "` + id + `", state = "ready", severity = "P1", pr = 0, parent = "", author = "", changes_requested = False)`
	}

	one := "[" + row("51") + ", " + row("52") + ", " + row("53") + "]"
	two := "[" + row("53") + ", " + row("51") + ", " + row("52") + "]"

	assert.Equal(t, []string{"51", "52", "53"}, rankedRows(t, nil, nil, one))
	assert.Equal(t, []string{"51", "52", "53"}, rankedRows(t, nil, nil, two), "rank is fed the rows out of item order, so the item id key of rank is what orders them")
}
