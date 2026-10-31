package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/config"
	"github.com/tobocop2/obee/internal/forge"
	"github.com/tobocop2/obee/internal/model"
)

// issueForge is the forge of the tests of the read after a batch.
type issueForge struct {
	forge.Forge

	defaultBranch string
	defaultErr    error
	states        map[int]string
	refs          []int
	pullState     string
	pullErr       error
}

func (f issueForge) DefaultBranch(context.Context) (string, error) {
	return f.defaultBranch, f.defaultErr
}

func (f issueForge) IssueState(_ context.Context, n int) (string, error) {
	state, ok := f.states[n]
	if !ok {
		return "", errors.New("no such issue")
	}

	return state, nil
}

func (f issueForge) ClosingRefs(context.Context, int) (model.ClosingRefs, error) {
	return model.ClosingRefs{Issues: f.refs}, nil
}

func (f issueForge) PRView(_ context.Context, number int, _ ...string) (model.PullRequest, error) {
	if f.pullErr != nil {
		return model.PullRequest{}, f.pullErr
	}

	return model.PullRequest{Number: number, State: &f.pullState}, nil
}

func issueHost(tracker model.TrackerName, f issueForge, forgeErr error) batchHost {
	return batchHost{pulls: pulls{
		projects: map[string]config.Project{"p": {Project: model.Project{Repo: "o/r", Tracker: tracker}}},
		forge:    func(model.Project) (forge.Forge, error) { return f, forgeErr },
	}}
}

func TestBatchHostIssues_ABaseThatIsTheDefaultBranchReadsTheIssueOfEachLandedItem(t *testing.T) {
	t.Parallel()

	host := issueHost(model.TrackerGithub, issueForge{defaultBranch: "main", states: map[int]string{4: "CLOSED", 9: "OPEN"}}, nil)

	got, err := host.Issues(t.Context(), "p", "main", 0, []string{"4", "9"})

	require.NoError(t, err)
	assert.Equal(t, []string{`issue #9 of item 9 is not closed after the batch landed (the forge says "OPEN")`}, got.Findings)
	assert.Empty(t, got.Note)
}

func TestBatchHostIssues_AnIntegrationBranchBaseIsNotReadAndTheNoteSaysWhy(t *testing.T) {
	t.Parallel()

	host := issueHost(model.TrackerGithub, issueForge{defaultBranch: "main", states: map[int]string{4: "OPEN"}}, nil)

	got, err := host.Issues(t.Context(), "p", "integration/r7", 0, []string{"4"})

	require.NoError(t, err)
	assert.Empty(t, got.Findings)
	assert.Equal(t, "issue states: the base integration/r7 is not the default branch main, so the issues stay open until the integration pull request lands. Pass `integration` to read them.", got.Note)
}

func TestBatchHostIssues_AMergedIntegrationPullRequestIsReadAgainstTheLandedItems(t *testing.T) {
	t.Parallel()

	host := issueHost(model.TrackerGithub, issueForge{pullState: "merged", states: map[int]string{4: "CLOSED"}, refs: []int{4, 12}}, nil)

	got, err := host.Issues(t.Context(), "p", "integration/r7", 70, []string{"4"})

	require.NoError(t, err)
	assert.Equal(t, []string{"pull request #70 closes issue #12 and no landed item names it"}, got.Findings)
}

func TestBatchHostIssues_AnIntegrationPullRequestThatIsNotMergedIsNotReadYet(t *testing.T) {
	t.Parallel()

	host := issueHost(model.TrackerGithub, issueForge{pullState: "OPEN"}, nil)

	got, err := host.Issues(t.Context(), "p", "integration/r7", 70, []string{"4"})

	require.NoError(t, err)
	assert.Empty(t, got.Findings)
	assert.Equal(t, "issue states: the integration pull request 70 is OPEN, not MERGED, so the issues are not read yet.", got.Note)
}

func TestBatchHostIssues_AClosedIntegrationPullRequestIsANoteAndAnUnknownStateIsAFinding(t *testing.T) {
	t.Parallel()

	closed := issueHost(model.TrackerGithub, issueForge{pullState: "closed"}, nil)

	got, err := closed.Issues(t.Context(), "p", "integration/r7", 70, []string{"4"})
	require.NoError(t, err)
	assert.Empty(t, got.Findings)
	assert.Equal(t, "issue states: the integration pull request 70 is CLOSED, not MERGED, so the issues are not read yet.", got.Note)

	for _, state := range []string{"", "DRAFTY"} {
		host := issueHost(model.TrackerGithub, issueForge{pullState: state}, nil)

		got, err = host.Issues(t.Context(), "p", "integration/r7", 70, []string{"4"})
		require.NoError(t, err)
		assert.Empty(t, got.Note, "state %q", state)
		assert.Equal(t, []string{fmt.Sprintf("the integration pull request 70 has the state %q, which is not OPEN, CLOSED or MERGED", strings.ToUpper(state))}, got.Findings)
	}
}

func TestBatchHostIssues_AReadThatFailsIsAFindingAndNeverAPass(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		host        batchHost
		integration int
		want        string
	}{
		"the pull request":   {issueHost(model.TrackerGithub, issueForge{pullErr: errors.New("down")}, nil), 70, "cannot read pull request #70: down"},
		"the default branch": {issueHost(model.TrackerGithub, issueForge{defaultErr: errors.New("down")}, nil), 0, "cannot read the default branch: down"},
		"the forge":          {issueHost(model.TrackerGithub, issueForge{}, errors.New("no token")), 0, "cannot read the issues: no token"},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := c.host.Issues(t.Context(), "p", "main", c.integration, []string{"4"})

			require.NoError(t, err)
			assert.Equal(t, []string{c.want}, got.Findings)
		})
	}
}

func TestBatchHostIssues_ATrackerWithNoIssueNumbersReadsNothingAndSaysSo(t *testing.T) {
	t.Parallel()

	host := issueHost(model.TrackerLocal, issueForge{}, nil)

	got, err := host.Issues(t.Context(), "p", "main", 0, []string{"4"})

	require.NoError(t, err)
	assert.Empty(t, got.Findings)
	assert.Equal(t, "issue states: the tracker local of project p has no issue numbers, so no read ran.", got.Note)
}

func TestBatchHostIssues_AProjectThatTheConfigLacksIsAnError(t *testing.T) {
	t.Parallel()

	_, err := issueHost(model.TrackerGithub, issueForge{}, nil).Issues(t.Context(), "other", "main", 0, []string{"4"})

	require.ErrorIs(t, err, errUnknownProject)
}
