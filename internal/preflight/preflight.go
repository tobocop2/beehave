// Package preflight asks whether the pull request a caller is about to act on is still the one it expects.
// One read of the pull request and one read of its head branch give a Stop, or none.
// The claim stops (assigned, claimed, held and draft) are not here: the library function claim_stop decides them.
package preflight

import (
	"context"
	"fmt"
	"slices"

	"github.com/tobocop2/beehest/internal/model"
)

// shaLength is the length of a full commit SHA. A shorter or longer text is never compared.
const shaLength = 40

// fields returns what the checks read from the pull request.
func fields() []string {
	return []string{"number", "state", "mergedAt", "headRefOid", "headRefName", "baseRefName"}
}

// Reader is the part of the forge that preflight reads. The caller passes the forge of the project.
type Reader interface {
	PRView(ctx context.Context, pr int, fields ...string) (model.PullRequest, error)
	BranchRef(ctx context.Context, branch string) (sha string, found bool, err error)
}

// Stop is the reason a caller must not go on, or Unchanged. The value of a Stop is its exit code.
type Stop uint8

// The closed set of stops. Each value is the exit code of the command that reports it.
const (
	Unchanged   Stop = 0
	Refused     Stop = 2
	Merged      Stop = 3
	Closed      Stop = 4
	HeadMoved   Stop = 5
	BaseChanged Stop = 6
	BranchGone  Stop = 7
)

// Stops returns every value of Stop.
func Stops() []Stop {
	return []Stop{Unchanged, Refused, Merged, Closed, HeadMoved, BaseChanged, BranchGone}
}

// Code returns the exit code of the stop. A value outside the set is Refused.
func (s Stop) Code() int {
	if !slices.Contains(Stops(), s) {
		return int(Refused)
	}

	return int(s)
}

func (s Stop) String() string {
	names := map[Stop]string{
		Unchanged: "UNCHANGED", Refused: "REFUSED", Merged: "MERGED", Closed: "CLOSED", HeadMoved: "HEAD MOVED",
		BaseChanged: "BASE CHANGED", BranchGone: "BRANCH GONE",
	}
	if name, ok := names[s]; ok {
		return name
	}

	return "UNKNOWN"
}

// Request names the pull request. ExpectHead is the full SHA the caller expects, ExpectBase the branch it gated against; an empty one is not checked.
type Request struct {
	PR         int
	ExpectHead string
	ExpectBase string
}

// Result is the verdict: the stop, a one-line summary of the pull request, and the lines to show.
// Pull is the pull request that the verdict judged, with the association of its author when the caller read it. It is zero when there is none.
type Result struct {
	Stop    Stop
	Summary string
	Lines   []string
	Pull    model.Authored
}

// OK reports whether nothing changed.
func (r Result) OK() bool { return r.Stop == Unchanged }

// ReadError is a read that failed or an answer that cannot be used. A caller must not go on.
type ReadError struct {
	PR     int
	Branch string
	Cause  error
}

func (e *ReadError) Error() string {
	if e.Branch != "" {
		return fmt.Sprintf("cannot read the branch %s of #%d", e.Branch, e.PR)
	}

	return fmt.Sprintf("cannot read #%d", e.PR)
}

// Unwrap returns the cause of the failed read.
func (e *ReadError) Unwrap() error { return e.Cause }

// view is the pull request as the checks read it. Every field a check decides on is set.
type view struct {
	pr    int
	state string
	head  string
	ref   string
	base  string
}

// Check judges the pull request against the request. The order is the state, the branch, then the head and the base.
// A merged pull request still shows a head that looks fine, so the state comes first.
func Check(ctx context.Context, r Reader, req Request) (Result, error) {
	v, err := read(ctx, r, req)
	if err != nil {
		return Result{}, err
	}

	summary := fmt.Sprintf("pr %d  state %s  head %s  ref %s  base %s", v.pr, v.state, v.head, v.ref, v.base)

	stop, lines, err := judge(ctx, r, v, req)
	if err != nil {
		return Result{}, err
	}

	return verdict(v, summary, stop, lines), nil
}

// verdict is the result for the first stop.
func verdict(v view, summary string, stop Stop, lines []string) Result {
	if stop == Unchanged {
		lines = []string{fmt.Sprintf("OK: #%d is unchanged in every respect this checks.", v.pr)}
	}

	return Result{Stop: stop, Summary: summary, Lines: lines}
}

