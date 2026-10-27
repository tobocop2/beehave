package flow

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/review"
)

var standingChanges = []model.ChangeRequest{{Login: "rev", Commit: "ccc"}}

func TestRun_TheReadyStepRefusesAPullRequestThatIsNotReadyAndNamesTheReason(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		state  review.Standing
		reason string
	}{
		"a_review_asks_for_changes":     {review.Standing{Number: testPull, ChangeRequests: standingChanges}, "pull request 7: a review asks for changes"},
		"a_thread_is_unresolved":        {review.Standing{Number: testPull, UnresolvedThreads: 2}, "pull request 7: 2 review threads are unresolved"},
		"the_branch_conflicts":          {review.Standing{Number: testPull, Conflicting: true}, "pull request 7: it conflicts with its base branch"},
		"two_reasons_stand":             {review.Standing{Number: testPull, ChangeRequests: standingChanges, Conflicting: true}, "a review asks for changes, it conflicts with its base branch"},
		"a_read_of_the_standing_failed": {review.Standing{Number: testPull, Unread: []string{"review threads"}}, "pull request 7: the review threads could not be read"},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			harness := newHarness(t)
			harness.forge.state = tt.state
			engine := harness.engine(t)

			_, err := engine.Run(testItem, testProject)

			require.ErrorIs(t, err, errNotReady)
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
	assert.Equal(t, []string{testItem, testItem}, harness.forge.asked, "one read for the ready step and one for the number the watch step reads")
}
