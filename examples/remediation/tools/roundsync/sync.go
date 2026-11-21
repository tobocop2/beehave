// Package roundsync brings the trunk into the round branch when the trunk moves. The forge does each merge, so nothing is checked out.
package roundsync

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/tobocop2/beehave/internal/forge"
	"github.com/tobocop2/beehave/internal/model"
)

const (
	// listLimit is the most pull requests one list reads.
	listLimit = 10
	// scanLimit is the most open pull requests of the round base that the search for older sync pull requests reads.
	scanLimit = 200
	// shortLength is the number of characters of the trunk tip in the name of the sync branch.
	shortLength = 8
	// draftLimit is the most time that the return of a pull request to draft may take after a failed or canceled merge.
	draftLimit = 30 * time.Second
	// ReasonRefused is the reason of a sync that the forge refuses for the workflow permission.
	ReasonRefused = "sync-refused"
)

// State is how a call ended.
type State string

// The states of a call.
const (
	Current   State = "current"
	Synced    State = "synced"
	Conflict  State = "conflict"
	Refused   State = "refused"
	Landed    State = "landed"
	Stale     State = "stale"
	HeadMoved State = "head-moved"
)

// Forge is the forge the sync works on.
type Forge interface {
	PRView(ctx context.Context, pr int, fields ...string) (model.PullRequest, error)
	PRList(ctx context.Context, limit int, args ...string) ([]model.PullRequest, error)
	BranchRef(ctx context.Context, branch string) (sha string, found bool, err error)
	CreateBranch(ctx context.Context, branch, sha string) error
	CreatePR(ctx context.Context, req model.NewPull) (int, error)
	ClosePR(ctx context.Context, pr int, comment string) error
	MergePR(ctx context.Context, pr int, sha string) error
	SetDraft(ctx context.Context, pr int, draft bool) error
	CommitsAhead(ctx context.Context, base, head string) (int, error)
	MergeBranch(ctx context.Context, base, head string) (string, error)
}

// Request is one check: the round base, the trunk and the label of the pull request that the check opens. An empty Label adds none.
type Request struct {
	Base  string
	Trunk string
	Label string
}

// LandRequest is one landing: the pull request, the head that the gate ran on and the base tip that the sync recorded.
type LandRequest struct {
	PR      int
	Head    string
	BaseTip string
}

// Result is how a call ended. A field that the state does not use is empty.
// Source is the branch whose merge into the sync branch conflicted: the trunk or the round base.
type Result struct {
	State   State
	Branch  string
	Source  string
	PR      int
	Head    string
	BaseTip string
	Reason  string
}

// Stops says that the workflow must not go on: the pull request is not landed and the call is a refusal.
func (r Result) Stops() bool {
	return r.State == Stale || r.State == HeadMoved
}

// Line is the one line of output: state first, then each field that is set.
func (r Result) Line() string {
	fields := []string{"state=" + string(r.State)}

	for _, field := range []struct{ key, value string }{
		{"branch", r.Branch}, {"source", r.Source}, {"head", r.Head}, {"base_tip", r.BaseTip}, {"reason", r.Reason},
	} {
		if field.value != "" {
			fields = append(fields, field.key+"="+field.value)
		}
	}

	if r.PR > 0 {
		fields = append(fields, "pr="+strconv.Itoa(r.PR))
	}

	return strings.Join(fields, " ")
}

// Branch is the name of the sync branch: it holds the round base and the first characters of the trunk tip.
func Branch(base, trunkTip string) string {
	return "sync/" + base + "/" + trunkTip[:min(len(trunkTip), shortLength)]
}

// syncer holds the state of one call. limit is the most time that the return to draft of a landing may take.
type syncer struct {
	forge Forge
	req   Request
	limit time.Duration
}

// Check finds out whether the trunk has commits that the round base lacks. If it has, the check merges the trunk into the sync branch and opens the pull request of the sync.
// A trunk with nothing new writes nothing. A conflict leaves the sync branch at the round base tip and opens no pull request.
// An open sync pull request that is ready is made a draft first, before any merge into the sync branch, so a ready mark that a stopped landing left never carries a new head.
func Check(ctx context.Context, forge Forge, req Request) (Result, error) {
	s := &syncer{forge: forge, req: req}

	ahead, err := forge.CommitsAhead(ctx, req.Base, req.Trunk)
	if err != nil {
		return Result{}, err
	}

	if ahead == 0 {
		return Result{State: Current}, nil
	}

	return s.sync(ctx)
}

