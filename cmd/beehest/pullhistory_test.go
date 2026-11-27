package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/config"
	"github.com/tobocop2/beehest/internal/forge"
	"github.com/tobocop2/beehest/internal/model"
)

// priorForge lists pull requests by the kind of the search: onBranch for --head, named for --search. comments holds the comments of each number.
// The methods it does not define panic, so a test shows every call the code makes.
type priorForge struct {
	forge.Forge

	onBranch, named     []model.PullRequest
	branchErr, namedErr error
	comments            map[int][]model.Comment
	commentErr          error
	asked               [][]string
	limits              []int
	commentsAsked       []int
}

// PRList answers as gh does: each row holds only the fields that --json names, and a name that a pull request does not have is refused.
func (f *priorForge) PRList(_ context.Context, limit int, args ...string) ([]model.PullRequest, error) {
	f.asked = append(f.asked, args)
	f.limits = append(f.limits, limit)

	rows, err := f.onBranch, f.branchErr
	if slices.Contains(args, "--search") {
		rows, err = f.named, f.namedErr
	}

	if err != nil {
		return nil, err
	}

	return onlyFields(rows, args)
}

// onlyFields keeps of each row the fields that the --json argument names, as the JSON output of gh holds them.
func onlyFields(rows []model.PullRequest, args []string) ([]model.PullRequest, error) {
	at := slices.Index(args, "--json")
	if at < 0 || at+1 >= len(args) {
		return nil, errors.New("the list asks for no fields")
	}

	names := strings.Split(args[at+1], ",")
	kept := make([]model.PullRequest, 0, len(rows))

	for _, row := range rows {
		out, err := rowWithFields(row, names)
		if err != nil {
			return nil, err
		}

		kept = append(kept, out)
	}

	return kept, nil
}

// rowWithFields is the row with only the named JSON fields set. A name that a pull request does not have is refused, as gh refuses it.
func rowWithFields(row model.PullRequest, names []string) (model.PullRequest, error) {
	var out model.PullRequest

	raw, err := json.Marshal(row)
	if err != nil {
		return out, err
	}

	var all map[string]json.RawMessage
	if err := json.Unmarshal(raw, &all); err != nil {
		return out, err
	}

	some := map[string]json.RawMessage{}

	for _, name := range names {
		value, ok := all[name]
		if !ok {
			return out, fmt.Errorf("unknown JSON field: %q", name)
		}

		some[name] = value
	}

	raw, err = json.Marshal(some)
	if err != nil {
		return out, err
	}

	return out, json.Unmarshal(raw, &out)
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
	row := model.PullRequest{Number: number, Title: title, Body: body, State: &state, HeadRefName: &branch, BaseRefName: &base}

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
	return model.Comment{Body: body, CreatedAt: at}
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
	assert.Equal(t, [][]string{
		{"--state", "all", "--head", "41", "--json", "number,title,body,state,headRefName,baseRefName,mergedAt,closedAt"},
		{"--state", "all", "--search", "#41 in:title,body", "--json", "number,title,body,state,headRefName,baseRefName,mergedAt,closedAt"},
	}, f.asked, "each list names the fields that the history reads, the close time among them")
	assert.Equal(t, []int{forge.OpenPullLimit, forge.OpenPullLimit}, f.limits, "the lists of every state get the limit of the open pull requests, not the one of the open search")
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
	assert.Len(t, f.asked, 1, "no search for an item that no pull request can name by number")
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
