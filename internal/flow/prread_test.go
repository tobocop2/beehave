package flow

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/preflight"
)

// readsFake serves the reads of the claim stops: the facts of the pull request of each item, and the assignees of each issue by its text, such as "acme/tools#9".
type readsFake struct {
	mu     sync.Mutex
	facts  map[string]model.PullFacts
	issues map[string][]string
	err    error
	asked  []string
	// failIssue fails the read of an issue and no other read.
	failIssue bool
}

func (f *readsFake) Preflight(context.Context, string, string, preflight.Request, bool) (preflight.Result, error) {
	return preflight.Result{}, nil
}

func (f *readsFake) ReadPull(_ context.Context, _, item string, _ bool) (model.PullFacts, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.asked = append(f.asked, "pull "+item)

	return f.facts[item], f.err
}

func (f *readsFake) IssueAssignees(_ context.Context, _, repo string, number int) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	link := model.IssueLink{Repo: repo, Number: number}.String()
	f.asked = append(f.asked, "issue "+link)

	if f.failIssue {
		return nil, errFake
	}

	return f.issues[link], f.err
}

// IssueAuthor finds no issue.
func (f *readsFake) IssueAuthor(context.Context, string, string) (model.Authored, error) {
	return model.Authored{}, nil
}

func readsHarness(t *testing.T, fake *readsFake) (*harness, *fakeWriter) {
	t.Helper()

	h := newHarness(t)
	h.preflight = fake
	w := newFakeWriter()
	h.writer = w

	return h, w
}

const noteOf = "item_comment(id = \"1\", key = \"k\", text = \"x\")\n"

func TestPRRead_ReturnsTheFactsOfThePullRequestToTheWorkflow(t *testing.T) {
	t.Parallel()

	fake := &readsFake{facts: map[string]model.PullFacts{
		testItem: {Number: 7, State: "OPEN", Author: "dev", Draft: true, Labels: []string{"hold"}, Assignees: []string{"a"}, Reviewers: []string{"r"}, Links: []model.IssueLink{{Number: 3}, {Repo: "acme/tools", Number: 9}}},
		"other":  {Number: 8, State: "MERGED"},
	}}
	h, w := readsHarness(t, fake)

	_, err := walkBody(t, h, `pull = pr_read(name = "a")
other = pr_read(name = "b", id = "other")
none = pr_read(name = "c", id = "nothing")
item_comment(id = "1", key = "k", text = "|".join([str(pull.found), str(pull.number), pull.state, str(pull.draft), ",".join(pull.labels), pull.author, ",".join(pull.assignees), ",".join(pull.reviewers), pull.links[1]["repo"] + "#" + str(pull.links[1]["number"]), other.state, str(none.found), str(none.number), str(none.links), str(none.labels)]))
`)

	require.NoError(t, err)
	assert.Equal(t, []string{testProject + "|1|" + testRun + ":k|True|7|OPEN|True|hold|dev|a|r|acme/tools#9|MERGED|False|0|[]|[]"}, w.notes)
	assert.Equal(t, []string{"pull " + testItem, "pull other", "pull nothing"}, fake.asked, "an empty id reads the item of the run")
}

func TestPRRead_AFailedReadEndsTheItemAndABuildWithNoReaderNamesTheFix(t *testing.T) {
	t.Parallel()

	h, w := readsHarness(t, &readsFake{err: errFake})

	_, err := walkBody(t, h, "pr_read()\n"+noteOf)

	require.ErrorIs(t, err, errFake)
	assert.Empty(t, w.notes)

	_, err = walkBody(t, newHarness(t), "pr_read()\n")

	require.ErrorIs(t, err, errNoPreflight)
}

func TestIssueAssignees_ReturnsTheLoginsThatHoldTheIssue(t *testing.T) {
	t.Parallel()

	fake := &readsFake{issues: map[string][]string{"#7": {"maintainer", "bot"}, "acme/tools#9": {"other"}}}
	h, w := readsHarness(t, fake)

	_, err := walkBody(t, h, `own = issue_assignees(number = 7, name = "a")
other = issue_assignees(repo = "acme/tools", number = 9, name = "b")
none = issue_assignees(number = 5, name = "c")
item_comment(id = "1", key = "k", text = ",".join(own.assignees) + "|" + ",".join(other.assignees) + "|" + str(none.assignees))
`)

	require.NoError(t, err)
	assert.Equal(t, []string{testProject + "|1|" + testRun + ":k|maintainer,bot|other|[]"}, w.notes)
	assert.Equal(t, []string{"issue #7", "issue acme/tools#9", "issue #5"}, fake.asked)
}

func TestIssueAssignees_AFailedReadNamesTheIssueAndAnIssueBelowOneIsRefused(t *testing.T) {
	t.Parallel()

	h, _ := readsHarness(t, &readsFake{err: errFake})

	_, err := walkBody(t, h, "issue_assignees(repo = \"acme/tools\", number = 9)\n")

	require.ErrorIs(t, err, errFake)
	require.ErrorContains(t, err, "cannot read the assignees of acme/tools#9")

	fake := &readsFake{}
	h, _ = readsHarness(t, fake)

	_, err = walkBody(t, h, "issue_assignees(number = 0)\n")

	require.ErrorContains(t, err, "`number` is 0")
	assert.Empty(t, fake.asked)

	_, err = walkBody(t, newHarness(t), "issue_assignees(number = 1)\n")

	require.ErrorIs(t, err, errNoPreflight)
}

func TestItemStop_RecordsTheStopOfTheRun(t *testing.T) {
	t.Parallel()

	h, _ := readsHarness(t, &readsFake{})
	engine := h.engine(t)

	result, err := engine.RunFile(testItem, testProject, writeFlow(t, bodyFlow("item_stop(name = \"a\", step = \"claim_stop:a\", code = 10, reason = \"claimed\")\n")), modeOff())

	require.NoError(t, err)
	require.NotNil(t, result.Stopped)
	assert.Equal(t, Stopped{Step: "claim_stop:a", RC: 10, Output: "claimed"}, *result.Stopped)
	assert.Equal(t, []string{"item_stop:a#1"}, stepNames(t, engine))
}

func TestItemStop_AStopOfAnotherItemChangesNothingInTheResultOfTheRun(t *testing.T) {
	t.Parallel()

	h, _ := readsHarness(t, &readsFake{})
	engine := h.engine(t)

	result, err := engine.RunFile(testItem, testProject, writeFlow(t, bodyFlow("recorded = item_stop(name = \"a\", step = \"claim_stop:a\", code = 10, reason = \"claimed\", id = \"other\")\nif recorded.recorded:\n    fail(\"recorded\")\n")), modeOff())

	require.NoError(t, err)
	assert.Nil(t, result.Stopped)
}

func TestItemStop_ACodeBelowOneIsRefused(t *testing.T) {
	t.Parallel()

	h, _ := readsHarness(t, &readsFake{})

	_, err := walkBody(t, h, "item_stop(step = \"x\", code = 0, reason = \"r\")\n")

	require.ErrorContains(t, err, "`code` is 0")
}
