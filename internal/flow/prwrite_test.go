package flow

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/review"
)

func prHarness(t *testing.T) (*harness, *fakeForge) {
	t.Helper()

	h := newHarness(t)
	f := &fakeForge{state: review.Standing{Number: 7}}
	h.forge = f

	return h, f
}

func TestPRWrite_CommentKeysTheCallWithTheRunAndReturnsTheNumber(t *testing.T) {
	t.Parallel()

	h, f := prHarness(t)

	_, err := walkBody(t, h, `posted = pr_comment(text = "taken", key = "take", name = "note")
command(run = [str(posted.number)])
`)
	require.NoError(t, err)

	assert.Equal(t, []string{testProject + "|" + testItem + "|" + testRun + ":take|taken"}, f.notes)
	assert.Equal(t, []string{"7"}, h.commands.ran())
}

func TestPRWrite_AssignAssignsThePullRequestAndOnlyWithLinkedItsIssues(t *testing.T) {
	t.Parallel()

	h, f := prHarness(t)

	_, err := walkBody(t, h, `own = pr_assign(assignee = "alice", name = "own")
both = pr_assign(assignee = "alice", linked = True, name = "both")
command(run = [str(own.number), str(len(own.issues)), str(len(both.issues))])
`)
	require.NoError(t, err)

	assert.Equal(t, []string{
		testProject + "|" + testItem + "|alice|false",
		testProject + "|" + testItem + "|alice|true",
	}, f.assigned)
	assert.Equal(t, []string{"7 0 2"}, h.commands.ran())
}

func TestPRWrite_AssignOfAnItemCallsTheWriter(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	w := newFakeWriter()
	h.writer = w

	_, err := walkBody(t, h, `done = item_assign(id = "n-1", assignee = "alice", name = "own")
command(run = [done.id])
`)
	require.NoError(t, err)

	assert.Equal(t, []string{testProject + "|n-1|alice"}, w.assigned)
	assert.Equal(t, []string{"n-1"}, h.commands.ran())
}

func TestPRWrite_ItemCreateTakesAnAssigneeAndTheDefaultIsNone(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	w := newFakeWriter()
	h.writer = w

	_, err := walkBody(t, h, `item_create(title = "a", key = "a", assignee = "alice", name = "a")
item_create(title = "b", key = "b", name = "b")
`)
	require.NoError(t, err)

	assert.Equal(t, []string{testRun + ":a|alice", testRun + ":b|"}, w.assignees)
}

func TestPRWrite_ItemCreateTakesOneSeverityAndRefusesAnyOtherBeforeTheWrite(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	w := newFakeWriter()
	h.writer = w

	_, err := walkBody(t, h, `item_create(title = "a", key = "a", severity = "P1", name = "a")
item_create(title = "b", key = "b", name = "b")
`)
	require.NoError(t, err)
	assert.Equal(t, []string{testRun + ":a|P1", testRun + ":b|"}, w.severities)

	for _, bad := range []string{"P4", "p1", "P?", "high", "P1,P2"} {
		bad := bad
		w := newFakeWriter()
		h := newHarness(t)
		h.writer = w

		_, err := walkBody(t, h, `item_create(title = "a", key = "a", severity = "`+bad+`", name = "a")`+"\n")
		require.ErrorContains(t, err, "severity", bad)
		assert.Empty(t, w.created, bad)
	}
}

func TestPRWrite_ArefusedArgumentWritesNothing(t *testing.T) {
	t.Parallel()

	for name, call := range map[string]string{
		"comment without a text":     `pr_comment(text = "", key = "k")`,
		"comment without a key":      `pr_comment(text = "x", key = "")`,
		"assign without an assignee": `pr_assign(assignee = "")`,
		"item assign without an id":  `item_assign(id = "", assignee = "alice")`,
		"item assign no assignee":    `item_assign(id = "1", assignee = "")`,
		"linked that is not a bool":  `pr_assign(assignee = "a", linked = "yes")`,
	} {
		h, f := prHarness(t)
		w := newFakeWriter()
		h.writer = w

		_, err := walkBody(t, h, call+"\n")
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), "workflow.star:", name)
		assert.Empty(t, f.notes, name)
		assert.Empty(t, f.assigned, name)
		assert.Empty(t, w.assigned, name)
	}
}

func TestPRWrite_AValueThatOnlyTheRunKnowsIsRefusedWhenItIsEmptyOrNotText(t *testing.T) {
	t.Parallel()

	for name, call := range map[string]string{
		"comment with an empty text": `pr_comment(text = empty, key = "k")`,
		"comment with an empty key":  `pr_comment(text = "x", key = empty)`,
		"comment with a number":      `pr_comment(text = number, key = "k")`,
		"assign with an empty login": `pr_assign(assignee = empty)`,
		"assign with a number":       `pr_assign(assignee = number)`,
	} {
		h, f := prHarness(t)

		_, err := walkBody(t, h, "empty = \"\"\nnumber = 1\n"+call+"\n")
		require.Error(t, err, name)
		assert.Empty(t, f.notes, name)
		assert.Empty(t, f.assigned, name)
	}
}

func TestPRWrite_AForgeErrorFailsTheCall(t *testing.T) {
	t.Parallel()

	for _, call := range []string{`pr_comment(text = "x", key = "k")`, `pr_assign(assignee = "a")`} {
		h, f := prHarness(t)
		f.writeErr = errors.New("the forge refused")

		_, err := walkBody(t, h, call+"\n")
		require.ErrorContains(t, err, "the forge refused", call)
	}
}

func TestPRWrite_AProjectWithNoRepositoryRefusesThePullRequestBuiltins(t *testing.T) {
	t.Parallel()

	for _, call := range []string{`pr_comment(text = "x", key = "k")`, `pr_assign(assignee = "a")`} {
		h, f := prHarness(t)
		h.repository = func(string) bool { return false }

		_, err := walkBody(t, h, call+"\n")
		require.ErrorContains(t, err, "needs a repository", call)
		assert.Empty(t, f.notes)
		assert.Empty(t, f.assigned)
	}
}

func TestPRWrite_ABuildWithNoWriterRefusesItemAssign(t *testing.T) {
	t.Parallel()

	_, err := walkBody(t, newHarness(t), "item_assign(id = \"1\", assignee = \"a\")\n")
	require.ErrorContains(t, err, "this build cannot write to a tracker")
}
