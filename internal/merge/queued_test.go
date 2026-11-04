package merge_test

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/merge"
	"github.com/tobocop2/obee/internal/model"
)

// queuedWorld is a forge that can queue a merge: it adds the optional AutoMergePR and DisableAutoMergePR to the world. States are the answers of the polls,
// oldest first. The last state repeats. A poll is the read of the fields state, headRefOid and headRefName, which no other read of the merge asks for.
type queuedWorld struct {
	*world
	states []string
	head   string
	polls  int
}

func (q *queuedWorld) AutoMergePR(_ context.Context, number int, pin string) error {
	q.writes = append(q.writes, fmt.Sprintf("AutoMergePR %d %s", number, pin))

	return q.fail("AutoMergePR")
}

func (q *queuedWorld) DisableAutoMergePR(_ context.Context, number int) error {
	q.writes = append(q.writes, fmt.Sprintf("DisableAutoMergePR %d", number))

	return q.fail("DisableAutoMergePR")
}

func (q *queuedWorld) PRView(ctx context.Context, number int, fields ...string) (model.PullRequest, error) {
	if !slices.Equal(fields, []string{"state", "headRefOid", "headRefName"}) {
		return q.world.PRView(ctx, number, fields...)
	}

	q.polls++

	state, head, branch := q.states[min(q.polls, len(q.states))-1], q.head, "feat"

	return model.PullRequest{State: &state, HeadRefOid: &head, HeadRefName: &branch}, q.fail("PRView poll")
}

func queued(t *testing.T, states ...string) *queuedWorld {
	t.Helper()

	w := newWorld(t)
	w.stacks[5] = "S1"

	return &queuedWorld{world: w, states: states, head: sha}
}

func queuing() merge.Request {
	req := request()
	req.Queue = true

	return req
}

func (q *queuedWorld) run(req merge.Request) merge.Result {
	return merge.Run(context.Background(), q, req)
}

func (q *queuedWorld) poll(req merge.Request, last bool) merge.Result {
	return merge.Poll(context.Background(), q, req, last)
}

func TestQueued_AStackedPullRequestIsQueuedPinnedAndNotPolledByRun(t *testing.T) {
	t.Parallel()

	q := queued(t, "OPEN")
	result := q.run(queuing())

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Equal(t, merge.Queued, result.Reason)
	assert.Equal(t, []string{"AutoMergePR 5 " + sha}, q.writes, "one queued merge, pinned to the gated head, and no direct merge")
	assert.Zero(t, q.polls)
}

func TestQueued_AFailedQueueCallIsTransient(t *testing.T) {
	t.Parallel()

	q := queued(t, "OPEN")
	q.fails["AutoMergePR"] = true
	result := q.run(queuing())

	assert.Equal(t, merge.Transient, result.Reason)
}

func TestQueued_RunMovesNoChildBeforeTheForgeHasMerged(t *testing.T) {
	t.Parallel()

	q := queued(t, "OPEN")
	q.children = []model.PullRequest{pullJSON(t, map[string]any{"number": 7, "baseRefName": "feat"})}
	req := queuing()
	req.Retarget = true
	result := q.run(req)

	assert.Equal(t, merge.Queued, result.Reason)
	assert.Equal(t, []string{"AutoMergePR 5 " + sha}, q.writes, "the child stays on its old base")
}

func TestPoll_AnOpenPullRequestAtTheGatedHeadStaysQueuedAndMovesNoChild(t *testing.T) {
	t.Parallel()

	q := queued(t, "OPEN")
	q.children = []model.PullRequest{pullJSON(t, map[string]any{"number": 7, "baseRefName": "feat"})}
	req := queuing()
	req.Retarget = true
	result := q.poll(req, false)

	assert.Equal(t, merge.Queued, result.Reason)
	assert.Empty(t, q.writes, "no disarm, no retarget")
}

func TestPoll_AWindowThatEndsUnmergedDisarmsTheQueuedMergeAndLeavesTheChildrenOnTheOldBase(t *testing.T) {
	t.Parallel()

	q := queued(t, "OPEN")
	q.children = []model.PullRequest{pullJSON(t, map[string]any{"number": 7, "baseRefName": "feat"})}
	req := queuing()
	req.Retarget = true
	result := q.poll(req, true)

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Equal(t, merge.Transient, result.Reason)
	assert.Equal(t, []string{"DisableAutoMergePR 5"}, q.writes, "the queued merge is turned off and no child moved")
}

