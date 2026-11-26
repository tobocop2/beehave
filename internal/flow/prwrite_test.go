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

func TestPRWrite_ACommentWithAPullRequestNumberGoesToThatPullRequestAndNotToTheOneOfTheItem(t *testing.T) {
	t.Parallel()

	h, f := prHarness(t)

	_, err := walkBody(t, h, `pr_comment(text = "own", key = "own", name = "own")
pr_comment(text = "other", key = "other", pr = 12, name = "other")
`)
	require.NoError(t, err)

	assert.Equal(t, []string{
		testProject + "|" + testItem + "|" + testRun + ":own|own",
		testProject + "|" + testItem + "#pr12|" + testRun + ":other|other",
	}, f.notes)
}

func TestPRWrite_ACommentWithAPullRequestNumberBelowZeroIsRefusedBeforeAnyPost(t *testing.T) {
	t.Parallel()

	h, f := prHarness(t)

	_, err := walkBody(t, h, `pr_comment(text = "x", key = "k", pr = -3, name = "c")
`)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "pr_comment: pr is -3. Give the number of a pull request, or leave pr out")
	assert.Empty(t, f.notes)
}

func TestPRWrite_ACrashBetweenEachWriteAndItsRecordRepeatsTheCallWithTheSameKey(t *testing.T) {
	t.Parallel()

	const (
		comment = testProject + "|" + testItem + "|" + testRun + ":take|taken"
		open    = testProject + "|" + testItem + "|Fix|text|main|"
	)

	tests := map[string]struct {
		call, method string
		logged       func(*fakeForge) []string
		want         string
	}{
		"pr_comment": {`pr_comment(text = "taken", key = "take", name = "c")`, "comment", func(f *fakeForge) []string { return f.notes }, comment},
		"pr_open":    {`pr_open(title = "Fix", body = "text", base = "main", name = "c")`, "open", func(f *fakeForge) []string { return f.opened }, open},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h, f := prHarness(t)
			deps := h.deps()
			deps.Forge = &crashForge{forge: f, crash: newCrash(t, h), on: tt.method}

			crashAndResume(t, h, deps, tt.call+"\n")

			assert.Equal(t, []string{tt.want, tt.want}, tt.logged(f), "the rerun reached the forge with the key and the arguments of the first call")
		})
	}
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
		"comment without a text": `pr_comment(text = "", key = "k")`,
		"comment without a key":  `pr_comment(text = "x", key = "")`,
	} {
		h, f := prHarness(t)
		w := newFakeWriter()
		h.writer = w

		_, err := walkBody(t, h, call+"\n")
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), "workflow.star:", name)
		assert.Empty(t, f.notes, name)
		assert.Empty(t, w.assignees, name)
	}
}

func TestPRWrite_AValueThatOnlyTheRunKnowsIsRefusedWhenItIsEmptyOrNotText(t *testing.T) {
	t.Parallel()

	for name, call := range map[string]string{
		"comment with an empty text": `pr_comment(text = empty, key = "k")`,
		"comment with an empty key":  `pr_comment(text = "x", key = empty)`,
		"comment with a number":      `pr_comment(text = number, key = "k")`,
	} {
		h, f := prHarness(t)

		_, err := walkBody(t, h, "empty = \"\"\nnumber = 1\n"+call+"\n")
		require.Error(t, err, name)
		assert.Empty(t, f.notes, name)
	}
}

func TestPRWrite_AForgeErrorFailsTheCall(t *testing.T) {
	t.Parallel()

	for _, call := range []string{`pr_comment(text = "x", key = "k")`, `pr_open(title = "t", base = "b")`} {
		h, f := prHarness(t)
		f.writeErr = errors.New("the forge refused")

		_, err := walkBody(t, h, call+"\n")
		require.ErrorContains(t, err, "the forge refused", call)
	}
}

func TestPRWrite_AProjectWithNoRepositoryRefusesThePullRequestBuiltins(t *testing.T) {
	t.Parallel()

	for _, call := range []string{`pr_comment(text = "x", key = "k")`, `pr_open(title = "t", base = "b")`} {
		h, f := prHarness(t)
		h.repository = func(string) bool { return false }

		_, err := walkBody(t, h, call+"\n")
		require.ErrorContains(t, err, "needs a repository", call)
		assert.Empty(t, f.notes)
		assert.Empty(t, f.opened)
	}
}

func TestPRWrite_ABuildWithNoWriterRefusesItemCreate(t *testing.T) {
	t.Parallel()

	_, err := walkBody(t, newHarness(t), "item_create(title = \"t\", key = \"k\", assignee = \"a\")\n")
	require.ErrorContains(t, err, "this build cannot write to a tracker")
}
