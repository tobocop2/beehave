package roundsync_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/examples/remediation/tools/roundsync"
	"github.com/tobocop2/beehave/internal/forge"
	"github.com/tobocop2/beehave/internal/model"
)

const (
	base      = "integration/r1"
	trunk     = "main"
	baseTip   = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	trunkTip  = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	syncName  = "sync/integration/r1/bbbbbbbb"
	mergedTip = "cccccccccccccccccccccccccccccccccccccccc"
)

var (
	errDown = errors.New("down")
	errHung = errors.New("the draft call hung")
)

// world is a fake forge. ahead holds the answer of each compare ("base...head"). fail holds an error for an operation: "Op", "Op:arg" or "Op:arg#n" for the nth call.
// A write is recorded in writes, in order. reads holds the arguments of each list and the fields of each view. A merge sets the compare of its pair to 0 and moves the tip of the target branch.
// cancelOnMerge, when set, is called by the merge of a pull request before it returns, as a signal to the tool does.
type world struct {
	tips          map[string]string
	ahead         map[string]int
	fail          map[string]error
	pulls         []model.PullRequest
	open          []model.PullRequest
	labels        []string
	view          model.PullRequest
	calls         map[string]int
	writes        []string
	reads         []string
	cancelOnMerge context.CancelFunc
	hangDraft     bool
	// deadlines holds the deadline of the context of each call that makes a draft, or the zero time when it has none.
	deadlines []time.Time
}

func newWorld() *world {
	open, name := "OPEN", syncName
	baseName, draft := base, true

	return &world{
		tips:  map[string]string{base: baseTip, trunk: trunkTip},
		ahead: map[string]int{base + "..." + trunk: 1},
		fail:  map[string]error{},
		calls: map[string]int{},
		view:  model.PullRequest{State: &open, BaseRefName: &baseName, HeadRefName: &name, IsDraft: &draft},
	}
}

func (w *world) failIf(op, arg string) error {
	key := op + ":" + arg
	w.calls[key]++

	for _, k := range []string{op, key, fmt.Sprintf("%s#%d", key, w.calls[key])} {
		if err := w.fail[k]; err != nil {
			return err
		}
	}

	return nil
}

func (w *world) PRView(_ context.Context, _ int, fields ...string) (model.PullRequest, error) {
	w.reads = append(w.reads, "view "+strings.Join(fields, ","))

	return w.view, w.failIf("PRView", "")
}

// PRList answers the list of one branch (a call with --head) with pulls, and the scan of the round base for older sync pull requests with open.
func (w *world) PRList(_ context.Context, _ int, args ...string) ([]model.PullRequest, error) {
	w.reads = append(w.reads, "list "+strings.Join(args, " "))

	if slices.Contains(args, "--head") {
		return w.pulls, w.failIf("PRList", "")
	}

	return w.open, w.failIf("PRList", "scan")
}

// ClosePR records a close with its comment.
func (w *world) ClosePR(_ context.Context, pr int, comment string) error {
	if err := w.failIf("ClosePR", strconv.Itoa(pr)); err != nil {
		return err
	}

	w.writes = append(w.writes, fmt.Sprintf("close %d %s", pr, comment))

	return nil
}

func (w *world) BranchRef(_ context.Context, branch string) (string, bool, error) {
	sha, found := w.tips[branch]

	return sha, found, w.failIf("BranchRef", branch)
}

func (w *world) CreateBranch(_ context.Context, branch, sha string) error {
	if err := w.failIf("CreateBranch", branch); err != nil {
		return err
	}

	w.writes = append(w.writes, "branch "+branch+" "+sha)
	w.tips[branch] = sha
	w.ahead[branch+"..."+trunk] = 1

	return nil
}

func (w *world) CreatePR(_ context.Context, req model.NewPull) (int, error) {
	if err := w.failIf("CreatePR", ""); err != nil {
		return 0, err
	}

	w.labels = append(w.labels, req.Label)
	w.writes = append(w.writes, fmt.Sprintf("pr %s %s %s", req.Head, req.Base, req.Title))

	return 7, nil
}

// MergePR refuses a draft, as GitHub does.
func (w *world) MergePR(ctx context.Context, pr int, sha string) error {
	if w.cancelOnMerge != nil {
		w.cancelOnMerge()

		return ctx.Err()
	}

	if err := w.failIf("MergePR", ""); err != nil {
		return err
	}

	if *w.view.IsDraft {
		return forge.Refuse("GraphQL: Pull Request is still a draft (mergePullRequest)")
	}

	w.writes = append(w.writes, fmt.Sprintf("land %d %s", pr, sha))

	return nil
}

