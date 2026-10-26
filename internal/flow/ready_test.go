package flow

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRun_TheReadyStepRefusesAPullRequestThatIsNotReadyAndNamesTheReason(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		state  PullState
		reason string
	}{
		"a_review_asks_for_changes": {PullState{Number: testPull, ChangesRequested: true}, "pull request 7: a review asks for changes"},
		"a_thread_is_unresolved":    {PullState{Number: testPull, UnresolvedThreads: 2}, "pull request 7: 2 review threads are unresolved"},
		"the_branch_conflicts":      {PullState{Number: testPull, Conflicting: true}, "pull request 7: it conflicts with its base branch"},
		"two_reasons_stand":         {PullState{Number: testPull, ChangesRequested: true, Conflicting: true}, "a review asks for changes, it conflicts with its base branch"},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			harness := newHarness(t)
			harness.forge.state = tt.state
			engine := harness.engine(t)

			_, err := engine.Run(testItem, testProject)

			require.ErrorIs(t, err, ErrNotReady)
			require.ErrorContains(t, err, "ready of item 12")
			require.ErrorContains(t, err, tt.reason)
			assert.Equal(t, []string{"claim:12", "release:12"}, harness.life.seen(), "the pull request is not marked ready, and the claim is still released")
		})
	}
}

func TestRun_TheReadyStepRefusesWhenTheForgeCannotBeRead(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.forge.err = errFake
	engine := harness.engine(t)

	_, err := engine.Run(testItem, testProject)

	require.ErrorIs(t, err, errFake)
	require.ErrorContains(t, err, "read the pull request of item 12")
	assert.Equal(t, []string{"claim:12", "release:12"}, harness.life.seen())
}

func TestRun_TheReadyStepMarksACleanPullRequestReady(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	engine := harness.engine(t)

	_, err := engine.Run(testItem, testProject)

	require.NoError(t, err)
	assert.Equal(t, []string{"claim:12", "ready:12", "release:12"}, harness.life.seen())
	assert.Equal(t, []string{testItem}, harness.forge.asked)
}