func TestPoll_AMergedPullRequestMovesItsChildrenThenLandsOnTheBase(t *testing.T) {
	t.Parallel()

	q := queued(t, "MERGED")
	q.children = []model.PullRequest{pullJSON(t, map[string]any{"number": 7, "baseRefName": "feat"})}
	req := queuing()
	req.Retarget = true
	result := q.poll(req, false)

	assert.Equal(t, merge.Done, result.Stop, result.Lines)
	assert.Equal(t, []string{"SetBase 7 main"}, q.writes)
	assert.Contains(t, result.Lines, "MERGED #5 at "+sha+"; main tip is now "+tipSHA)
}

func TestPoll_AMergedPullRequestMustStillLandOnTheBase(t *testing.T) {
	t.Parallel()

	q := queued(t, "MERGED")
	q.behind = true
	result := q.poll(queuing(), false)

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Contains(t, result.Lines[len(result.Lines)-1], "did not land")
}

// blind is a queued forge that cannot compare commits: it lists only the methods of Forge and Queuer.
type blind struct {
	merge.Forge
	merge.Queuer
}

func TestPoll_AForgeThatCannotCompareCommitsRefusesAMergedPullRequest(t *testing.T) {
	t.Parallel()

	q := queued(t, "MERGED")
	result := merge.Poll(context.Background(), blind{q, q}, queuing(), false)

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Contains(t, result.Lines[len(result.Lines)-1], "cannot compare commits")
}

func TestPoll_AMergedPullRequestWhoseChildrenCannotBeReadIsRefused(t *testing.T) {
	t.Parallel()

	q := queued(t, "MERGED")
	q.fails["PRList"] = true
	req := queuing()
	req.Retarget = true
	result := q.poll(req, false)

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Contains(t, result.Lines[len(result.Lines)-1], "stacked children")
}

func TestPoll_AChildThatCannotMoveAfterTheMergeSaysTheParentIsMerged(t *testing.T) {
	t.Parallel()

	q := queued(t, "MERGED")
	q.children = []model.PullRequest{pullJSON(t, map[string]any{"number": 7, "baseRefName": "feat"})}
	q.failBase = map[int]bool{7: true}
	req := queuing()
	req.Retarget = true
	result := q.poll(req, false)

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Contains(t, result.Lines[len(result.Lines)-1], "#5 is merged.")
}

func TestPoll_AMergedHeadThatIsNotTheGatedHeadFailsLoudly(t *testing.T) {
	t.Parallel()

	q := queued(t, "MERGED")
	q.head = moved
	result := q.poll(queuing(), false)

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Equal(t, merge.Plain, result.Reason)
	assert.Contains(t, result.Lines[len(result.Lines)-1], "merged commits that no review saw")
	assert.Empty(t, q.writes, "nothing is disarmed and no child moves: the merge already happened")
}

func TestPoll_AHeadThatMovedDisarmsTheQueuedMergeAndRefuses(t *testing.T) {
	t.Parallel()

	q := queued(t, "OPEN")
	q.head = moved
	result := q.poll(queuing(), false)

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Equal(t, merge.Plain, result.Reason)
	assert.Contains(t, result.Lines, "REFUSE: head moved to "+moved+" while the merge of #5 was queued. Re-gate.")
	assert.Equal(t, []string{"DisableAutoMergePR 5"}, q.writes)
}

func TestPoll_AClosedPullRequestDisarmsAndRefuses(t *testing.T) {
	t.Parallel()

	q := queued(t, "CLOSED")
	result := q.poll(queuing(), false)

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Equal(t, merge.Plain, result.Reason)
	assert.Contains(t, result.Lines, "REFUSE: #5 is CLOSED now, so its queued merge cannot land.")
	assert.Equal(t, []string{"DisableAutoMergePR 5"}, q.writes)
}

func TestPoll_ARefusalThatCannotDisarmSaysTheMergeMayStillBeArmed(t *testing.T) {
	t.Parallel()

	q := queued(t, "OPEN")
	q.head = moved
	q.fails["DisableAutoMergePR"] = true
	result := q.poll(queuing(), false)

	assert.Equal(t, merge.Plain, result.Reason)
	assert.Contains(t, result.Lines[len(result.Lines)-1], "may still be armed")
}

func TestPoll_AFailedReadKeepsWaitingUntilTheLastRead(t *testing.T) {
	t.Parallel()

	q := queued(t, "OPEN")
	q.fails["PRView poll"] = true

	middle := q.poll(queuing(), false)
	assert.Equal(t, merge.Queued, middle.Reason)
	assert.Empty(t, q.writes)

	final := q.poll(queuing(), true)
	assert.Equal(t, merge.Transient, final.Reason)
	assert.Equal(t, []string{"DisableAutoMergePR 5"}, q.writes, "the last read disarms even when it cannot read")
}