// SetDraft fails on a canceled context, as a gh call does. With hangDraft set, a call that makes a draft waits for the end of its context, or fails with errHung after 5 seconds.
func (w *world) SetDraft(ctx context.Context, pr int, draft bool) error {
	if draft {
		deadline, _ := ctx.Deadline()
		w.deadlines = append(w.deadlines, deadline)
	}

	if w.hangDraft && draft {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
			return errHung
		}
	}

	if err := w.failIf("SetDraft", strconv.FormatBool(draft)); err != nil {
		return err
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	*w.view.IsDraft = draft
	w.writes = append(w.writes, fmt.Sprintf("draft %d %t", pr, draft))

	return nil
}

func (w *world) CommitsAhead(_ context.Context, from, head string) (int, error) {
	return w.ahead[from+"..."+head], w.failIf("CommitsAhead", from+"..."+head)
}

func (w *world) MergeBranch(_ context.Context, into, head string) (string, error) {
	if err := w.failIf("MergeBranch", head); err != nil {
		return "", err
	}

	w.writes = append(w.writes, "merge "+into+" "+head)
	w.ahead[into+"..."+head] = 0
	w.tips[into] = mergedTip

	return mergedTip, nil
}

func check(t *testing.T, w *world) (roundsync.Result, error) {
	t.Helper()

	return roundsync.Check(context.Background(), w, roundsync.Request{Base: base, Trunk: trunk})
}

func TestCheck_ATrunkThatHasNothingNewWritesNothing(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.ahead[base+"..."+trunk] = 0

	result, err := check(t, w)

	require.NoError(t, err)
	assert.Equal(t, "state=current", result.Line())
	assert.Empty(t, w.writes)
}

func TestCheck_ACleanMoveCutsTheBranchMergesTheTrunkAndOpensADraftPull(t *testing.T) {
	t.Parallel()

	w := newWorld()

	result, err := check(t, w)

	require.NoError(t, err)
	assert.Equal(t, "state=synced branch="+syncName+" head="+mergedTip+" base_tip="+baseTip+" pr=7", result.Line())
	assert.Equal(t, []string{
		"branch " + syncName + " " + baseTip,
		"merge " + syncName + " " + trunk,
		"pr " + syncName + " " + base + " Merge main into integration/r1",
	}, w.writes)
	assert.Equal(t, []string{
		"list --state open --head " + syncName + " --base " + base + " --json number,isDraft",
		"list --state open --base " + base + " --json number,headRefName",
	}, w.reads)
}

// openSync is an open pull request of the round base with the head branch.
func openSync(number int, head string) model.PullRequest {
	return model.PullRequest{Number: number, HeadRefName: &head}
}

func TestCheck_TheSyncPullRequestCarriesTheLabelOfTheRequest(t *testing.T) {
	t.Parallel()

	w := newWorld()

	_, err := roundsync.Check(context.Background(), w, roundsync.Request{Base: base, Trunk: trunk, Label: "made-by-beehave"})

	require.NoError(t, err)
	assert.Equal(t, []string{"made-by-beehave"}, w.labels)
}

func TestCheck_AnOlderSyncPullRequestOfTheSameBaseIsClosedWithACommentThatNamesTheNewOne(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.open = []model.PullRequest{
		openSync(690, "sync/"+base+"/be3a1b1f"),
		openSync(725, "sync/"+base+"/4f9e577c"),
		openSync(7, syncName),
		openSync(12, "bb-12"),
		openSync(13, "sync/other/base/deadbeef"),
		{Number: 14},
	}

	_, err := check(t, w)

	require.NoError(t, err)
	assert.Equal(t, []string{
		"branch " + syncName + " " + baseTip,
		"merge " + syncName + " " + trunk,
		"pr " + syncName + " " + base + " Merge main into integration/r1",
		"close 690 Closed: pull request #7 replaces this sync, which is for an older tip of main.",
		"close 725 Closed: pull request #7 replaces this sync, which is for an older tip of main.",
	}, w.writes, "the live sync, an item pull request, another base and a pull request with no branch stay open")
}

func TestCheck_ARepeatCallStillClosesAnOlderSyncPullRequestThatAFailedCloseLeftOpen(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.tips[syncName] = mergedTip
	w.ahead[syncName+"..."+trunk] = 0
	w.pulls = []model.PullRequest{draftPull(3)}
	w.open = []model.PullRequest{openSync(690, "sync/"+base+"/be3a1b1f")}

	_, err := check(t, w)

	require.NoError(t, err)
	assert.Equal(t, []string{"close 690 Closed: pull request #3 replaces this sync, which is for an older tip of main."}, w.writes)
}

