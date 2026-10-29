package flow

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"

	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/workflow"
)

// ItemInput starts the workflow of one item.
type ItemInput struct {
	Item    string `json:"item"`
	Project string `json:"project"`
	File    string `json:"file,omitempty"`
	// Files are the workflow file and every file it loads, with their hashes. A run executes only these bytes.
	Files []workflow.PinnedFile `json:"files,omitempty"`
	// Login is the agent login that handleFor picked before the run started. DBOS stores the input, so a resume keeps it.
	Login string `json:"login,omitempty"`
	// Entry is the function of the workflow file that runs the item. It is empty for an item that a user started, which runs main.
	// A run that spawn started has it set to the entry of the spawn call, and such a run cannot spawn.
	Entry string `json:"entry,omitempty"`
}

// ItemResult is what a finished item workflow reports.
// SentBack holds the reason when the watch returned the pull request to draft. The run is finished, and the next run of the item starts a new attempt.
type ItemResult struct {
	Item     string `json:"item"`
	SentBack string `json:"sent_back,omitempty"`
	// Merge is the state that the merge step of a workflow file ended in: merged or sent_back. It is empty when the file has no merge step, or when the step did not run.
	Merge string `json:"merge,omitempty"`
	// Stopped is the command step that ended the item on a code of its `stop_on_rc`. It is nil when no step did.
	Stopped *Stopped `json:"stopped,omitempty"`
}

// Stopped is a command step that ended the item: its name, its exit code and what it printed.
type Stopped struct {
	Step   string `json:"step"`
	RC     int    `json:"rc"`
	Output string `json:"output"`
}

// item runs one item: claim, the steps of its workflow file, release.
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

	result, err := e.runStar(ctx, in, login)

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

// begin claims the item. The login came in the input of the run, so an agent step that names no runner uses it,
// and a resumed run keeps it, so every agent of a run, and every resume of a session, uses the one login.
// An input with no login claims nothing.
func (e *Engine) begin(ctx dbos.Context, in ItemInput) (login, owner string, took time.Time, err error) {
	if in.Login == "" {
		return "", "", time.Time{}, errEmptyLogin
	}

	owner, took, err = e.claim(ctx, in)

	return in.Login, owner, took, err
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

// waitOwner blocks until the owner answers through obee answer. The wait holds no queue slot.
// The findings are the question: status shows them while the run waits.
func (e *Engine) waitOwner(ctx dbos.Context, item, question string) (string, error) {
	if err := show(ctx, item, questionKey, question, "its question"); err != nil {
		return "", err
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

// show sets an event of the run that status reads. what names the event for the error.
func show(ctx dbos.Context, item, key, value, what string) error {
	if err := dbos.SetEvent(ctx, key, value); err != nil {
		return fmt.Errorf("item %s cannot show %s: %w", item, what, err)
	}

	return nil
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