func (s *syncer) sync(ctx context.Context) (Result, error) {
	baseTip, trunkTip, err := s.tips(ctx, s.req.Base, s.req.Trunk)
	if err != nil {
		return Result{}, err
	}

	branch := Branch(s.req.Base, trunkTip)
	if err := s.ensureBranch(ctx, branch, baseTip); err != nil {
		return Result{}, err
	}

	number, err := s.draftPull(ctx, branch)
	if err != nil {
		return Result{}, err
	}

	if stop, done, err := s.mergeAll(ctx, branch, baseTip); done || err != nil {
		return stop, err
	}

	return s.open(ctx, branch, baseTip, number)
}

// draftPull finds the open pull request of the sync branch, or 0 when there is none, and makes it a draft when it is ready.
// A pull request that the gh answer does not mark as a draft counts as ready.
func (s *syncer) draftPull(ctx context.Context, branch string) (int, error) {
	rows, err := s.forge.PRList(ctx, listLimit, "--state", "open", "--head", branch, "--base", s.req.Base, "--json", "number,isDraft")
	if err != nil || len(rows) == 0 {
		return 0, err
	}

	pull := rows[0]
	if pull.IsDraft == nil || !*pull.IsDraft {
		if err := s.forge.SetDraft(ctx, pull.Number, true); err != nil {
			return 0, err
		}
	}

	return pull.Number, nil
}

// tip is the tip of a branch. A branch that is missing is an error.
func (s *syncer) tip(ctx context.Context, branch string) (string, error) {
	sha, found, err := s.forge.BranchRef(ctx, branch)
	if err != nil {
		return "", err
	}

	if !found {
		return "", forge.Refuse("cannot find branch %s", branch)
	}

	return sha, nil
}

// ensureBranch cuts the sync branch at the round base tip when it does not exist. A repeat call finds it.
func (s *syncer) ensureBranch(ctx context.Context, branch, baseTip string) error {
	_, found, err := s.forge.BranchRef(ctx, branch)
	if err != nil || found {
		return err
	}

	return s.forge.CreateBranch(ctx, branch, baseTip)
}

// mergeAll brings the trunk and then the round base into the sync branch. A source that the branch already holds is not merged again.
// The round base goes in so the head that the gate runs on holds every commit of the base that the landing keeps.
// done is true when the call ends here, with the result to show.
func (s *syncer) mergeAll(ctx context.Context, branch, baseTip string) (result Result, done bool, err error) {
	for _, source := range []string{s.req.Trunk, s.req.Base} {
		ahead, err := s.forge.CommitsAhead(ctx, branch, source)
		if err != nil {
			return Result{}, false, err
		}

		if ahead == 0 {
			continue
		}

		if _, err := s.forge.MergeBranch(ctx, branch, source); err != nil {
			return s.refusal(err, branch, source, baseTip)
		}
	}

	return Result{}, false, nil
}

// refusal turns a failed merge of source into the result that ends the call. A conflict and a refusal for the workflow permission are states. Any other failure is an error.
func (s *syncer) refusal(cause error, branch, source, baseTip string) (Result, bool, error) {
	switch {
	case errors.Is(cause, forge.ErrMergeConflict):
		return Result{State: Conflict, Branch: branch, Source: source, BaseTip: baseTip}, true, nil
	case errors.Is(cause, forge.ErrWorkflowPermission):
		return Result{State: Refused, Branch: branch, BaseTip: baseTip, Reason: ReasonRefused}, true, nil
	default:
		return Result{}, false, cause
	}
}

// open opens the pull request of the sync branch as a draft when number is 0, and shows the head that the gate must run on.
func (s *syncer) open(ctx context.Context, branch, baseTip string, number int) (Result, error) {
	if number == 0 {
		created, err := s.create(ctx, branch)
		if err != nil {
			return Result{}, err
		}

		number = created
	}

	head, err := s.tip(ctx, branch)
	if err != nil {
		return Result{}, err
	}

	if err := s.closeOlder(ctx, branch, number); err != nil {
		return Result{}, err
	}

	return Result{State: Synced, Branch: branch, PR: number, Head: head, BaseTip: baseTip}, nil
}

// closeOlder closes every other open sync pull request into the round base, not only older ones: branch names carry no order. The match is the prefix sync/<base>/, and the comment names the pull request that replaces it.
// It runs on every call that reaches the open pull request, so a close that failed is tried again by the next call. A failed close is an error.
func (s *syncer) closeOlder(ctx context.Context, branch string, replacement int) error {
	rows, err := s.forge.PRList(ctx, scanLimit, "--state", "open", "--base", s.req.Base, "--json", "number,headRefName")
	if err != nil {
		return err
	}

	prefix := "sync/" + s.req.Base + "/"

	for _, row := range rows {
		if row.HeadRefName == nil || *row.HeadRefName == branch || !strings.HasPrefix(*row.HeadRefName, prefix) {
			continue
		}

		comment := fmt.Sprintf("Closed: pull request #%d replaces this sync, which is for an older tip of %s.", replacement, s.req.Trunk)
		if err := s.forge.ClosePR(ctx, row.Number, comment); err != nil {
			return err
		}
	}

	return nil
}

