// Package preflight asks whether the pull request a caller is about to act on is still the one it expects.
// One read of the pull request and one read of its head branch give a Stop, or none.
package preflight

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/tobocop2/obee/internal/merge"
	"github.com/tobocop2/obee/internal/model"
)

// shaLength is the length of a full commit SHA. A shorter or longer text is never compared.
const shaLength = 40

// fields returns what the checks read from the pull request.
func fields() []string {
	return []string{"number", "state", "isDraft", "mergedAt", "headRefOid", "headRefName", "baseRefName", "labels", "headRepository", "author"}
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
	Draft       Stop = 8
	Held        Stop = 9
	Claimed     Stop = 10
)

// Stops returns every value of Stop.
func Stops() []Stop {
	return []Stop{Unchanged, Refused, Merged, Closed, HeadMoved, BaseChanged, BranchGone, Draft, Held, Claimed}
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
		BaseChanged: "BASE CHANGED", BranchGone: "BRANCH GONE", Draft: "DRAFT", Held: "HELD", Claimed: "CLAIMED",
	}
	if name, ok := names[s]; ok {
		return name
	}

	return "UNKNOWN"
}

// Request names the pull request. ExpectHead is the full SHA the caller expects, ExpectBase the branch it gated against; an empty one is not checked.
// Own lists the logins of the caller. An open pull request by any other login claims the item. An empty list checks no author.
type Request struct {
	PR         int
	ExpectHead string
	ExpectBase string
	Own        []string
}

// Result is the verdict: the stop, a one-line summary of the pull request, and the lines to show.
type Result struct {
	Stop    Stop
	Summary string
	Lines   []string
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
	pr     int
	state  string
	draft  bool
	head   string
	ref    string
	base   string
	labels string
	author string
}

// Check judges the pull request against the request. The order is the state, the branch, the author, the labels, the draft flag, then the head and the base.
// A merged pull request still shows a head that looks fine, so the state comes first.
// A pull request that carries the whole label hold is held. An empty hold holds nothing.
func Check(ctx context.Context, r Reader, req Request, hold string) (Result, error) {
	v, err := read(ctx, r, req.PR)
	if err != nil {
		return Result{}, err
	}

	summary := fmt.Sprintf("pr %d  state %s  draft %t  head %s  ref %s  base %s  labels [%s]",
		v.pr, v.state, v.draft, v.head, v.ref, v.base, v.labels)

	stop, lines, err := judge(ctx, r, v, req, hold)
	if err != nil {
		return Result{}, err
	}

	if stop == Unchanged {
		lines = []string{fmt.Sprintf("OK: #%d is unchanged in every respect this checks.", v.pr)}
	}

	return Result{Stop: stop, Summary: summary, Lines: lines}, nil
}

// judge returns the first stop in the order, or Unchanged. Only the branch check reads the forge again.
func judge(ctx context.Context, r Reader, v view, req Request, hold string) (Stop, []string, error) {
	if stop, lines := stateStop(v); stop != Unchanged {
		return stop, lines, nil
	}

	if stop, lines, err := branchCheck(ctx, r, v); err != nil || stop != Unchanged {
		return stop, lines, err
	}

	if stop, lines := claimStop(v, req.Own); stop != Unchanged {
		return stop, lines, nil
	}

	if stop, lines := labelStop(v, hold); stop != Unchanged {
		return stop, lines, nil
	}

	stop, lines := expectationStop(v, req)

	return stop, lines, nil
}

// read reads the pull request. A missing state, draft flag, branch name or label list is a refusal, because a null would pass as an open, ready pull request.
func read(ctx context.Context, r Reader, pr int) (view, error) {
	pull, err := r.PRView(ctx, pr, fields()...)
	if err != nil {
		return view{}, &ReadError{PR: pr, Cause: err}
	}

	if pull.State == nil || pull.IsDraft == nil || pull.HeadRefName == nil || pull.Labels == nil {
		return view{}, &ReadError{PR: pr}
	}

	return view{
		pr:     pr,
		state:  *pull.State,
		draft:  *pull.IsDraft,
		head:   model.OrNull(text(pull.HeadRefOid)),
		ref:    *pull.HeadRefName,
		base:   model.OrNull(text(pull.BaseRefName)),
		labels: labelNames(pull),
		author: pull.Author.Login,
	}, nil
}

// labelNames joins the label names with commas. A label with no name reads as null.
func labelNames(pull model.PullRequest) string {
	names := make([]string, len(pull.Labels))
	for i, label := range pull.Labels {
		names[i] = model.OrNull(label.Name)
	}

	return strings.Join(names, ",")
}

func text(field *string) string {
	if field == nil {
		return ""
	}

	return *field
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

// claimStop is the stop for an open pull request of a login that is not in own. Somebody else is already on the item.
func claimStop(v view, own []string) (Stop, []string) {
	if len(own) == 0 || merge.IsOwn(v.author, own) {
		return Unchanged, nil
	}

	return Claimed, []string{
		fmt.Sprintf("STOP: #%d is open and its author is '%s', who is not on the list of own logins. The item is claimed.", v.pr, model.OrNull(v.author)),
		"  Recovery: leave the item to its author and move to the next item.",
	}
}

// labelStop holds the hold label and the draft flag. A hold is a product decision opening, so it outranks a draft.
func labelStop(v view, hold string) (Stop, []string) {
	if hold != "" && slices.Contains(strings.Split(v.labels, ","), hold) {
		return Held, []string{
			fmt.Sprintf("STOP: #%d now carries the '%s' label. A product decision is open; nothing merges.", v.pr, hold),
			"  Recovery: write the judgement on the issue and move to the next item.",
		}
	}

	if v.draft {
		return Draft, []string{
			fmt.Sprintf("STOP: #%d is a DRAFT. It is not mergeable and may be mid-rework by its author.", v.pr),
			"  Recovery: review it if you were sent to review it, but do not merge and do not push",
			"  without saying so on the pull request first.",
		}
	}

	return Unchanged, nil
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
