package flow

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRun_EveryStepOfAnItemReadsTheProjectOfItsRun(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	engine := harness.engine(t)

	_, err := engine.Run(testItem, "alpha")
	require.NoError(t, err)

	require.NotEmpty(t, harness.agents.seen())

	for _, request := range harness.agents.seen() {
		assert.Equal(t, "alpha", request.Project)
	}

	require.NotEmpty(t, harness.briefs.seen())

	for _, request := range harness.briefs.seen() {
		assert.Equal(t, "alpha", request.Project)
	}

	assert.Equal(t, []string{"alpha", "alpha", "alpha"}, harness.life.projectsSeen())
}

func TestRun_RefusesAnItemWithoutAProject(t *testing.T) {
	t.Parallel()

	engine := newHarness(t).engine(t)

	_, err := engine.Run(testItem, "")

	require.ErrorIs(t, err, errNoProject)
}

func TestRun_AnOpenRunUnderAnotherProjectIsRefusedAndNamesBothProjects(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.verdicts.reviews = changes(6)
	engine := harness.engine(t)
	client := harness.client(t, testVersion)

	first := runInBackground(engine, testItem)
	eventually(t, func() bool { return harness.verdicts.count() == 6 })

	_, err := engine.Run(testItem, "other")

	require.ErrorIs(t, err, errProjectConflict)
	require.ErrorContains(t, err, "project p")
	require.ErrorContains(t, err, "other")
	require.ErrorContains(t, err, "obee cancel 12")

	require.NoError(t, client.Answer(testItem, "go"))
	require.NoError(t, (<-first).err)
}

func TestTick_RenewsTheClaimOfARunThatWaitsForTheOwner(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.verdicts.reviews = changes(6)
	engine := harness.engine(t)
	client := harness.client(t, testVersion)

	done := runInBackground(engine, testItem)
	eventually(t, func() bool { return harness.verdicts.count() == 6 })
	harness.tick(t)

	require.NotEmpty(t, harness.beats.seen())
	assert.Equal(t, "p/"+testOwner+testItem, harness.beats.seen()[0])

	require.NoError(t, client.Answer(testItem, "go"))
	require.NoError(t, (<-done).err)
	assert.Empty(t, engine.holds.snapshot())
}

func TestRun_AFailedRenewalStopsTheRunBeforeItsNextAgentStepAndSaysWhy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		text   string
		err    error
		reason string
	}{
		{"the_tracker_answers_with_a_refusal", "claim taken by another owner", nil, "claim taken by another owner"},
		{"the_tracker_cannot_be_reached", "", errFake, errFake.Error()},
		{"the_refusal_holds_a_secret", "tracker refused " + testSecret, nil, "tracker refused [redacted]"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			harness := newHarness(t)
			harness.verdicts.reviews = changes(6)
			engine := harness.engine(t)
			client := harness.client(t, testVersion)

			done := runInBackground(engine, testItem)
			eventually(t, func() bool { return harness.verdicts.count() == 6 })

			harness.beats.fail(testOwner+testItem, test.text, test.err)
			harness.tick(t)
			require.Empty(t, engine.holds.snapshot())

			before := len(harness.agents.seen())

			require.NoError(t, client.Answer(testItem, "go"))

			outcome := <-done

			require.ErrorIs(t, outcome.err, errClaimLost)
			require.ErrorContains(t, outcome.err, test.reason)
			assert.Len(t, harness.agents.seen(), before)
			assert.Contains(t, harness.life.seen(), "release:12")
		})
	}
}
