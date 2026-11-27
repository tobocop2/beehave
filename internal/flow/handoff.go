package flow

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	"github.com/tobocop2/beehest/internal/landing"
	"github.com/tobocop2/beehest/internal/review"
	"github.com/tobocop2/beehest/internal/workflow"
)

// watchEvery is the time between two reads of the ready pull requests of a hand-off. A day of waiting costs 24 reads and 24 sleeps in the durable history.
const watchEvery = time.Hour

// mergeOwner is the value of the merge event while a hand-off waits for the owner to merge.
const mergeOwner = "owner_merge"

// draftTimeout bounds the reads and the draft calls that put pull requests back to draft after a cancel. They run on a context that the cancel does not reach.
const draftTimeout = 30 * time.Second

// handState says how one read of a member of a hand-off ended. Only handWaiting keeps the member in the watch.
type handState string

const (
	handWaiting handState = "waiting"
	handMerged  handState = "merged"
	handClosed  handState = "closed"
	handMoved   handState = "moved"
	handHeld    handState = "held"
)

// handRead is the record of one read of a member. Head is the head that a handMoved read found, and Reason says why a member left the watch without a merge.
type handRead struct {
	State  handState `json:"state"`
	Head   string    `json:"head,omitempty"`
	Reason string    `json:"reason,omitempty"`
}

// handResult collects how the members of a hand-off ended.
type handResult struct {
	merged   []string
	rejected []landing.Outcome
	moved    []workflow.PullRequest
}

// handOff marks each member ready, tells the owner once, and reads the members until each one is merged, closed or drafted again.
// A member is drafted again when its head moves, or when a change request or a conflict stands. A member that is not ready and open at its head never gets the ready mark,
// and neither does a member with a change request, a conflict or an unresolved thread. The wait between two reads is a durable sleep, so a restart resumes the watch.
// A call that ends for good, by a cancel or by an error, puts each member that is still ready back to draft: nothing watches it any more.
func (s *script) handOff(call batchCall) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	var out handResult

	err := s.handRun(call, &out)
	if err != nil && endsTheMerge(s.ctx) {
		err = errors.Join(err, s.handUnwatch(call, out))
	}

	return handResultValue(out), errors.Join(err, show(s.ctx, s.in.Item, mergeKey, "", "that the wait for the owner is over"))
}

// handRun is the round of the ready mark, the notice to the owner and the watch.
func (s *script) handRun(call batchCall, out *handResult) error {
	pending, err := s.handLook(call, call.members, 0, out)
	if err != nil || len(pending) == 0 {
		return err
	}

	s.handAsk()

	return s.handWatch(call, pending, out)
}

// handUnwatch puts each member that has not ended back to draft. Each member gets a context of its own that the cancel does not reach, with its own limit,
// so a member that is slow takes no time from the next one. It reads the pull request first, so a merged or closed one, and a draft, get no call.
// A read or a draft call that fails tells the owner to convert the pull request by hand, on a context and a limit of its own for the notice.
func (s *script) handUnwatch(call batchCall, out handResult) error {
	var failed []error

	for _, m := range out.open(call.members) {
		failed = append(failed, s.draftMember(call.settings.Repo, m.Item))
	}

	err := errors.Join(failed...)
	if err != nil {
		// A cancel ends the call with the error of the cancel, so the owner reads this one in the notice.
		ctx, cancel := s.draftContext()
		defer cancel()

		s.e.tell(ctx, s.in.Project, s.in.Item, err.Error())
	}

	return err
}

// draftContext is a context that the cancel does not reach, with the limit for one member or for the notice.
func (s *script) draftContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(s.ctx), cmp.Or(s.e.deps.Clock.Draft, draftTimeout))
}

// draftMember puts one member back to draft within the limit of that member.
func (s *script) draftMember(repo, item string) error {
	ctx, cancel := s.draftContext()
	defer cancel()

	return s.e.draftOpen(ctx, s.in.Project, repo, item)
}

// draftOpen puts the pull request of an item back to draft when it is open and ready for review.
func (e *Engine) draftOpen(ctx context.Context, project, repo, item string) error {
	standing, err := e.deps.Batch.Pull(ctx, project, item, repo, "")
	if err == nil && standing.Head == "" && !standing.Merged && !standing.Closed {
		err = errors.New("the forge gave no head, so the pull request was not read")
	}

	if err != nil {
		return fmt.Errorf("cannot read the pull request of item %s to make it a draft again. If it is open and ready for review, convert it to a draft by hand: %w", item, err)
	}

	if standing.Merged || standing.Closed || standing.Draft {
		return nil
	}

	if err := e.deps.Batch.SetDraft(ctx, project, item, repo, true); err != nil {
		return fmt.Errorf("cannot make the pull request of item %s a draft again. If it is open and ready for review, convert it to a draft by hand: %w", item, err)
	}

	return nil
}

