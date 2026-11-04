package merge_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/obee/internal/merge"
	"github.com/tobocop2/obee/internal/model"
)

func approval(commit string) model.Review {
	return model.Review{User: model.Account{Login: "rev"}, State: model.ReviewApproved, CommitID: commit}
}

func TestRun_AMergedHeadThatIsNotOnTheBaseFailsTheItem(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.behind = true
	result := w.run(request())

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Equal(t, merge.TreeProof, result.Reason)
	assert.Contains(t, last(result), "is not on main")
	assert.Contains(t, last(result), "ahead by 4, behind by 2", "the refusal prints both counts and not the bare word")
	assert.True(t, w.called("Compare "+sha+" main"))
}

// noHistory is a forge that cannot compare commits: the embedded interface hides Compare.
type noHistory struct{ merge.Forge }

func TestRun_AForgeThatCannotCompareCommitsFailsTheItem(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	result := merge.Run(context.Background(), noHistory{w}, request())

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Contains(t, last(result), "cannot prove that the merge of #5 landed")
	assert.Empty(t, w.writes, "no SetDraft and no MergePR may happen")
}

func TestRun_AMergedHeadThatIsOnTheBasePasses(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	result := w.run(request())

	assert.Equal(t, merge.Done, result.Stop)
	assert.True(t, w.called("Compare "+sha+" main"))
}

func TestRun_AnAncestryThatCannotBeReadFailsTheItem(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.ancestryErr = true
	result := w.run(request())

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Equal(t, merge.TreeProof, result.Reason)
	assert.Contains(t, last(result), "cannot tell whether")
}

func TestRun_AnApprovalOfAnOlderCommitRefusesBeforeAnyWrite(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.reviews = []model.Review{approval(moved)}
	result := w.run(request())

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Contains(t, last(result), "approved "+moved)
	assert.Empty(t, w.writes)
}

func TestRun_AnApprovalOfTheHeadMerges(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.reviews = []model.Review{approval(moved), approval(sha)}

	assert.Equal(t, merge.Done, w.run(request()).Stop)
	assert.Equal(t, []string{"MergePR 5 " + sha}, w.writes)
}

func TestRun_AnApprovalWithNoCommitRefuses(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.reviews = []model.Review{approval("")}

	assert.Equal(t, merge.Refused, w.run(request()).Stop)
	assert.Empty(t, w.writes)
}

func TestRun_AReviewListThatCannotBeReadRefuses(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.fails["Reviews"] = true

	assert.Equal(t, merge.Refused, w.run(request()).Stop)
	assert.Empty(t, w.writes)
}

func TestRun_AnUnapprovedPullRequestIsNotJudgedByTheCommitGuard(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.reviews = []model.Review{{State: model.ReviewCommented, CommitID: moved}}

	assert.Equal(t, merge.Done, w.run(request()).Stop)
}

func TestRun_ADraftThatWasMergedOrClosedBeforeReadyingStopsBeforeTheWrite(t *testing.T) {
	t.Parallel()

	for _, state := range []string{"MERGED", "CLOSED"} {
		w := newWorld(t).set(t, map[string]any{"isDraft": true})
		later := pullJSON(t, map[string]any{"state": state, "isDraft": true})
		w.later = &later
		req := request()
		req.AllowReady = true
		result := w.run(req)

		assert.Equal(t, merge.Refused, result.Stop, state)
		assert.Contains(t, last(result), state, state)
		assert.Empty(t, w.writes, state)
	}
}

func TestRun_APullRequestThatTurnedDraftBeforeTheMergeCallStopsBeforeIt(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	later := pullJSON(t, map[string]any{"isDraft": true})
	w.later = &later
	result := w.run(request())

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Contains(t, last(result), "draft")
	assert.Empty(t, merges(w))
}

func TestRun_APullRequestThatWasMergedOrClosedBeforeTheMergeCallStopsBeforeIt(t *testing.T) {
	t.Parallel()

	for _, state := range []string{"MERGED", "CLOSED"} {
		w := newWorld(t)
		later := pullJSON(t, map[string]any{"state": state})
		w.later = &later
		result := w.run(request())

		assert.Equal(t, merge.Refused, result.Stop, state)
		assert.Contains(t, last(result), state, state)
		assert.Empty(t, merges(w), state)
	}
}
