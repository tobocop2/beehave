package review_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/review"
)

func sweepOf(t *testing.T, w *world, s review.Settings) review.Sweeping {
	t.Helper()

	s.Me = me

	got, err := review.Sweep(context.Background(), w, w, s)
	require.NoError(t, err)

	return got
}

func sweepRow(t *testing.T, w *world, s review.Settings) string {
	t.Helper()

	got := sweepOf(t, w, s)
	require.Len(t, got.Rows, 1)

	return got.Rows[0].String()
}

func swept(base string, over func(*model.OwnPull)) *world {
	w := newWorld()
	w.own = []model.OwnPull{{Number: 2, Base: base, ChangesKnown: true, ThreadsKnown: true}}

	if over != nil {
		over(&w.own[0])
	}

	return w
}

func TestSweep_ACleanPullRequestHasAPlainRowUnderTheHeader(t *testing.T) {
	t.Parallel()

	got := sweepOf(t, swept("main", nil), review.Settings{DefaultBranch: "main"})
	assert.Equal(t, "PR      DRAFT     REVIEWS                UNRESOLVED THREADS", review.SweepHeader)
	assert.Equal(t, "#2      false     -                      0", got.Rows[0].String())
	assert.Empty(t, got.Warnings)
}

func TestSweep_AStandingChangeRequestAndOpenThreadsAreFlagged(t *testing.T) {
	t.Parallel()

	w := swept("main", func(p *model.OwnPull) {
		p.ChangeRequests = []model.ChangeRequest{{Login: "rev", Commit: "eeeeeeeeeeeeeeeeeeee"}}
		p.OpenThreads = 1
	})
	assert.Equal(t, "#2      false     CR:rev@eeeeeeeee       1 <-- ANSWER THIS <-- 1 OPEN THREAD(S)", sweepRow(t, w, review.Settings{DefaultBranch: "main"}))
}

func TestSweep_UnknownReviewsAndThreadsShowQuestionMarks(t *testing.T) {
	t.Parallel()

	w := swept("main", func(p *model.OwnPull) { p.ChangesKnown, p.ThreadsKnown = false, false })
	assert.Equal(t, "#2      false     ?                      ? <-- REVIEWS UNREADABLE <-- ? OPEN THREAD(S)", sweepRow(t, w, review.Settings{DefaultBranch: "main"}))
}

func TestSweep_TheBaseFlag(t *testing.T) {
	t.Parallel()

	round := review.Settings{Base: "round", Integration: "round", IntegrationSet: true, DefaultBranch: "main"}

	for name, tc := range map[string]struct {
		base  string
		stack string
		s     review.Settings
		want  string
	}{
		"the integration branch":   {"round", "", round, ""},
		"the default branch":       {"main", "", round, " <-- BASED ON main, NOT THE INTEGRATION BRANCH round"},
		"a trunk, no integration":  {"main", "", review.Settings{DefaultBranch: "main"}, ""},
		"in a stack":               {"feat", "s1", round, ""},
		"in no stack":              {"feat", "", round, " <-- BASED ON feat BUT IN NO STACK"},
		"unknown default":          {"feat", "", review.Settings{Integration: "round", IntegrationSet: true}, " <-- BASED ON feat, NOT THE INTEGRATION BRANCH round (the default branch is unknown; if this is not the default branch, the pull request is in no stack)"},
		"the base names the trunk": {"dev", "", review.Settings{Base: "dev"}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			w := swept(tc.base, nil)
			w.stacks[2] = tc.stack
			assert.Equal(t, "#2      false     -                      0"+tc.want, sweepRow(t, w, tc.s))
		})
	}
}

func TestSweep_AnUnreadableStackIsFlagged(t *testing.T) {
	t.Parallel()

	w := swept("feat", nil)
	w.fails["StackID"] = true
	assert.Equal(t, "#2      false     -                      0 <-- BASED ON feat, STACK UNREADABLE",
		sweepRow(t, w, review.Settings{DefaultBranch: "main"}))
}

func TestSweep_ABaseAndIntegrationBranchThatDifferRefuse(t *testing.T) {
	t.Parallel()

	_, err := review.Sweep(context.Background(), swept("main", nil), swept("main", nil),
		review.Settings{Me: me, Base: "a", Integration: "b", IntegrationSet: true})
	require.ErrorContains(t, err, "the base branch (a) and the integration branch (b) differ")
}

func TestSweep_TheDefaultBranchIsLookedUpOnlyWhenNeeded(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		s       review.Settings
		lookups int32
	}{
		"named":                {review.Settings{DefaultBranch: "main"}, 0},
		"a base and no round":  {review.Settings{Base: "dev"}, 0},
		"an integration":       {review.Settings{Integration: "round", IntegrationSet: true}, 1},
		"neither base nor one": {review.Settings{}, 1},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			w := swept("feat", nil)
			w.defaultBranch = "main"

			got := sweepOf(t, w, tc.s)
			assert.Len(t, got.Rows, 1)
			assert.Equal(t, tc.lookups, w.lookups.Load(), "the forge is asked for the default branch only when the check needs it")
		})
	}
}

func TestSweep_AnEmptyIntegrationBranchWarnsAndOneThatIsTheDefaultWarns(t *testing.T) {
	t.Parallel()

	got := sweepOf(t, swept("main", nil), review.Settings{IntegrationSet: true, DefaultBranch: "main"})
	assert.Equal(t, []string{"WARNING: the integration branch is empty, so the integration-branch check is OFF. Leave it unset, or name the branch."}, got.Warnings)

	got = sweepOf(t, swept("main", nil), review.Settings{Integration: "main", IntegrationSet: true, DefaultBranch: "main"})
	assert.Equal(t, []string{"WARNING: the integration branch is the default branch main, so the integration-branch check is OFF. Name a branch that is not the default branch."}, got.Warnings)
}

func TestSweep_AFailedReadIsAnUnreadError(t *testing.T) {
	t.Parallel()

	w := swept("main", nil)
	w.fails["OwnPulls"] = true

	_, err := review.Sweep(context.Background(), w, w, review.Settings{Me: me})
	require.ErrorIs(t, err, errDown)
}

func TestSweep_TheJSONHoldsTheRowsAsTheyPrint(t *testing.T) {
	t.Parallel()

	data, err := json.Marshal(sweepOf(t, swept("main", nil), review.Settings{DefaultBranch: "main"}).Rows)
	require.NoError(t, err)
	assert.JSONEq(t, `[{"number":2,"draft":false,"reviews":"-","threads":"0"}]`, string(data))
}

func TestSweep_ADefaultBranchThatCannotBeReadIsUnknown(t *testing.T) {
	t.Parallel()

	w := swept("feat", nil)
	w.fails["DefaultBranch"] = true

	got := sweepOf(t, w, review.Settings{Integration: "round", IntegrationSet: true})
	assert.Empty(t, got.Warnings)
	assert.Contains(t, got.Rows[0].Flag, "the default branch is unknown")
}
