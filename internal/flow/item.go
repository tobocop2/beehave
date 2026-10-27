package flow

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"

	"github.com/tobocop2/obee/internal/model"
)

// reviewTries is how many reviews an item gets for one verdict when the first leaves none that can be read.
const reviewTries = 2

// errHeadMoved is returned when the commit changed between the gate, the review and the ready step.
var errHeadMoved = errors.New("the head of the pull request moved")

// ItemInput starts the workflow of one item.
type ItemInput struct {
	Item    string `json:"item"`
	Project string `json:"project"`
}

// ItemResult is what a finished item workflow reports. FixRounds counts the fix rounds the review asked for.
type ItemResult struct {
	Item      string `json:"item"`
	FixRounds int    `json:"fix_rounds"`
}

// loopState is the part of an item the fix loop carries from one round to the next.
type loopState struct {
	item     string
	project  string
	login    string
	fixed    int
	findings string
	answer   string
}

// item runs one item: claim, plan, implement, the review and fix rounds, ready, release.
// The workflow is not queued, so the wait for the owner holds no agent slot.
func (e *Engine) item(parent dbos.Context, in ItemInput) (ItemResult, error) {
	return e.track(parent, in, func(ctx dbos.Context) (ItemResult, error) {
		login, owner, took, err := e.begin(ctx, in)
		if err != nil {
			return ItemResult{}, err
		}

		result, workErr := e.workHolding(ctx, in, login, claimTake{owner: owner, took: took})

		// A run whose context ended is canceled, interrupted or without its claim. A canceled run is released by the tick; an interrupted run keeps its claim and continues at the next obee run.
		// A run that lost its claim has nothing to release.
		if ctx.Err() != nil {
			return result, workErr
		}

		return result, errors.Join(workErr, e.finish(ctx, in, owner))
	})
}

// claimTake is the claim of a run: its owner token, and when the take began. took is zero when this run did not make the take:
// a continued or resumed run replays the claim step, so the age of the lease is not known.
type claimTake struct {
	owner string
	took  time.Time
}

// workHolding runs the work of an item while the tick renews its claim. A run that lost the claim returns errClaimLost, with the reason, in place of the error of its work.
func (e *Engine) workHolding(ctx dbos.Context, in ItemInput, login string, taken claimTake) (ItemResult, error) {
	id, forget := e.holds.hold(ctx, in.Project, taken.owner)
	defer forget()

	if err := e.firstTrust(ctx, id, in, taken); err != nil {
		return ItemResult{}, err
	}

	result, err := e.work(ctx, in, login)
	if lost := e.claimCheck(id, in.Item); lost != nil {
		return result, lost
	}

	return result, err
}

// firstTrust starts the trust in a claim. A take that this run made is trusted from the start of the take. A take of unknown age
// needs one good renewal before the first agent starts.
func (e *Engine) firstTrust(ctx context.Context, id string, in ItemInput, taken claimTake) error {
	if !taken.took.IsZero() {
		e.trust(id, taken.took)

		return nil
	}

	if err := e.firstRenewal(ctx, id, in, taken.owner); err != nil {
		return err
	}

	return e.claimCheck(id, in.Item)
}

// firstRenewal renews a claim of unknown age until a renewal works or the tracker says the claim is gone, as the tick does, so one busy tracker call
// does not end a run that took a fresh claim. It gives up when a limit of trust has passed since the first try: the lease of that take can have ended.
func (e *Engine) firstRenewal(ctx context.Context, id string, in ItemInput, owner string) error {
	first := e.deps.Clock.now()

	for {
		start := e.deps.Clock.now()

		text, err := e.beatOne(ctx, claimHold{project: in.Project, owner: owner}, trustLimit-e.deps.Clock.age(first))
		if err == nil || text != "" {
			e.settle(id, start, text, err)

			return nil
		}

		if e.deps.Clock.age(first) >= trustLimit {
			return fmt.Errorf("renew the claim of item %s before the first agent starts: %w", in.Item, err)
		}

		if err := pause(ctx, cmp.Or(e.deps.Clock.Poll, tickInterval)); err != nil {
			return err
		}
	}
}

// finish releases an item. The token file of the run ends with a release that worked: a later run that reuses the run id must not read a dead token.
func (e *Engine) finish(ctx dbos.Context, in ItemInput, owner string) error {
	err := e.release(ctx, in, owner)
	if err == nil {
		e.forget(workflowID(ctx))
	}

	return err
}