// handWatch reads the members that wait, round by round, until none waits. A durable sleep separates two rounds.
func (s *script) handWatch(call batchCall, pending []workflow.PullRequest, out *handResult) error {
	for round := 1; ; round++ {
		var err error
		if pending, err = s.handLook(call, pending, round, out); err != nil || len(pending) == 0 {
			return err
		}

		if _, err = dbos.Sleep(s.ctx, cmp.Or(s.e.deps.Clock.Watch, watchEvery)); err != nil {
			return fmt.Errorf("merge %q waits for the owner of item %s: %w", call.name, s.in.Item, err)
		}
	}
}

// handAsk shows that the run waits for the owner and runs the notify command of the project once.
// A cancel makes the event write fail, and the next read step of the watch reports the same cancel, so the write error is not returned here.
func (s *script) handAsk() {
	_ = show(s.ctx, s.in.Item, mergeKey, mergeOwner, "that it waits for the owner to merge") //nolint:errcheck // the next step of the watch reports a cancel

	s.e.announce(s.ctx, s.in.Project, s.in.Item, fmt.Sprintf("The pull request of item %s is ready. Merge it, or send it back.", s.in.Item))
}

// handLook reads each pending member as one recorded step and returns the members that still wait. Round 0 is the read that marks the member ready.
func (s *script) handLook(call batchCall, pending []workflow.PullRequest, round int, out *handResult) ([]workflow.PullRequest, error) {
	var waiting []workflow.PullRequest

	for _, m := range pending {
		read, err := runStep(s.e, s.ctx, handStepName(call.name, m.Item, round), func(c context.Context) (handRead, error) {
			return s.e.handRead(c, s.in.Project, call.settings.Repo, m, round == 0, s.writes())
		}, dbos.WithStepMaxRetries(stepRetries))
		if err != nil {
			return nil, fmt.Errorf("merge %q: %w", call.name, err)
		}

		if out.add(m, read) {
			waiting = append(waiting, m)
		}
	}

	return waiting, nil
}

// handStepName is the name of the read of a member in a round. Round 0 is the read that marks the member ready.
func handStepName(call, item string, round int) string {
	if round == 0 {
		return fmt.Sprintf("batch:%s.ready#%s", call, item)
	}

	return fmt.Sprintf("batch:%s.watch#%s@%d", call, item, round)
}

// open lists the members that have not ended: they are in no list of the result.
func (r *handResult) open(members []workflow.PullRequest) []workflow.PullRequest {
	return slices.DeleteFunc(slices.Clone(members), func(m workflow.PullRequest) bool {
		return slices.Contains(r.merged, m.Item) || slices.ContainsFunc(r.rejected, func(o landing.Outcome) bool { return o.Item == m.Item })
	})
}

// add keeps the end of a member in the result. It reports whether the member still waits.
func (r *handResult) add(m workflow.PullRequest, read handRead) bool {
	switch read.State {
	case handMerged:
		r.merged = append(r.merged, m.Item)
	case handClosed, handHeld:
		r.rejected = append(r.rejected, landing.Outcome{Item: m.Item, Reason: read.Reason})
	case handMoved:
		r.rejected = append(r.rejected, landing.Outcome{Item: m.Item, Reason: read.Reason})
		r.moved = append(r.moved, workflow.PullRequest{Item: m.Item, Head: read.Head})
	case handWaiting:
	}

	return read.State == handWaiting
}

// handRead reads the pull request of a member once, through the standing review. A merged pull request ends the member. A pull request that is closed ends it with a reason.
// A head that is not the head of the member, and has another tree, puts the pull request back to draft. A head with the same tree is a rewrite and no move.
// With ready set, a draft at the head of the member is marked ready through writes, unless a change request, a conflict or an unresolved thread holds it back.
func (e *Engine) handRead(ctx context.Context, project, repo string, m workflow.PullRequest, ready bool, writes runWrites) (handRead, error) {
	standing, err := e.deps.Batch.Pull(ctx, project, m.Item, repo, m.Head)
	if err != nil {
		return handRead{}, fmt.Errorf("read the pull request of item %s: %w", m.Item, err)
	}

	if ended, over := handEnded(standing); over {
		return ended, nil
	}

	if len(standing.Unread) > 0 || standing.Head == "" {
		return handRead{}, fmt.Errorf("read the pull request of item %s: %s", m.Item, unreadText(standing, m.Head))
	}

	if standing.Head != m.Head && standing.Rewritten != m.Head {
		return e.handMoved(ctx, project, repo, m, standing)
	}

	return e.handStanding(ctx, project, repo, m, standing, ready, writes)
}