func TestDisarm_TurnsOffTheQueuedMergeOfAnOpenPullRequest(t *testing.T) {
	t.Parallel()

	q := queued(t, "OPEN")
	result := merge.Disarm(context.Background(), q, queuing())

	assert.Equal(t, merge.Done, result.Stop)
	assert.Equal(t, []string{"DisableAutoMergePR 5"}, q.writes)
}

func TestDisarm_ALandedPullRequestIsNotTouched(t *testing.T) {
	t.Parallel()

	q := queued(t, "MERGED")
	result := merge.Disarm(context.Background(), q, queuing())

	assert.Equal(t, merge.Done, result.Stop)
	assert.Empty(t, q.writes, "a merged pull request has no queued merge to turn off")
}

func TestDisarm_AFailedReadStillTurnsOffTheQueuedMerge(t *testing.T) {
	t.Parallel()

	q := queued(t, "MERGED")
	q.fails["PRView poll"] = true
	result := merge.Disarm(context.Background(), q, queuing())

	assert.Equal(t, merge.Done, result.Stop)
	assert.Equal(t, []string{"DisableAutoMergePR 5"}, q.writes, "the state is unknown, so the merge is turned off")
}

func TestDisarm_AWriteThatFailsSaysTheMergeMayStillBeArmed(t *testing.T) {
	t.Parallel()

	q := queued(t, "OPEN")
	q.fails["DisableAutoMergePR"] = true
	result := merge.Disarm(context.Background(), q, queuing())

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Contains(t, result.Lines[len(result.Lines)-1], "may still be armed")
}

func TestDisarm_AForgeThatCannotQueueRefuses(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	result := merge.Disarm(context.Background(), w, queuing())

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Empty(t, w.writes)
}

func TestPoll_AForgeThatCannotQueueRefuses(t *testing.T) {
	t.Parallel()

	result := merge.Poll(context.Background(), newWorld(t), queuing(), false)

	assert.Equal(t, merge.Refused, result.Stop)
}

func TestQueued_ACrashAfterTheQueueThenAResumeThatFindsMergedLandsThroughTheIntent(t *testing.T) {
	t.Parallel()

	q := queued(t, "OPEN")
	q.set(t, map[string]any{"state": "MERGED", "mergeable": "UNKNOWN", "mergedAt": intentTime.Add(time.Minute).Format(time.RFC3339)})
	q.children = []model.PullRequest{pullJSON(t, map[string]any{"number": 7, "baseRefName": "feat"})}
	req := queuing()
	req.Intent = intentFor()
	req.Retarget = true
	result := q.run(req)

	assert.Equal(t, merge.Done, result.Stop, result.Lines)
	assert.Equal(t, []string{"SetBase 7 main"}, q.writes, "the forge's merge counts as ours, the queue is not called again, and the child moves")
}

func TestQueued_AReplayWhoseChildCannotMoveIsRefused(t *testing.T) {
	t.Parallel()

	q := queued(t, "OPEN")
	q.set(t, map[string]any{"state": "MERGED", "mergeable": "UNKNOWN", "mergedAt": intentTime.Add(time.Minute).Format(time.RFC3339)})
	q.children = []model.PullRequest{pullJSON(t, map[string]any{"number": 7, "baseRefName": "feat"})}
	q.failBase = map[int]bool{7: true}
	req := queuing()
	req.Intent = intentFor()
	req.Retarget = true
	result := q.run(req)

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Contains(t, result.Lines[len(result.Lines)-1], "cannot move #7")
}

func TestQueued_WithoutAnIntentAMergedPullRequestStillRefuses(t *testing.T) {
	t.Parallel()

	q := queued(t, "OPEN")
	q.set(t, map[string]any{"state": "MERGED"})
	result := q.run(queuing())

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Contains(t, result.Lines[len(result.Lines)-1], "state is MERGED")
	assert.Empty(t, q.writes)
}

func TestQueued_NoQueueSettingKeepsTheRefusal(t *testing.T) {
	t.Parallel()

	q := queued(t, "MERGED")
	result := q.run(request())

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Contains(t, result.Lines[len(result.Lines)-1], "REFUSE: #5 is in stack S1.")
	assert.Empty(t, q.writes)
}

func TestQueued_AForgeThatCannotQueueKeepsTheRefusal(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.stacks[5] = "S1"
	result := w.run(queuing())

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Contains(t, result.Lines[len(result.Lines)-1], "REFUSE: #5 is in stack S1.")
	require.Empty(t, w.writes)
}

func TestQueued_APullRequestOutsideAStackStillMergesDirectly(t *testing.T) {
	t.Parallel()

	q := queued(t, "OPEN")
	q.stacks[5] = ""
	result := q.run(queuing())

	assert.Equal(t, merge.Done, result.Stop)
	assert.Equal(t, []string{"MergePR 5 " + sha}, q.writes)
	assert.Zero(t, q.polls)
}
