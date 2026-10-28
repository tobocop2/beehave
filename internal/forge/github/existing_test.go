package github_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/proc/proctest"
)

const (
	reviewsPath = "repos/owner/name/pulls/7/reviews"
	inlinePath  = "repos/owner/name/pulls/7/comments"
	convoPath   = "repos/owner/name/issues/7/comments"
	at          = "2026-10-01T10:00:00Z"
)

func pages(t *testing.T, items ...[]fields) string {
	t.Helper()

	for i, page := range items {
		if page == nil {
			items[i] = []fields{}
		}
	}

	return toJSON(t, items)
}

func reviewRow(user, state, when string, body any) fields {
	return fields{"user": fields{"login": user}, "state": state, "submitted_at": when, "body": body}
}

var (
	inlineNote = fields{"user": fields{"login": "gz"}, "path": "a.rs", "line": 3, "created_at": at, "body": "inline note"}
	convoNote  = fields{"user": fields{"login": "gz"}, "created_at": "2026-10-01T09:00:00Z", "body": "conversation note"}
)

// existingRun plans the three sources and the change request query.
func existingRun(t *testing.T, reviews, inline, convo []fields, standing string) *proctest.Scripted {
	t.Helper()

	run := &proctest.Scripted{}
	run.Plan(pages(t, reviews), "api", "--paginate", "--slurp", reviewsPath)
	run.Plan(pages(t, inline), "api", "--paginate", "--slurp", inlinePath)
	run.Plan(pages(t, convo), "api", "--paginate", "--slurp", convoPath)
	run.PlanContaining(standing, "latestOpinionatedReviews")

	return run
}

func existing(t *testing.T, run *proctest.Scripted) ([]string, error) {
	t.Helper()

	return newForge(t, repo, run).ExistingReview(context.Background(), 7)
}

func TestExistingReview_AChangeRequestFollowedByAComment_StillStands(t *testing.T) {
	t.Parallel()

	run := existingRun(t, []fields{reviewRow("gz", "CHANGES_REQUESTED", at, "b"), reviewRow("gz", "COMMENTED", "t", "b")}, nil, nil,
		nodes(t, -1, [3]any{"gz", "CHANGES_REQUESTED", "aaaaaaaaaaaa"}))

	lines, err := existing(t, run)
	require.NoError(t, err)
	assert.Equal(t, "\n# standing change requests: gz@aaaaaaaaa", lines[len(lines)-1])
}

func TestExistingReview_CountsAndSectionsComeInOrder(t *testing.T) {
	t.Parallel()

	run := existingRun(t, []fields{reviewRow("ann", "APPROVED", "2026-10-01T11:00:00Z", "b")}, []fields{inlineNote}, []fields{convoNote},
		nodes(t, -1, [3]any{"ann", "APPROVED", "a"}))

	lines, err := existing(t, run)
	require.NoError(t, err)
	assert.Equal(t, []string{
		"# existing review on #7: 1 reviews, 1 inline, 1 conversation",
		"\n## REVIEW [APPROVED] ann 2026-10-01T11:00:00Z\nb",
		"\n## INLINE a.rs:3 gz " + at + "\ninline note",
		"\n## CONVERSATION gz 2026-10-01T09:00:00Z\nconversation note",
		"\n# standing change requests: none",
	}, lines)
}

func TestExistingReview_WhenNothingWasSaid_TheReportStatesIt(t *testing.T) {
	t.Parallel()

	lines, err := existing(t, existingRun(t, nil, nil, nil, nodes(t, -1)))
	require.NoError(t, err)
	assert.Equal(t, []string{
		"# existing review on #7: 0 reviews, 0 inline, 0 conversation",
		"# nothing has been said yet: every finding you return is NEW by definition.",
		"\n# standing change requests: none",
	}, lines)
}