// judge returns the first stop in the order, or Unchanged. Only the branch check reads the forge again.
func judge(ctx context.Context, r Reader, v view, req Request) (Stop, []string, error) {
	if stop, lines := stateStop(v); stop != Unchanged {
		return stop, lines, nil
	}

	if stop, lines, err := branchCheck(ctx, r, v); err != nil || stop != Unchanged {
		return stop, lines, err
	}

	stop, lines := expectationStop(v, req)

	return stop, lines, nil
}

// read reads the pull request. A missing state or branch name is a refusal, because a null would pass as an open pull request.
func read(ctx context.Context, r Reader, req Request) (view, error) {
	pr := req.PR

	pull, err := r.PRView(ctx, pr, fields()...)
	if err != nil {
		return view{}, &ReadError{PR: pr, Cause: err}
	}

	if pull.State == nil || pull.HeadRefName == nil {
		return view{}, &ReadError{PR: pr}
	}

	return view{
		pr:    pr,
		state: *pull.State,
		head:  model.OrNull(model.Deref(pull.HeadRefOid)),
		ref:   *pull.HeadRefName,
		base:  model.OrNull(model.Deref(pull.BaseRefName)),
	}, nil
}

func stateStop(v view) (Stop, []string) {
	switch v.state {
	case "MERGED":
		return Merged, []string{
			fmt.Sprintf("STOP: #%d is already MERGED. Anything you push to '%s' now lands on a branch nobody ships.", v.pr, v.ref),
			fmt.Sprintf("  Recovery: branch from the CURRENT tip of '%s', re-apply the work, open a NEW pull request,", v.base),
			fmt.Sprintf("  and say in its body that it continues #%d. Do not reopen a merged pull request.", v.pr),
		}
	case "CLOSED":
		return Closed, []string{
			fmt.Sprintf("STOP: #%d is CLOSED and not merged. Somebody decided against it while you were working.", v.pr),
			"  Recovery: read the closing comment before anything else. A close is a decision, and",
			"  reopening it is the maintainer's call, not the caller's.",
		}
	}

	return Unchanged, nil
}

// branchCheck reads the head branch and returns BranchGone when it is missing.
func branchCheck(ctx context.Context, r Reader, v view) (Stop, []string, error) {
	_, found, err := r.BranchRef(ctx, v.ref)
	if err != nil {
		return Unchanged, nil, &ReadError{PR: v.pr, Branch: v.ref, Cause: err}
	}

	if found {
		return Unchanged, nil, nil
	}

	stop, lines := branchStop(v)

	return stop, lines, nil
}

// branchStop is the stop for a head branch deleted while the pull request stays open. A push would recreate it and detach the work from every review.
func branchStop(v view) (Stop, []string) {
	return BranchGone, []string{
		fmt.Sprintf("STOP: the head branch '%s' no longer exists on the remote.", v.ref),
		"  Recovery: do NOT push, which would recreate it silently. Branch from the current tip of",
		fmt.Sprintf("  '%s', re-apply the work, and open a new pull request.", v.base),
	}
}

// expectationStop compares the head and the base with what the caller expects. Only a full SHA is compared:
// a short one never equals a full one, and a prefix match would pass a truncated one.
func expectationStop(v view, req Request) (Stop, []string) {
	if req.ExpectHead != "" && len(req.ExpectHead) != shaLength {
		return Refused, []string{fmt.Sprintf("REFUSE: the expected head needs the full %d-character SHA, got '%s'.", shaLength, req.ExpectHead)}
	}

	if req.ExpectHead != "" && v.head != req.ExpectHead {
		return HeadMoved, []string{
			fmt.Sprintf("STOP: the head moved. You expected %s; the head is now %s.", req.ExpectHead, v.head),
			"  Recovery: a review of the old SHA does not describe this one. Re-read the diff between",
			"  them and review the delta, and re-run any gate whose result named the old SHA.",
		}
	}

	if req.ExpectBase != "" && v.base != req.ExpectBase {
		return BaseChanged, []string{
			fmt.Sprintf("STOP: the base changed. You gated against '%s'; it now targets '%s'.", req.ExpectBase, v.base),
			fmt.Sprintf("  Recovery: a union gate is a statement about one base. Re-cut it against '%s'.", v.base),
		}
	}

	return Unchanged, nil
}