// release ends an item while its claim is still held: first the worktree, then the claim of the owner token.
// A worktree that stays is not a failure; the step logs why.
func (e *Engine) release(ctx dbos.Context, in ItemInput, owner string) error {
	kept, treeErr := runStep(e, ctx, treeStepName, func(c context.Context) (string, error) {
		return e.removeTree(c, in.Project, in.Item)
	})
	if kept != "" {
		e.logger.Info("kept the worktree", "item", in.Item, "reason", kept)
	}

	if treeErr != nil {
		treeErr = fmt.Errorf("%s of item %s: %w", treeStepName, in.Item, treeErr)
	}

	releaseClaim := func(c context.Context, project, item string) error {
		c, cancel := ReleaseDeps{Clock: e.deps.Clock}.limit(c)
		defer cancel()

		return e.deps.Lifecycle.Release(c, project, item, owner)
	}

	return errors.Join(treeErr, e.lifecycle(ctx, model.StepRelease, releaseClaim, in.Project, in.Item))
}

// begin picks the login of an item and claims the item. Each is a step.
func (e *Engine) begin(ctx dbos.Context, in ItemInput) (login, owner string, took time.Time, err error) {
	login, err = runStep(e, ctx, "login", func(c context.Context) (string, error) {
		return e.deps.Logins.Pick(c, in.Project, in.Item)
	})
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("pick the login of item %s: %w", in.Item, err)
	}

	owner, took, err = e.claim(ctx, in)

	return login, owner, took, err
}

// claim takes the claim of an item and returns its owner token and the time the take began. A resumed run replays this step with the token of the old claim,
// so the token that obee resume saved for the run wins. A replayed step makes no take, so its time is zero.
func (e *Engine) claim(ctx dbos.Context, in ItemInput) (string, time.Time, error) {
	var took time.Time

	recorded, err := runStep(e, ctx, string(model.StepClaim), func(c context.Context) (string, error) {
		took = e.deps.Clock.now()

		return e.deps.Lifecycle.Claim(c, in.Project, in.Item)
	})
	if err != nil {
		return "", time.Time{}, fmt.Errorf("%s of item %s: %w", model.StepClaim, in.Item, err)
	}

	saved, err := savedOwner(e.dir, workflowID(ctx))

	return cmp.Or(saved, recorded), took, err
}

// lifecycle runs one step around the work of an item. The ready and release steps of the lifecycle plug in here.
func (e *Engine) lifecycle(ctx dbos.Context, step model.Step, call func(context.Context, string, string) error, project, item string) error {
	_, err := runStep(e, ctx, string(step), func(c context.Context) (struct{}, error) {
		return struct{}{}, call(c, project, item)
	})
	if err != nil {
		return fmt.Errorf("%s of item %s: %w", step, item, err)
	}

	return nil
}

// work runs the plan and the first implement round, then the fix loop.
func (e *Engine) work(ctx dbos.Context, in ItemInput, login string) (ItemResult, error) {
	for _, step := range []model.Step{model.StepPlan, model.StepImplement} {
		if err := e.runPhase(ctx, PhaseInput{Item: in.Item, Project: in.Project, Step: step, Login: login, Fresh: true}); err != nil {
			return ItemResult{}, err
		}
	}

	return e.fixLoop(ctx, loopState{item: in.Item, project: in.Project, login: login})
}

// fixLoop reviews and fixes until the review approves. Round policy lives in nextRound.
func (e *Engine) fixLoop(ctx dbos.Context, state loopState) (ItemResult, error) {
	for {
		review, err := e.review(ctx, state)
		if err != nil {
			return ItemResult{}, err
		}

		action := nextRound(state.fixed, review.Verdict)
		if action == actionStop {
			ready := func(c context.Context, project, item string) error { return e.markReady(c, project, item, review.Head) }

			return ItemResult{Item: state.item, FixRounds: state.fixed}, e.lifecycle(ctx, model.StepReady, ready, state.project, state.item)
		}

		state.findings = review.Findings

		if err := e.advance(ctx, &state, action); err != nil {
			return ItemResult{}, err
		}
	}
}

