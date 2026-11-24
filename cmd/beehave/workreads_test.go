package main

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/forge"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/store"
)

// openItemsTracker is a tracker with two open items, one of them on hold. labels holds the label of each ItemList call.
type openItemsTracker struct {
	store.Tracker

	labels *[]string
}

func (t openItemsTracker) ItemList(_ context.Context, label string) ([]model.Item, error) {
	*t.labels = append(*t.labels, label)

	return []model.Item{{ID: "21", Severity: model.SeverityP1}, {ID: "22", Hold: true}}, nil
}

func queueReadsOver(f forge.Forge, tracker store.Tracker) queueReads {
	projects := map[string]config.Project{"p": {}}

	return queueReads{
		items: trackerItems{projects: projects, tracker: func(context.Context, model.Project) (store.Tracker, error) { return tracker, nil }},
		pulls: pulls{projects: projects, forge: func(model.Project) (forge.Forge, error) { return f, nil }},
	}
}

func TestQueueReads_ItemListListsTheItemsOfTheTrackerOfTheProjectForTheLabel(t *testing.T) {
	t.Parallel()

	var labels []string

	reads := queueReadsOver(nil, openItemsTracker{labels: &labels})

	items, err := reads.ItemList(t.Context(), "p", "round")

	require.NoError(t, err)
	assert.Equal(t, []model.Item{{ID: "21", Severity: model.SeverityP1}, {ID: "22", Hold: true}}, items)
	assert.Equal(t, []string{"round"}, labels)

	_, err = reads.ItemList(t.Context(), "zzz", "")
	require.ErrorIs(t, err, errUnknownProject)
}

func TestQueueReads_OpenPullsListsTheOpenPullRequestsOfTheBaseOfTheForgeOfTheProject(t *testing.T) {
	t.Parallel()

	f := &pullForge{opened: []model.OpenPull{{Number: 7, Branch: "21", Base: "main"}}}

	pullsOpen, err := queueReadsOver(f, nil).OpenPulls(t.Context(), "p", "main")

	require.NoError(t, err)
	assert.Equal(t, []model.OpenPull{{Number: 7, Branch: "21", Base: "main"}}, pullsOpen)
	assert.Equal(t, []string{"main"}, f.openBases)
}

func TestQueueReads_APullRequestWithNoHeadBranchStaysInTheListAndIsNoError(t *testing.T) {
	t.Parallel()

	listed := []model.OpenPull{{Number: 7, Branch: "21", Base: "main"}, {Number: 9, Base: "main"}}
	f := &pullForge{opened: listed, listErr: model.OpenPull{Number: 9, Base: "main"}.Check()}

	got, err := queueReadsOver(f, nil).OpenPulls(t.Context(), "p", "")

	require.NoError(t, err)
	assert.Equal(t, listed, got)
}

func TestQueueReads_SeveralHolesJoinedStayInTheListAndAreNoError(t *testing.T) {
	t.Parallel()

	listed := []model.OpenPull{{Number: 8, Base: "main"}, {Number: 9, Base: "main"}}
	f := &pullForge{opened: listed, listErr: errors.Join(listed[0].Check(), fmt.Errorf("read: %w", listed[1].Check()))}

	got, err := queueReadsOver(f, nil).OpenPulls(t.Context(), "p", "")

	require.NoError(t, err)
	assert.Equal(t, listed, got)
}

func TestQueueReads_AHoleJoinedToARealFailureIsAnErrorAndKeepsNoList(t *testing.T) {
	t.Parallel()

	listed := []model.OpenPull{{Number: 9, Base: "main"}}
	f := &pullForge{opened: listed, listErr: errors.Join(listed[0].Check(), errFake)}

	got, err := queueReadsOver(f, nil).OpenPulls(t.Context(), "p", "")

	require.ErrorIs(t, err, errFake, "the real failure is not lost behind the hole")
	assert.Empty(t, got)
}

func TestQueueReads_PullStateReadsTheStateOfAPullRequestOfAnyState(t *testing.T) {
	t.Parallel()

	state, err := queueReadsOver(&pullForge{state: "MERGED"}, nil).PullState(t.Context(), "p", 7)

	require.NoError(t, err)
	assert.Equal(t, "MERGED", state)
}

func TestQueueReads_APullStateThatCannotBeReadIsAnError(t *testing.T) {
	t.Parallel()

	reads := queueReadsOver(nil, nil)
	reads.pulls.forge = func(model.Project) (forge.Forge, error) { return nil, forge.ErrNoForge }

	_, err := reads.PullState(t.Context(), "p", 7)
	require.ErrorIs(t, err, forge.ErrNoForge)

	_, err = queueReadsOver(stateForge{}, nil).PullState(t.Context(), "p", 7)
	require.ErrorIs(t, err, errNoPullState)

	_, err = queueReadsOver(stateForge{err: errFake}, nil).PullState(t.Context(), "p", 7)
	require.ErrorIs(t, err, errFake)
}

// stateForge shows a pull request with no state, or fails the read.
type stateForge struct {
	forge.Forge

	err error
}

func (f stateForge) PRView(context.Context, int, ...string) (model.PullRequest, error) {
	return model.PullRequest{}, f.err
}

func TestQueueReads_AFailedListOfPullRequestsIsAnError(t *testing.T) {
	t.Parallel()

	f := &pullForge{listErr: errFake}

	_, err := queueReadsOver(f, nil).OpenPulls(t.Context(), "p", "")

	require.ErrorIs(t, err, errFake)
}

func TestQueueReads_AFailedTrackerLookupIsAnError(t *testing.T) {
	t.Parallel()

	reads := queueReadsOver(nil, nil)
	reads.items.tracker = func(context.Context, model.Project) (store.Tracker, error) { return nil, errFake }

	_, err := reads.ItemList(t.Context(), "p", "")
	require.ErrorIs(t, err, errFake)
}

func TestQueueReads_AFailedForgeLookupIsAnError(t *testing.T) {
	t.Parallel()

	reads := queueReadsOver(nil, nil)
	reads.pulls.forge = func(model.Project) (forge.Forge, error) { return nil, errFake }

	_, err := reads.OpenPulls(t.Context(), "p", "")
	require.ErrorIs(t, err, errFake)
}

func TestQueueReads_AProjectWithNoForgeHasNoOpenPullRequests(t *testing.T) {
	t.Parallel()

	reads := queueReadsOver(nil, nil)
	reads.pulls.forge = func(model.Project) (forge.Forge, error) { return nil, forge.ErrNoForge }

	got, err := reads.OpenPulls(t.Context(), "p", "")

	require.NoError(t, err)
	assert.Empty(t, got)
}