func TestCheck_AFailedCloseOrScanOfAnOlderSyncPullRequestStopsTheCallWithTheError(t *testing.T) {
	t.Parallel()

	for name, key := range map[string]string{"close": "ClosePR:690", "scan": "PRList:scan"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			w := newWorld()
			w.open = []model.PullRequest{openSync(690, "sync/"+base+"/be3a1b1f")}
			w.fail[key] = errDown

			_, err := check(t, w)

			require.ErrorIs(t, err, errDown)
		})
	}
}

// draftPull is an open sync pull request that is a draft.
func draftPull(number int) model.PullRequest {
	draft := true

	return model.PullRequest{Number: number, IsDraft: &draft}
}

func TestCheck_ARepeatCallFindsTheBranchTheMergeAndThePullRequest(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.tips[syncName] = mergedTip
	w.ahead[syncName+"..."+trunk] = 0
	w.pulls = []model.PullRequest{draftPull(3)}

	result, err := check(t, w)

	require.NoError(t, err)
	assert.Equal(t, roundsync.Result{State: roundsync.Synced, Branch: syncName, PR: 3, Head: mergedTip, BaseTip: baseTip}, result)
	assert.Empty(t, w.writes, "no branch, no merge, no pull request, no draft call")
}

func TestCheck_ABaseThatMovedAfterTheSyncBranchWasCutIsMergedIntoTheBranch(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.tips[syncName] = mergedTip
	w.ahead[syncName+"..."+trunk] = 0
	w.ahead[syncName+"..."+base] = 2
	w.pulls = []model.PullRequest{draftPull(3)}

	_, err := check(t, w)

	require.NoError(t, err)
	assert.Equal(t, []string{"merge " + syncName + " " + base}, w.writes)
}

func TestCheck_AReadySyncPullRequestIsMadeADraftBeforeAMergeMovesItsHead(t *testing.T) {
	t.Parallel()

	for name, pull := range map[string]model.PullRequest{"ready": {Number: 3, IsDraft: new(bool)}, "no_draft_field": {Number: 3}} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			w := newWorld()
			w.tips[syncName] = mergedTip
			w.ahead[syncName+"..."+trunk] = 0
			w.ahead[syncName+"..."+base] = 1
			w.pulls = []model.PullRequest{pull}

			_, err := check(t, w)

			require.NoError(t, err)
			assert.Equal(t, []string{"draft 3 true", "merge " + syncName + " " + base}, w.writes, "the draft call comes before the merge that moves the head")
		})
	}
}

func TestCheck_AFailedDraftCallMergesNothing(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.tips[syncName] = mergedTip
	w.ahead[syncName+"..."+base] = 1
	w.pulls = []model.PullRequest{{Number: 3, IsDraft: new(bool)}}
	w.fail["SetDraft:true"] = errDown

	_, err := check(t, w)

	require.ErrorIs(t, err, errDown)
	assert.Empty(t, w.writes)
}

func TestCheck_AConflictLeavesTheBranchAtTheBaseTipAndOpensNoPull(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.fail["MergeBranch:"+trunk] = &forge.Error{Message: "cannot merge", Err: forge.ErrMergeConflict}

	result, err := check(t, w)

	require.NoError(t, err)
	assert.Equal(t, "state=conflict branch="+syncName+" source="+trunk+" base_tip="+baseTip, result.Line())
	assert.Equal(t, []string{"branch " + syncName + " " + baseTip}, w.writes)
}

func TestCheck_AConflictWithTheMovedBaseNamesTheBaseAsTheSource(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.tips[syncName] = mergedTip
	w.ahead[syncName+"..."+trunk] = 0
	w.ahead[syncName+"..."+base] = 1
	w.fail["MergeBranch:"+base] = &forge.Error{Message: "cannot merge", Err: forge.ErrMergeConflict}

	result, err := check(t, w)

	require.NoError(t, err)
	assert.Equal(t, "state=conflict branch="+syncName+" source="+base+" base_tip="+baseTip, result.Line())
	assert.Empty(t, w.writes, "the trunk is in the branch already, and the conflicting merge writes nothing")
}

func TestCheck_ARefusalForTheWorkflowPermissionIsTheStateRefusedWithItsReason(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.fail["MergeBranch:"+trunk] = &forge.Error{Message: "cannot merge", Err: forge.ErrWorkflowPermission}

	result, err := check(t, w)

	require.NoError(t, err)
	assert.Equal(t, "state=refused branch="+syncName+" base_tip="+baseTip+" reason=sync-refused", result.Line())
}