// advance runs the fix round that the round policy chose. It first waits for the owner when the policy asks for it.
func (e *Engine) advance(ctx dbos.Context, state *loopState, action roundAction) error {
	if action == actionWaitOwner {
		answer, err := e.waitOwner(ctx, state.item, state.findings)
		if err != nil {
			return err
		}

		state.answer, action = answer, actionResume
	}

	return e.fix(ctx, state, action)
}

// review runs the gate, then the review agent when the gate passes, and reads the verdict.
func (e *Engine) review(ctx dbos.Context, state loopState) (Review, error) {
	gate, err := runStep(e, ctx, string(model.StepGate), func(c context.Context) (GateResult, error) {
		return e.deps.Gate.Run(c, state.project, state.item)
	})
	if err != nil {
		return Review{}, fmt.Errorf("gate of item %s: %w", state.item, err)
	}

	if !gate.Passed {
		return Review{Verdict: VerdictChanges, Findings: gate.Report, Head: gate.Head}, nil
	}

	review, err := e.reviewTries(ctx, state)
	if err == nil && review.Head != gate.Head {
		err = fmt.Errorf("%w: item %s: the gate ran on %q and the review read %q", errHeadMoved, state.item, gate.Head, review.Head)
	}

	return review, err
}

// reviewTries runs the review phase and reads its verdict. A verdict that cannot be read earns one more review.
func (e *Engine) reviewTries(ctx dbos.Context, state loopState) (Review, error) {
	input := PhaseInput{Item: state.item, Project: state.project, Step: model.StepReview, Round: state.fixed, Login: state.login, Fresh: true}

	var err error

	for range reviewTries {
		if err = e.runPhase(ctx, input); err != nil {
			return Review{}, err
		}

		var review Review
		if review, err = e.readVerdict(ctx, state); err == nil {
			return review, nil
		}
	}

	return Review{}, fmt.Errorf("read the verdict of item %s after %d reviews: %w", state.item, reviewTries, err)
}

// readVerdict reads the verdict that the review left, as one step.
func (e *Engine) readVerdict(ctx dbos.Context, state loopState) (Review, error) {
	return runStep(e, ctx, string(model.StepRecord), func(c context.Context) (Review, error) {
		return e.deps.Verdicts.Read(c, state.project, state.item, state.fixed)
	})
}

// fix runs one fix round: an implement phase that reads the findings and the last answer of the owner.
func (e *Engine) fix(ctx dbos.Context, state *loopState, action roundAction) error {
	state.fixed++
	input := PhaseInput{
		Item: state.item, Project: state.project, Step: model.StepImplement, Round: state.fixed, Login: state.login,
		Fresh: action == actionFresh, Findings: state.findings, Answer: state.answer,
	}
	state.answer = ""

	if action == actionFresh {
		fresh, err := runStep(e, ctx, "fresh-login", func(c context.Context) (string, error) {
			return e.deps.Logins.PickFresh(c, state.project, state.item)
		})
		if err != nil {
			return fmt.Errorf("pick the fresh runner of item %s: %w", state.item, err)
		}

		state.login = cmp.Or(fresh, state.login)
		input.Login = state.login
	}

	return e.runPhase(ctx, input)
}

// waitOwner blocks until the owner answers through obee answer. The wait holds no queue slot.
// The findings are the question: status shows them while the run waits.
func (e *Engine) waitOwner(ctx dbos.Context, item, question string) (string, error) {
	if err := dbos.SetEvent(ctx, questionKey, question); err != nil {
		return "", fmt.Errorf("item %s cannot show its question: %w", item, err)
	}

	answer, err := e.receiveAnswer(ctx)
	if err == nil {
		err = dbos.SetEvent(ctx, questionKey, "")
	}

	if err != nil {
		return "", fmt.Errorf("item %s waits for the owner: %w. Answer with obee answer %s <text>", item, err, item)
	}

	return answer, nil
}

// receiveAnswer waits for the answer of the owner. The owner has no deadline: a wait that times out starts again.
func (e *Engine) receiveAnswer(ctx dbos.Context) (string, error) {
	for {
		answer, err := dbos.Recv[string](ctx, ownerTopic, cmp.Or(e.deps.Clock.OwnerWait, ownerWaitLimit))
		if !errors.Is(err, context.DeadlineExceeded) {
			return answer, err
		}
	}
}
