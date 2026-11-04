package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/forge"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/store"
)

// openItemsTracker is a tracker with two open items, one of them on hold.
type openItemsTracker struct{ store.Tracker }

func (openItemsTracker) OpenItems(context.Context) ([]model.Item, error) {
	return []model.Item{{ID: "21", Severity: model.SeverityP1}, {ID: "22", Hold: true}}, nil
}

func queueReadsOver(f forge.Forge, tracker store.Tracker) queueReads {
	projects := map[string]config.Project{"p": {}}

	return queueReads{
		items: trackerItems{projects: projects, tracker: func(context.Context, model.Project) (store.Tracker, error) { return tracker, nil }},
		pulls: pulls{projects: projects, forge: func(model.Project) (forge.Forge, error) { return f, nil }},
	}
}

func TestQueueReads_OpenItemsListsTheOpenItemsOfTheTrackerOfTheProject(t *testing.T) {
	t.Parallel()

	reads := queueReadsOver(nil, openItemsTracker{})

	items, err := reads.OpenItems(t.Context(), "p")

	require.NoError(t, err)
	assert.Equal(t, []model.Item{{ID: "21", Severity: model.SeverityP1}, {ID: "22", Hold: true}}, items)

	_, err = reads.OpenItems(t.Context(), "zzz")
	require.ErrorIs(t, err, errUnknownProject)
}

func TestQueueReads_OpenPullsListsTheOpenPullRequestsWithTheirBranchAndLabels(t *testing.T) {
	t.Parallel()

	f := &pullForge{listed: map[string][]model.PullRequest{"open": {{Number: 7}}}}

	pullsOpen, err := queueReadsOver(f, nil).OpenPulls(t.Context(), "p")

	require.NoError(t, err)
	assert.Equal(t, []model.PullRequest{{Number: 7}}, pullsOpen)
	assert.Equal(t, [][]string{{"--state", "open", "--json", "number,headRefName,labels,reviewDecision,author"}}, f.asked)
}

func TestQueueReads_AFailedListOfPullRequestsIsAnError(t *testing.T) {
	t.Parallel()

	f := &pullForge{listErr: errFake}

	_, err := queueReadsOver(f, nil).OpenPulls(t.Context(), "p")

	require.ErrorIs(t, err, errFake)
}

func TestQueueReads_AFailedTrackerLookupIsAnError(t *testing.T) {
	t.Parallel()

	reads := queueReadsOver(nil, nil)
	reads.items.tracker = func(context.Context, model.Project) (store.Tracker, error) { return nil, errFake }

	_, err := reads.OpenItems(t.Context(), "p")
	require.ErrorIs(t, err, errFake)
}

func TestQueueReads_AFailedForgeLookupIsAnError(t *testing.T) {
	t.Parallel()

	reads := queueReadsOver(nil, nil)
	reads.pulls.forge = func(model.Project) (forge.Forge, error) { return nil, errFake }

	_, err := reads.OpenPulls(t.Context(), "p")
	require.ErrorIs(t, err, errFake)
}

func TestQueueReads_AProjectWithNoForgeHasNoOpenPullRequests(t *testing.T) {
	t.Parallel()

	reads := queueReadsOver(nil, nil)
	reads.pulls.forge = func(model.Project) (forge.Forge, error) { return nil, forge.ErrNoForge }

	got, err := reads.OpenPulls(t.Context(), "p")

	require.NoError(t, err)
	assert.Empty(t, got)
}