func TestCheck_AnyOtherFailureIsAnErrorAndNotAState(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"compare_of_the_trunk":     "CommitsAhead",
		"base_tip":                 "BranchRef:" + base,
		"trunk_tip":                "BranchRef:" + trunk,
		"read_of_the_sync_branch":  "BranchRef:" + syncName + "#1",
		"cut_of_the_sync_branch":   "CreateBranch",
		"compare_of_the_branch":    "CommitsAhead:" + syncName + "..." + trunk,
		"merge_of_the_trunk":       "MergeBranch:" + trunk,
		"list_of_pull_requests":    "PRList",
		"open_of_the_pull_request": "CreatePR",
		"read_of_the_head":         "BranchRef:" + syncName + "#2",
	}

	for name, key := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			w := newWorld()
			w.fail[key] = errDown

			_, err := check(t, w)

			require.ErrorIs(t, err, errDown)
		})
	}
}

func TestCheck_AMissingBranchIsAnError(t *testing.T) {
	t.Parallel()

	for _, missing := range []string{base, trunk} {
		w := newWorld()
		delete(w.tips, missing)

		_, err := check(t, w)

		require.ErrorContains(t, err, "cannot find branch "+missing)
	}
}

func TestBranch_HoldsTheBaseAndTheFirstEightCharactersOfTheTrunkTip(t *testing.T) {
	t.Parallel()

	assert.Equal(t, syncName, roundsync.Branch(base, trunkTip))
	assert.Equal(t, "sync/x/abc", roundsync.Branch("x", "abc"))
}

func land(t *testing.T, w *world, head string, tip string) (roundsync.Result, error) {
	t.Helper()

	return roundsync.Land(context.Background(), w, roundsync.LandRequest{PR: 7, Head: head, BaseTip: tip})
}

func TestLand_MergesThePinnedHeadWhenTheBaseTipIsTheRecordedOne(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.tips[syncName] = mergedTip

	result, err := land(t, w, mergedTip, baseTip)

	require.NoError(t, err)
	assert.Equal(t, "state=landed branch="+syncName+" head="+mergedTip+" base_tip="+baseTip+" pr=7", result.Line())
	assert.Equal(t, []string{"draft 7 false", "land 7 " + mergedTip}, w.writes, "the draft of the sync is marked ready, then merged")
}

func TestLand_APullRequestThatIsReadyAlreadyMergesWithNoReadyCall(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.tips[syncName] = mergedTip
	*w.view.IsDraft = false

	_, err := land(t, w, mergedTip, baseTip)

	require.NoError(t, err)
	assert.Equal(t, []string{"land 7 " + mergedTip}, w.writes)
}

func TestLand_AMergeThatFailsPutsTheDraftBack(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.tips[syncName] = mergedTip
	w.fail["MergePR"] = errDown

	_, err := land(t, w, mergedTip, baseTip)

	require.ErrorIs(t, err, errDown)
	assert.Equal(t, []string{"draft 7 false", "draft 7 true"}, w.writes)
	assert.True(t, *w.view.IsDraft, "no person can merge the head that did not land")
}

func TestLand_AFailedReadyOrAFailedReturnToDraftIsAnError(t *testing.T) {
	t.Parallel()

	errBack := errors.New("back")

	w := newWorld()
	w.tips[syncName] = mergedTip
	w.fail["SetDraft:false"] = errDown

	_, err := land(t, w, mergedTip, baseTip)

	require.ErrorIs(t, err, errDown)
	assert.Empty(t, w.writes, "no merge after a failed ready")

	w = newWorld()
	w.tips[syncName] = mergedTip
	w.fail["MergePR"] = errDown
	w.fail["SetDraft:true"] = errBack

	_, err = land(t, w, mergedTip, baseTip)

	require.ErrorIs(t, err, errDown)
	require.ErrorIs(t, err, errBack)
	assert.ErrorContains(t, err, "cannot make pull request #7 a draft again after the failed merge")
}

func TestLand_TheReturnToDraftMayTake30Seconds(t *testing.T) {
	t.Parallel()

	assert.Equal(t, 30*time.Second, roundsync.DraftLimit)
}

func TestLand_TheReturnToDraftOfLandEndsAtTheDraftLimit(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.tips[syncName] = mergedTip
	w.fail["MergePR"] = errDown

	before := time.Now()
	_, err := land(t, w, mergedTip, baseTip)
	after := time.Now()

	require.ErrorIs(t, err, errDown)
	require.Len(t, w.deadlines, 1)
	assert.False(t, w.deadlines[0].Before(before.Add(roundsync.DraftLimit)), "the deadline is at least the draft limit after the call")
	assert.False(t, w.deadlines[0].After(after.Add(roundsync.DraftLimit)), "the deadline is at most the draft limit after the call")
}