func (s *syncer) create(ctx context.Context, branch string) (int, error) {
	return s.forge.CreatePR(ctx, model.NewPull{
		Head:  branch,
		Base:  s.req.Base,
		Title: fmt.Sprintf("Merge %s into %s", s.req.Trunk, s.req.Base),
		Body:  fmt.Sprintf("Brings the commits of %s that %s lacks into the round.", s.req.Trunk, s.req.Base),
		Label: s.req.Label,
	})
}

// Land merges the pull request of a sync with a merge commit, pinned to the head that the gate ran on.
// It lands only when the round base is still at the tip that the sync recorded, so the base tree after the merge equals the gated tree.
// A moved base is the state Stale and a moved head is the state HeadMoved. Neither writes.
// The sync opens a draft, which the forge does not merge, so the landing marks it ready just before the merge. A merge that fails or is canceled makes it a draft again,
// and a failed return to draft is part of the error. A kill of the process can leave it ready, until a Check on the same sync branch makes it a draft.
func Land(ctx context.Context, forge Forge, req LandRequest) (Result, error) {
	return landWithin(ctx, forge, req, draftLimit)
}

// landWithin is Land with limit as the most time that the return to draft may take.
func landWithin(ctx context.Context, forge Forge, req LandRequest, limit time.Duration) (Result, error) {
	s := &syncer{forge: forge, limit: limit}

	pull, err := forge.PRView(ctx, req.PR, "state", "baseRefName", "headRefName", "isDraft")
	if err != nil {
		return Result{}, err
	}

	base, head, err := s.openNames(pull, req.PR)
	if err != nil {
		return Result{}, err
	}

	return s.land(ctx, req, base, head, pull.IsDraft != nil && *pull.IsDraft)
}

// openNames reads the base and head branch names of an open pull request.
func (s *syncer) openNames(pull model.PullRequest, number int) (base, head string, err error) {
	if pull.State == nil || *pull.State != "OPEN" || pull.BaseRefName == nil || pull.HeadRefName == nil {
		return "", "", forge.Refuse("pull request #%d is not an open pull request with a base and a head branch", number)
	}

	return *pull.BaseRefName, *pull.HeadRefName, nil
}

// tips reads the tips of two branches.
func (s *syncer) tips(ctx context.Context, first, second string) (firstTip, secondTip string, err error) {
	if firstTip, err = s.tip(ctx, first); err != nil {
		return "", "", err
	}

	if secondTip, err = s.tip(ctx, second); err != nil {
		return "", "", err
	}

	return firstTip, secondTip, nil
}

func (s *syncer) land(ctx context.Context, req LandRequest, base, head string, draft bool) (Result, error) {
	baseTip, headTip, err := s.tips(ctx, base, head)
	if err != nil {
		return Result{}, err
	}

	result := Result{PR: req.PR, Head: headTip, BaseTip: baseTip, Branch: head, State: Stale}
	if baseTip != req.BaseTip {
		return result, nil
	}

	result.State = HeadMoved
	if headTip != req.Head {
		return result, nil
	}

	result.State = Landed

	return result, s.merge(ctx, req, draft)
}

// merge marks a draft ready and merges it pinned to the gated head. A merge that fails or is canceled makes the pull request a draft again.
func (s *syncer) merge(ctx context.Context, req LandRequest, draft bool) error {
	if draft {
		if err := s.forge.SetDraft(ctx, req.PR, false); err != nil {
			return err
		}
	}

	err := s.forge.MergePR(ctx, req.PR, req.Head)
	if err == nil {
		return nil
	}

	return errors.Join(err, backToDraft(ctx, s.forge, req.PR, s.limit))
}

// backToDraft makes a pull request a draft on a context that a cancel of ctx does not end, within limit.
// The merge failed, so the state of the pull request is not known: GitHub can have merged it while gh was stopped.
func backToDraft(ctx context.Context, forge Forge, pr int, limit time.Duration) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), limit)
	defer cancel()

	if err := forge.SetDraft(ctx, pr, true); err != nil {
		return fmt.Errorf("cannot make pull request #%d a draft again after the failed merge. If it is open and ready for review, convert it to a draft by hand: %w", pr, err)
	}

	return nil
}
