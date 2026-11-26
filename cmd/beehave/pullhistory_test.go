package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/forge"
	"github.com/tobocop2/beehave/internal/model"
)

// priorForge lists pull requests by the kind of the read: onBranch for the branch, named for the search. comments holds the comments of each number.
// The methods it does not define panic, so a test shows every call the code makes.
type priorForge struct {
	forge.Forge

	onBranch, named     []model.PullRequest
	branchErr, namedErr error
	comments            map[int][]model.Comment
	commentErr          error
	branches, searches  []string
	commentsAsked       []int
}

func (f *priorForge) BranchPulls(_ context.Context, branch string) ([]model.PullRequest, error) {
	f.branches = append(f.branches, branch)

	return f.onBranch, f.branchErr
}

func (f *priorForge) SearchPulls(_ context.Context, words string) ([]model.PullRequest, error) {
	f.searches = append(f.searches, words)

	return f.named, f.namedErr
}

func (f *priorForge) Comments(_ context.Context, number int) ([]model.Comment, error) {
	f.commentsAsked = append(f.commentsAsked, number)

	return f.comments[number], f.commentErr
}

func historyOver(f forge.Forge) pulls {
	return pulls{projects: map[string]config.Project{"p": {}}, forge: func(model.Project) (forge.Forge, error) { return f, nil }}
}

// closeTime is the close time of every closed or merged row of priorRow.
const closeTime = "2026-11-10T12:00:00Z"

// priorRow is a listed pull request with the fields that History reads. A row that is not open closed at closeTime.
func priorRow(number int, title, body, state, branch, mergedAt string) model.PullRequest {
	base := "main"
	row := model.PullRequest{Number: number, Title: title, Body: body, State: &state, HeadRefName: &branch, BaseRefName: &base, AuthorAssociation: model.AssociationOwner}

	if mergedAt != "" {
		row.MergedAt = &mergedAt
	}

	if state != stateOpen {
		closed := closeTime
		row.ClosedAt = &closed
	}

	return row
}

// said is a comment with its time.
func said(body, at string) model.Comment {
	return model.Comment{Body: body, CreatedAt: at, AuthorAssociation: model.AssociationOwner}
}

func TestPulls_HistoryListsThePullRequestsOfTheBranchAndOfTheIssueWithTheirClosingComment(t *testing.T) {
	t.Parallel()

	f := &priorForge{
		onBranch: []model.PullRequest{priorRow(9, "retry", "", "OPEN", "41", ""), priorRow(4, "first try", "", "CLOSED", "41", "")},
		named: []model.PullRequest{
			priorRow(4, "first try", "", "CLOSED", "41", ""),
			priorRow(6, "other branch", "Closes #41", "MERGED", "fix-x", "2026-11-01T00:00:00Z"),
			priorRow(7, "loose match", "see #410", "CLOSED", "y", ""),
			priorRow(8, "retry of #41", "a body that names no issue", "CLOSED", "z", ""),
			priorRow(11, "fix #410", "", "CLOSED", "w", ""),
		},
		comments: map[int][]model.Comment{
			4: {said("early", "2026-11-09T00:00:00Z"), said("refuted: slower by 8%", "2026-11-10T11:00:00Z")},
			6: {said("landed", "2026-11-10T12:00:00Z")},
		},
	}

	got, err := historyOver(f).History(t.Context(), "p", "41")

	require.NoError(t, err)
	assert.Equal(t, []model.PriorPull{
		{Number: 4, Title: "first try", State: "CLOSED", Branch: "41", Base: "main", ClosingComment: "refuted: slower by 8%"},
		{Number: 6, Title: "other branch", State: "MERGED", Branch: "fix-x", Base: "main", MergedAt: "2026-11-01T00:00:00Z", ClosingComment: "landed"},
		{Number: 8, Title: "retry of #41", State: "CLOSED", Branch: "z", Base: "main"},
		{Number: 9, Title: "retry", State: "OPEN", Branch: "41", Base: "main"},
	}, got, "the branch and the issue join by number; a title alone that names the item keeps a pull request; a longer number in the body or the title is dropped")
	assert.Equal(t, []string{"41"}, f.branches, "the history lists the branch of the item")
	assert.Equal(t, []string{"#41 in:title,body"}, f.searches, "the history searches the title and the body for the issue")
	assert.Equal(t, []int{4, 6, 8}, f.commentsAsked, "an open pull request has no closing comment to read")
}