func TestExistingReview_OneSourceWithSomethingDropsTheNothingLine(t *testing.T) {
	t.Parallel()

	tests := map[string]*proctest.Scripted{
		"a_review":               existingRun(t, []fields{reviewRow("a", "COMMENTED", "t", "b")}, nil, nil, nodes(t, -1)),
		"an_inline_comment":      existingRun(t, nil, []fields{inlineNote}, nil, nodes(t, -1)),
		"a_conversation_comment": existingRun(t, nil, nil, []fields{convoNote}, nodes(t, -1)),
	}
	for name, run := range tests {
		lines, err := existing(t, run)
		require.NoError(t, err, name)

		for _, line := range lines {
			assert.False(t, strings.HasPrefix(line, "# nothing has been said"), name)
		}
	}
}

func TestExistingReview_MissingFieldsReadAsTheOldReportReadThem(t *testing.T) {
	t.Parallel()

	run := existingRun(t,
		[]fields{{"state": "APPROVED", "user": fields{"login": "ann"}, "body": nil}, {"body": ""}},
		[]fields{
			{"path": "a.rs", "original_line": 9, "user": fields{"login": "gz"}, "body": nil, "created_at": "t"},
			{"path": "b", "user": fields{}},
			{"path": "a", "line": 0, "original_line": 5, "user": fields{"login": "g"}, "created_at": "t", "body": "x"},
		},
		[]fields{{"user": fields{"login": "gz"}, "body": nil, "created_at": "t"}},
		nodes(t, -1))

	lines, err := existing(t, run)
	require.NoError(t, err)
	assert.Equal(t, "\n## REVIEW [APPROVED] ann \n(no body)", lines[1])
	assert.Equal(t, "\n## REVIEW [null] null \n", lines[2])
	assert.Equal(t, "\n## INLINE a.rs:9 gz t\n", lines[3])
	assert.Equal(t, "\n## INLINE b:? null null\n", lines[4])
	assert.Equal(t, "\n## INLINE a:0 g t\nx", lines[5])
	assert.Equal(t, "\n## CONVERSATION gz t\n", lines[6])
}

func TestExistingReview_LineEndingsInABodyReachTheReportUnchanged(t *testing.T) {
	t.Parallel()

	body := "one\r\ntwo\rthree\n"
	run := existingRun(t, []fields{reviewRow("gz", "COMMENTED", "t", body)},
		[]fields{{"user": fields{"login": "g"}, "path": "p", "line": 1, "created_at": "t", "body": body}},
		[]fields{{"user": fields{"login": "g"}, "created_at": "t", "body": body}}, nodes(t, -1))

	lines, err := existing(t, run)
	require.NoError(t, err)

	for _, line := range lines[1:4] {
		assert.True(t, strings.HasSuffix(line, body))
	}
}

func TestExistingReview_AnUnreadableSourceRefusesAndNeverReadsAsEmpty(t *testing.T) {
	t.Parallel()

	answers := map[string]struct {
		stdout string
		exit   int
	}{
		"empty_exit1": {"", 1}, "valid_list_exit1": {"[[]]", 1}, "null": {"null", 0},
		"object": {`{"a":1}`, 0}, "flat_list": {"[1]", 0}, "not_json": {"not json", 0},
	}
	for _, source := range []string{"pulls/7/reviews", "pulls/7/comments", "issues/7/comments"} {
		for name, bad := range answers {
			run := existingRun(t, nil, nil, nil, nodes(t, -1))
			run.PlanResult(commandResult(bad.stdout, bad.exit), "repos/owner/name/"+source)

			lines, err := existing(t, run)
			require.Error(t, err, name)
			assert.Nil(t, lines)
			assert.True(t, strings.HasPrefix(err.Error(), "cannot read "+source+". An unreadable source is not an empty one."), err.Error())
		}
	}
}

func TestExistingReview_AFailedChangeRequestReadKeepsEveryOtherLine(t *testing.T) {
	t.Parallel()

	run := existingRun(t, []fields{reviewRow("gz", "COMMENTED", "t", "b")}, []fields{inlineNote}, []fields{convoNote}, "")
	run.PlanContainingFail("latestOpinionatedReviews")

	lines, err := existing(t, run)
	require.EqualError(t, err, "could not read the reviews of pull request 7")
	assert.Len(t, lines, 4)
	assert.Equal(t, "# existing review on #7: 1 reviews, 1 inline, 1 conversation", lines[0])
}