// unreadText says why the standing of a pull request is not whole: the reads that failed, or the head that the forge did not give.
func unreadText(standing review.Standing, reviewed string) string {
	if reasons := standing.Reasons(reviewed); len(standing.Unread) > 0 {
		return strings.Join(reasons[:len(standing.Unread)], ", ")
	}

	return "the forge gave no head"
}

// handEnded reads a pull request that is merged, or closed, as the end of the member. over is false for an open one.
func handEnded(standing review.Standing) (read handRead, over bool) {
	switch {
	case standing.Merged:
		return handRead{State: handMerged}, true
	case standing.Closed:
		return handRead{State: handClosed, Reason: "the pull request is closed"}, true
	}

	return handRead{}, false
}

// handStanding acts on a pull request at the head of the member. A draft about to be marked ready, and a ready pull request,
// are held back by a change request or a conflict: the draft stays a draft and the ready one goes back to draft. An unresolved thread holds back the ready mark only.
// A pull request that is a draft during the watch ends the member, whatever made it a draft, and the result says why.
func (e *Engine) handStanding(ctx context.Context, project, repo string, m workflow.PullRequest, standing review.Standing, ready bool, writes runWrites) (handRead, error) {
	marking := ready && standing.Draft
	if !marking {
		standing.UnresolvedThreads = 0
	}

	reasons := strings.Join(standing.Reasons(m.Head), ", ")

	switch {
	case standing.Draft && !marking:
		return handRead{State: handHeld, Reason: cmp.Or(reasons, "it is a draft")}, nil
	case reasons != "":
		return e.handHold(ctx, project, repo, m, standing, reasons)
	case marking:
		if err := writes.MarkReady(ctx, project, m.Item, repo); err != nil {
			return handRead{}, fmt.Errorf("mark the pull request of item %s ready: %w", m.Item, err)
		}
	}

	return handRead{State: handWaiting}, nil
}

// handHold ends the member because of a reason to wait. A draft that was about to be marked ready stays a draft. A ready pull request goes back to draft.
func (e *Engine) handHold(ctx context.Context, project, repo string, m workflow.PullRequest, standing review.Standing, reasons string) (handRead, error) {
	if standing.Draft {
		return handRead{State: handHeld, Reason: "not marked ready: " + reasons}, nil
	}

	why := fmt.Sprintf("the pull request of item %s has a reason to wait: %s", m.Item, reasons)

	return e.draftAgain(ctx, project, repo, m, standing, why, handRead{State: handHeld, Reason: "drafted again: " + reasons})
}

// handMoved puts a pull request whose head moved back to draft, unless it is a draft already, and returns the new head.
func (e *Engine) handMoved(ctx context.Context, project, repo string, m workflow.PullRequest, standing review.Standing) (handRead, error) {
	why := fmt.Sprintf("the head of item %s moved to %s", m.Item, standing.Head)
	read := handRead{State: handMoved, Head: standing.Head, Reason: fmt.Sprintf("the head moved to %s after the head %s was marked ready", standing.Head, m.Head)}

	return e.draftAgain(ctx, project, repo, m, standing, why, read)
}

// draftAgain puts a pull request back to draft, unless it is a draft already, and returns read. A draft call that fails tells the owner to convert it by hand.
func (e *Engine) draftAgain(ctx context.Context, project, repo string, m workflow.PullRequest, standing review.Standing, why string, read handRead) (handRead, error) {
	if !standing.Draft {
		if err := e.deps.Batch.SetDraft(ctx, project, m.Item, repo, true); err != nil {
			return handRead{}, fmt.Errorf("%s. Cannot make its pull request a draft again. If it is open and ready for review, convert it to a draft by hand: %w", why, err)
		}
	}

	return read, nil
}

// handResultValue builds the result of a hand-off: the fields of the result of a landing, with no landing in them.
func handResultValue(out handResult) starlark.Value { //nolint:ireturn // the builtin contract of starlark-go
	return starlarkstruct.FromStringDict(starlarkstruct.Default, starlark.StringDict{
		mergedField: textList(out.merged), rejectedField: outcomes(out.rejected), deferredField: outcomes(nil), tipField: starlark.String(""), treeName: starlark.String(""),
		"gates": starlark.MakeInt(0), "findings": textList(nil), noteField: starlark.String(""), unprovenField: outcomes(nil), movedField: movedList(out.moved),
		hostLossKey: starlark.NewList(nil), hostLossesKey: starlark.MakeInt(0),
	})
}

// movedList turns the members whose head moved into a Starlark list of struct(item, head).
func movedList(moved []workflow.PullRequest) *starlark.List {
	out := make([]starlark.Value, len(moved))
	for i, m := range moved {
		out[i] = starlarkstruct.FromStringDict(starlarkstruct.Default, starlark.StringDict{itemName: starlark.String(m.Item), headKeyword: starlark.String(m.Head)})
	}

	return starlark.NewList(out)
}