func TestPulls_TheClosingCommentIsTheLastCommentMadeAtOrBeforeTheClose(t *testing.T) {
	t.Parallel()

	f := &priorForge{
		onBranch: []model.PullRequest{
			priorRow(4, "closed", "", "CLOSED", "41", ""),
			priorRow(5, "only late talk", "", "CLOSED", "41", ""),
			priorRow(6, "merged", "", "MERGED", "41", closeTime),
			priorRow(9, "open", "", "OPEN", "41", ""),
		},
		comments: map[int][]model.Comment{
			// The forge lists the comments oldest first. A comment after the close is a later remark, not the reason for the close.
			4: {said("early", "2026-11-09T00:00:00Z"), said("refuted: slower", closeTime), said("superseded by #12", "2026-11-10T12:00:01Z")},
			5: {said("bot: stale branch deleted", "2026-11-11T00:00:00Z")},
			6: {said("landed", "2026-11-10T13:00:00+01:00"), said("thanks", "2026-11-12T00:00:00Z")},
			9: {said("still open", "2026-11-09T00:00:00Z")},
		},
	}

	got, err := historyOver(f).History(t.Context(), "p", "bb-1")

	require.NoError(t, err)
	require.Len(t, got, 4)
	assert.Equal(t, "refuted: slower", got[0].ClosingComment, "a comment at the close time counts, and one a second later does not")
	assert.Empty(t, got[1].ClosingComment, "a pull request with only comments after its close has no closing comment")
	assert.Equal(t, "landed", got[2].ClosingComment, "the times compare as instants, not as text: 13:00+01:00 is the close time")
	assert.Empty(t, got[3].ClosingComment, "an open pull request has no closing comment")
	assert.Equal(t, []int{4, 5, 6}, f.commentsAsked, "the comments of an open pull request are not read")
}

func TestPulls_HistoryOfAnItemThatIsNoIssueNumberReadsOnlyItsBranch(t *testing.T) {
	t.Parallel()

	f := &priorForge{onBranch: []model.PullRequest{priorRow(3, "t", "", "CLOSED", "bb-1", "")}}

	got, err := historyOver(f).History(t.Context(), "p", "bb-1")

	require.NoError(t, err)
	assert.Equal(t, []model.PriorPull{{Number: 3, Title: "t", State: "CLOSED", Branch: "bb-1", Base: "main"}}, got, "a closed pull request with no comment has an empty closing comment")
	assert.Empty(t, f.searches, "no search for an item that no pull request can name by number")
}

func TestPulls_HistoryOfAProjectWithNoForgeIsEmpty(t *testing.T) {
	t.Parallel()

	noForge := pulls{projects: map[string]config.Project{"p": {}}, forge: func(model.Project) (forge.Forge, error) { return nil, forge.ErrNoForge }}

	got, err := noForge.History(t.Context(), "p", "41")

	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestPulls_AHistoryReadThatFailsIsAnError(t *testing.T) {
	t.Parallel()

	_, err := historyOver(nil).History(t.Context(), "zzz", "41")
	require.ErrorIs(t, err, errUnknownProject)

	_, err = historyOver(&priorForge{branchErr: errFake}).History(t.Context(), "p", "41")
	require.ErrorIs(t, err, errFake)
	require.ErrorContains(t, err, "list the pull requests on the branch of item 41")

	_, err = historyOver(&priorForge{namedErr: errFake}).History(t.Context(), "p", "41")
	require.ErrorIs(t, err, errFake)
	require.ErrorContains(t, err, "search the pull requests that name item 41")

	closed := &priorForge{onBranch: []model.PullRequest{priorRow(4, "t", "", "CLOSED", "41", "")}, commentErr: errFake}
	_, err = historyOver(closed).History(t.Context(), "p", "41")
	require.ErrorIs(t, err, errFake)
	require.ErrorContains(t, err, "read the closing comment of pull request 4")

	noClose := priorRow(4, "t", "", "CLOSED", "41", "")
	noClose.ClosedAt = nil
	_, err = historyOver(&priorForge{onBranch: []model.PullRequest{noClose}}).History(t.Context(), "p", "41")
	require.ErrorContains(t, err, "pull request 4 is CLOSED but the forge gave no valid close time")

	badTime := &priorForge{onBranch: []model.PullRequest{priorRow(4, "t", "", "CLOSED", "41", "")}, comments: map[int][]model.Comment{4: {said("x", "yesterday")}}}
	_, err = historyOver(badTime).History(t.Context(), "p", "41")
	require.ErrorContains(t, err, "a comment of pull request 4 has no valid time")
}

func TestHistory_ReadsTheAuthorAssociation(t *testing.T) {
	t.Parallel()

	foreign := priorRow(5, "from a contributor", "", "CLOSED", "41", "")
	foreign.AuthorAssociation = model.AssociationContributor
	nobody := priorRow(6, "no association", "", "OPEN", "41", "")
	nobody.AuthorAssociation = ""
	ownByStranger := priorRow(7, "own pull request", "", "CLOSED", "41", "")

	f := &priorForge{
		onBranch: []model.PullRequest{priorRow(4, "own", "", "OPEN", "41", ""), foreign, nobody, ownByStranger},
		comments: map[int][]model.Comment{
			5: {said("owner answer", "2026-11-10T11:00:00Z")},
			7: {{Body: "stranger answer", CreatedAt: "2026-11-10T11:00:00Z", AuthorAssociation: model.AssociationNone}},
		},
	}

	got, err := historyOver(f).History(t.Context(), "p", "bb-1")

	require.NoError(t, err)
	require.Len(t, got, 4)
	assert.Equal(t, []bool{false, true, true, false}, []bool{got[0].Foreign, got[1].Foreign, got[2].Foreign, got[3].Foreign}, "a pull request of another account, or with no association, is foreign")
	assert.Equal(t, []bool{false, false, false, true}, []bool{got[0].ClosingForeign, got[1].ClosingForeign, got[2].ClosingForeign, got[3].ClosingForeign}, "the closing comment is judged by its own author, and a pull request with no comment has no foreign comment")
	assert.Equal(t, "owner answer", got[1].ClosingComment, "the adapter keeps the text: the step module withholds it")
}
