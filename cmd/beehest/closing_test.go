package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/config"
	"github.com/tobocop2/beehest/internal/flow"
	"github.com/tobocop2/beehest/internal/forge"
	"github.com/tobocop2/beehest/internal/merge"
	"github.com/tobocop2/beehest/internal/model"
	storegithub "github.com/tobocop2/beehest/internal/store/github"
)

func TestPullsRequest_NamesAreTheClosesOfTheMember(t *testing.T) {
	t.Parallel()

	for tracker, closes := range map[model.TrackerName][]int{storegithub.Tracker: {12, 7}, "local": {}, "beads": {3}} {
		req := pulls{}.request(config.Project{Project: model.Project{Tracker: tracker}}, 5, gated, flow.MergeSettings{Closes: closes}, nil)

		assert.Equal(t, closes, req.Names, tracker)
	}
}

func TestPullsRequest_TheIssuesTheOwnerKeptReachTheMergeRequest(t *testing.T) {
	t.Parallel()

	req := pulls{}.request(config.Project{}, 5, gated, flow.MergeSettings{Kept: []model.IssueRef{"#13", "other-org/other-repo#5"}}, nil)

	assert.Equal(t, []model.IssueRef{"#13", "other-org/other-repo#5"}, req.Kept)
}

func TestPullsRequest_TheClosingSetThatTheScopeCheckReadReachesTheMergeRequestAsThePinnedSet(t *testing.T) {
	t.Parallel()

	req := pulls{}.request(config.Project{}, 5, gated, flow.MergeSettings{Closing: []model.IssueRef{"#12", "other-org/other-repo#5"}}, nil)
	assert.Equal(t, []model.IssueRef{"#12", "other-org/other-repo#5"}, req.Pinned)

	assert.Nil(t, pulls{}.request(config.Project{}, 5, gated, flow.MergeSettings{}, nil).Pinned, "a member with no check pins no set")
}

func TestPullsMerge_AForgeThatCannotReadClosingReferencesIsRefusedForEveryTrackerAndNothingMerges(t *testing.T) {
	t.Parallel()

	for _, tracker := range []model.TrackerName{storegithub.Tracker, "local"} {
		p, f, settings := shipping(t, nil)
		p.projects = map[string]config.Project{"p": {Project: model.Project{Repo: "o/r", Tracker: tracker}}}
		settings.Closes = []int{12}
		f.refsErr = errFake

		result, err := p.Merge(t.Context(), "p", "12", gated, settings)

		require.NoError(t, err)
		assert.Equal(t, merge.Refused, result.Stop, tracker)
		assert.Equal(t, merge.Transient, result.Reason, tracker)
		assert.Contains(t, result.Lines[len(result.Lines)-1], "cannot read the issues that #5 closes", tracker)
		assert.Empty(t, f.writes, tracker)
	}
}

// listedForge lists the issues in issues as the issues that the pull request closes.
type listedForge struct {
	*shipForge

	issues []int
}

func (f listedForge) ClosingRefs(context.Context, int) (model.ClosingRefs, error) {
	return model.ClosingRefs{Issues: f.issues, BaseBranch: "main", DefaultBranch: "main"}, nil
}

func TestPullsMerge_AnIssueThatTheForgeListsAndTheClosesListLacksIsRefusedForTheLocalTracker(t *testing.T) {
	t.Parallel()

	p, f, settings := shipping(t, nil)
	p.projects = map[string]config.Project{"p": {Project: model.Project{Repo: "o/r", Tracker: "local"}}}
	p.forge = func(model.Project) (forge.Forge, error) { return listedForge{shipForge: f, issues: []int{7}}, nil }
	settings.Closes = []int{}

	result, err := p.Merge(t.Context(), "p", "12", gated, settings)

	require.NoError(t, err)
	assert.Equal(t, merge.Refused, result.Stop)
	assert.Equal(t, "REFUSE: #5 closes #7 and the closes list of the member holds no issue. The owner decides: fix the closing keywords or the closes list.", result.Lines[len(result.Lines)-1])
	assert.Empty(t, f.writes)
}

func TestPullsMerge_AProjectThatTheMapLacksIsRefusedAndNothingMerges(t *testing.T) {
	t.Parallel()

	p, f, settings := shipping(t, nil)
	p.projects = map[string]config.Project{}

	_, err := p.Merge(t.Context(), "ghost", "12", gated, settings)

	require.ErrorIs(t, err, errUnknownProject)
	assert.Empty(t, f.writes)
}
