package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/config"
	"github.com/tobocop2/obee/internal/flow"
	"github.com/tobocop2/obee/internal/merge"
	"github.com/tobocop2/obee/internal/model"
)

func trackerProject(tracker model.TrackerName) config.Project {
	return config.Project{Project: model.Project{Tracker: tracker}}
}

func requestFor(tracker model.TrackerName, id string) merge.Request {
	return pulls{}.request("p", trackerProject(tracker), id, 5, gated, flow.MergeSettings{}, nil)
}

func TestPullsRequest_AGithubItemIdThatIsAPlainPositiveNumberNamesThatIssue(t *testing.T) {
	t.Parallel()

	req := requestFor(model.TrackerGithub, "12")

	assert.True(t, req.AuditClosing)
	assert.Equal(t, []int{12}, req.Names)
	assert.Empty(t, req.Unaudited)
}

func TestPullsMerge_AGithubItemOnAForgeThatCannotReadClosingReferencesIsRefusedAndNothingMerges(t *testing.T) {
	t.Parallel()

	p, f, settings := shipping(t, nil)
	p.projects = map[string]config.Project{"p": {Project: model.Project{Repo: "o/r", Tracker: model.TrackerGithub}}}

	result, err := p.Merge(t.Context(), "p", "12", gated, settings)

	require.NoError(t, err)
	assert.Equal(t, merge.Refused, result.Stop)
	assert.Equal(t, merge.Transient, result.Reason)
	assert.Contains(t, result.Lines[len(result.Lines)-1], "cannot read the issues that #5 closes")
	assert.Empty(t, f.writes)
}

func TestPullsRequest_AnIdThatIsNotAPlainPositiveNumberAuditsNothingAndSaysSo(t *testing.T) {
	t.Parallel()

	for _, id := range []string{"012", "bb-1", "0", "-3", "1a", ""} {
		req := requestFor(model.TrackerGithub, id)

		assert.False(t, req.AuditClosing, id)
		assert.Empty(t, req.Names, id)
		assert.Contains(t, req.Unaudited, "no audit ran", id)
		assert.Contains(t, req.Unaudited, `"`+id+`"`, id)
	}
}

func TestPullsRequest_ANumericIdOfATrackerThatIsNotGithubAuditsNothingAndSaysSo(t *testing.T) {
	t.Parallel()

	for _, tracker := range []model.TrackerName{model.TrackerLocal, model.TrackerBeads} {
		req := requestFor(tracker, "12")

		assert.False(t, req.AuditClosing, tracker)
		assert.Empty(t, req.Names, tracker)
		assert.Contains(t, req.Unaudited, "no audit ran", tracker)
		assert.Contains(t, req.Unaudited, string(tracker), tracker)
	}
}

func TestPullsMerge_AProjectThatTheMapLacksIsRefusedAndNothingMerges(t *testing.T) {
	t.Parallel()

	p, f, settings := shipping(t, nil)
	p.projects = map[string]config.Project{}

	_, err := p.Merge(t.Context(), "ghost", "12", gated, settings)

	require.ErrorIs(t, err, errUnknownProject)
	assert.Empty(t, f.writes)
}