func TestLand_AReturnToDraftThatHangsEndsAtItsLimit(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.tips[syncName] = mergedTip
	w.fail["MergePR"] = errDown
	w.hangDraft = true

	_, err := roundsync.LandWithin(context.Background(), w, roundsync.LandRequest{PR: 7, Head: mergedTip, BaseTip: baseTip}, 20*time.Millisecond)

	require.ErrorIs(t, err, errDown)
	require.ErrorIs(t, err, context.DeadlineExceeded, "the limit ends the call, not the hang of the fake")
	assert.NotErrorIs(t, err, errHung)
}

func TestLand_ACancelDuringTheMergeLeavesADraft(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := newWorld()
	w.tips[syncName] = mergedTip
	w.cancelOnMerge = cancel

	_, err := roundsync.Land(ctx, w, roundsync.LandRequest{PR: 7, Head: mergedTip, BaseTip: baseTip})

	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, []string{"draft 7 false", "draft 7 true"}, w.writes)
	assert.True(t, *w.view.IsDraft, "the return to draft runs on a context that the cancel does not end")
}

func TestLand_AReadyPullRequestWhoseMergeFailsIsMadeADraft(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.tips[syncName] = mergedTip
	*w.view.IsDraft = false
	w.fail["MergePR"] = errDown

	_, err := land(t, w, mergedTip, baseTip)

	require.ErrorIs(t, err, errDown)
	assert.Equal(t, []string{"draft 7 true"}, w.writes)
}

func TestLand_ReadsTheDraftStateOfThePullRequest(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.tips[syncName] = mergedTip

	_, err := land(t, w, mergedTip, baseTip)

	require.NoError(t, err)
	assert.Equal(t, []string{"view state,baseRefName,headRefName,isDraft"}, w.reads)
}

func TestLand_AMovedBaseIsStaleAndWritesNothing(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.tips[syncName] = mergedTip

	result, err := land(t, w, mergedTip, trunkTip)

	require.NoError(t, err)
	assert.Equal(t, roundsync.Stale, result.State)
	assert.True(t, result.Stops())
	assert.Empty(t, w.writes)
}

func TestLand_AMovedHeadIsHeadMovedAndWritesNothing(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.tips[syncName] = trunkTip

	result, err := land(t, w, mergedTip, baseTip)

	require.NoError(t, err)
	assert.Equal(t, roundsync.HeadMoved, result.State)
	assert.True(t, result.Stops())
	assert.Empty(t, w.writes)
}

func TestLand_FailuresAreErrors(t *testing.T) {
	t.Parallel()

	for name, key := range map[string]string{
		"view": "PRView", "base_tip": "BranchRef:" + base, "head_tip": "BranchRef:" + syncName, "merge": "MergePR",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			w := newWorld()
			w.tips[syncName] = mergedTip
			w.fail[key] = errDown

			_, err := land(t, w, mergedTip, baseTip)

			require.ErrorIs(t, err, errDown)
		})
	}
}

func TestLand_APullRequestThatIsNotOpenOrHasNoBranchesIsAnError(t *testing.T) {
	t.Parallel()

	merged := "MERGED"

	for name, edit := range map[string]func(*model.PullRequest){
		"merged":   func(p *model.PullRequest) { p.State = &merged },
		"no_state": func(p *model.PullRequest) { p.State = nil },
		"no_base":  func(p *model.PullRequest) { p.BaseRefName = nil },
		"no_head":  func(p *model.PullRequest) { p.HeadRefName = nil },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			w := newWorld()
			edit(&w.view)

			_, err := land(t, w, mergedTip, baseTip)

			require.ErrorContains(t, err, "pull request #7 is not an open pull request")
			assert.Empty(t, w.writes)
		})
	}
}

func TestLand_AMissingBranchIsAnError(t *testing.T) {
	t.Parallel()

	w := newWorld()

	_, err := land(t, w, mergedTip, baseTip)

	require.ErrorContains(t, err, "cannot find branch "+syncName)
}

func TestCheck_ABranchThatOnlySharesTheStartOfTheSyncPrefixIsNotClosed(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.open = []model.PullRequest{
		openSync(15, "sync/integration/r10/deadbeef"),
		openSync(16, "sync/integration/r1"),
	}

	_, err := check(t, w)

	require.NoError(t, err)
	assert.Equal(t, []string{
		"branch " + syncName + " " + baseTip,
		"merge " + syncName + " " + trunk,
		"pr " + syncName + " " + base + " Merge main into integration/r1",
	}, w.writes, "round r10 and a branch with no slash after the base are not sync pull requests of round r1")
}
