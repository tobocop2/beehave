package flow

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"

	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc"
	"github.com/tobocop2/beehave/internal/workflow"
)

// ItemInput starts the workflow of one item.
type ItemInput struct {
	Item    string `json:"item"`
	Project string `json:"project"`
	File    string `json:"file,omitempty"`
	// Files are the workflow file and every file it loads, with their hashes. A run executes only these bytes.
	Files []workflow.PinnedFile `json:"files,omitempty"`
	// Login is the agent login that a spawn or the queue picked before the run started. DBOS stores the input, so a resume keeps it.
	// A run that `beehave run` started has none: it picks at its first agent step.
	Login string `json:"login,omitempty"`
	// Entry is the function of the workflow file that runs the item. It is empty for an item that a user started, which runs main.
	// A run that spawn started has it set to the entry of the spawn call.
	Entry string `json:"entry,omitempty"`
	// Depth is the level of the run below the run a user started, which is at depth 0. Spawn gives its children Depth + 1.
	Depth int `json:"depth,omitempty"`
	// Parent is the id of the run that spawned this run, which send(to = "parent") sends to. It is empty for a run that a user started.
	Parent string `json:"parent,omitempty"`
	// Base is the base branch that the spawn call gave the run, or the base of the round item that spawned it. The worktree of the item is cut there. A run that a user started takes the base of the last run of the same item. It is empty when the item has no earlier run, and the item is cut at the base branch of the project.
	Base string `json:"base,omitempty"`
	// Sandbox is the sandbox mode the run started with, on or off. The start of a run refuses an empty mode, so only a run recorded before the field existed has none, and it runs with the mode off.
	Sandbox model.SandboxMode `json:"sandbox,omitempty"`
	// OnOpen is what this start does with an open run of the item. It is an instruction of the start, not state of the run, so the store never holds it: a resume and a replay read it empty.
	OnOpen workflow.OnOpen `json:"-"`
	// Debounce is the window that holds this start back. A later start of the item in the window takes the place of this one. It is an instruction of the start, so the store never holds it. Zero starts at once.
	Debounce time.Duration `json:"-"`
}

// RunOption is a choice for a run that a caller makes when it starts the run.
type RunOption func(*ItemInput)

// RunOnOpen sets what the start does with an open run of its item: join it, or replace it. An empty policy joins.
func RunOnOpen(policy workflow.OnOpen) RunOption { return func(in *ItemInput) { in.OnOpen = policy } }

// RunDebounce holds the start back for the window, and lets a later start of the item in the window take its place. Zero starts at once.
func RunDebounce(window time.Duration) RunOption { return func(in *ItemInput) { in.Debounce = window } }

// RunSandbox records the sandbox mode of the run in its input. The caller resolves the mode from its own config, never from the engine that serves the run.
func RunSandbox(mode model.SandboxMode) RunOption { return func(in *ItemInput) { in.Sandbox = mode } }

// ItemResult is what a finished item workflow reports.
// SentBack holds the reason when the watch returned the pull request to draft. The run is finished, and the next run of the item starts a new attempt.
type ItemResult struct {
	Item     string `json:"item"`
	SentBack string `json:"sent_back,omitempty"`
	// Closed holds the text for the owner when the pull request was closed with no merge. The run is finished, and nothing returned to draft.
	Closed string `json:"closed,omitempty"`
	// Merge is the state that the merge step of a workflow file ended in: merged, sent_back or closed. It is empty when the file has no merge step, or when the step did not run.
	Merge string `json:"merge,omitempty"`
	// Stopped is the command step that ended the item on a code of its `stop_on_rc`. It is nil when no step did.
	Stopped *Stopped `json:"stopped,omitempty"`
	// Value is the JSON text of what the entry function of a run that spawn started returned. It is empty for a run that returned None, and for a run that a user started.
	Value json.RawMessage `json:"value,omitempty"`
}

// StoppedOutputLimit is the most characters of the output of a stopping step that a status line or a queue row shows.
const StoppedOutputLimit = 120

// Stopped is a command step that ended the item: its name, its exit code and what it printed.
type Stopped struct {
	Step   string `json:"step"`
	RC     int    `json:"rc"`
	Output string `json:"output"`
}

// item runs one item: claim, the steps of its workflow file, release.
// The workflow is not queued, so the wait for the owner holds no agent slot.
func (e *Engine) item(parent dbos.Context, in ItemInput) (ItemResult, error) {
	return e.track(parent, in, func(ctx dbos.Context, a *attempt) (ItemResult, error) {
		login, owner, took, err := e.begin(ctx, in)
		if err != nil {
			return ItemResult{}, err
		}

		// The give-back of a cancel reads the token after the attempt ends, so the write needs no lock.
		a.owner = owner

		result, workErr := e.workHolding(ctx, a, login, claimTake{owner: owner, took: took})

		return result, e.endItem(ctx, a, in, workErr)
	})
}

// endItem releases the item of a run whose work ended with workErr, and returns the error of the run.
// A run whose context ended is canceled, interrupted or without its claim. A canceled run is released by the tick; an interrupted run keeps its claim and continues at the next beehave run.
// A run that lost its claim has nothing to release.
// A replay over a changed file records no step: a step recorded at a later id would stand in the way of the replay that follows the author's fix.
// The claim and the worktree go back as a cancel gives them back, with no step, and the parent reads the end of the run at its next look.
func (e *Engine) endItem(ctx dbos.Context, a *attempt, in ItemInput, workErr error) error {
	switch {
	case ctx.Err() != nil:
		return workErr
	case replayMismatch(workErr):
		e.giveBack(ctx, a)

		return workErr
	}

	released := e.finish(ctx, a)
	e.tellParent(ctx, in)

	return errors.Join(workErr, released)
}

// tellParent sends the end notice of a run that spawn started to the run that spawned it, after the file and the release ended, as a recorded send.
// The notice only wakes a first_of of the parent. A send that fails is logged, and the parent finds the end at its next read of the rows.
func (e *Engine) tellParent(ctx dbos.Context, in ItemInput) {
	if in.Parent == "" {
		return
	}

	if err := endNotice(ctx, in.Parent, workflowID(ctx)); err != nil {
		e.logger.WarnContext(ctx, "cannot tell the parent run that this run ended; the parent reads the end at its next look", "item", in.Item, "parent", in.Parent, "error", err)
	}
}

// claimTake is the claim of a run: its owner token, and when the take began. took is zero when this run did not make the take:
// a continued or resumed run replays the claim step, so the age of the lease is not known.
type claimTake struct {
	owner string
	took  time.Time
}

// workHolding runs the work of an item while the tick renews its claim. A run that lost the claim returns errClaimLost, with the reason, in place of the error of its work.
func (e *Engine) workHolding(ctx dbos.Context, a *attempt, login string, taken claimTake) (ItemResult, error) {
	forget := e.holds.hold(a, a.in.Project, taken.owner)
	defer forget()

	if err := e.trustClaim(ctx, a, taken); err != nil {
		return ItemResult{}, err
	}

	start, err := e.makeTree(ctx, a.in)
	if err != nil {
		return ItemResult{}, err
	}

	result, err := e.runStar(ctx, a.in, login, start)

	if _, lost := e.claimed(a.id, a.in.Item); lost != nil {
		return result, lost
	}

	return result, err
}

// reachedRelease reports whether a replayed run already began its release in an earlier process. The run did all its work then, and the release can have freed the claim before the stop of that process cut the step off, so no renewal of the claim can work.
func (e *Engine) reachedRelease(a *attempt, taken claimTake) (bool, error) {
	if !taken.took.IsZero() || a.id == "" {
		return false, nil
	}

	steps, err := dbos.GetWorkflowSteps(e.ctx, a.id)
	if err != nil {
		return false, fmt.Errorf("read the steps of %s: %w", a.id, err)
	}

	return slices.ContainsFunc(steps, func(step dbos.StepInfo) bool { return step.StepName == treeStepName }), nil
}

// trustClaim starts the trust in the claim of a run. A run that reached its release replays its recorded steps and renews nothing: the tick skips its claim, and the trust lasts for the replay.
func (e *Engine) trustClaim(ctx context.Context, a *attempt, taken claimTake) error {
	released, err := e.reachedRelease(a, taken)
	if err != nil {
		return err
	}

	if !released {
		return e.firstTrust(ctx, a, taken)
	}

	e.holds.freed(a)
	e.trust(a, e.deps.Clock.now())

	return nil
}

// firstTrust starts the trust in a claim. A take that this run made is trusted from the start of the take. A take of unknown age
// needs one good renewal before the first agent starts.
func (e *Engine) firstTrust(ctx context.Context, a *attempt, taken claimTake) error {
	if !taken.took.IsZero() {
		e.trust(a, taken.took)

		return nil
	}

	// The first renewal moves the lease to now, so the renewal of the process that ran before is read first.
	e.remember(ctx, a, taken.owner)

	if err := e.firstRenewal(ctx, a, taken.owner); err != nil {
		return err
	}

	_, err := e.claimed(a.id, a.in.Item)

	return err
}

// makeTree makes the worktree of a round item before its workflow file runs, so the command and gate steps of the item run in the repository.
// It returns the commit of the worktree, which every later step of the run makes a removed worktree again at. The step is recorded, so a resumed run
// reads the same commit and does not run it again. Any other item, and a round item of a project with no repository, gets no step and an empty commit.
func (e *Engine) makeTree(ctx dbos.Context, in ItemInput) (string, error) {
	if model.ItemKind(in.Item) != model.KindRound || !e.hasRepository(in.Project) {
		return "", nil
	}

	start, err := runStep(e, ctx, treeMakeStepName, func(c context.Context) (string, error) {
		dir, err := e.deps.Trees.Ensure(c, in.Project, in.Item, "", "")
		if err != nil {
			return "", err
		}

		return folderHead(c, e.deps.Trees.Git(dir))
	})
	if err != nil {
		return "", fmt.Errorf("%s of item %s: %w", treeMakeStepName, in.Item, err)
	}

	return start, nil
}

// remember reads the last renewal of the lease of the claim of the owner from the claim store and keeps it with the claim of the run. A read that fails is logged
// and keeps nothing: the only cost is that the trace of a later loss gives no run time.
func (e *Engine) remember(ctx context.Context, a *attempt, owner string) {
	renewed, err := e.deps.Beats.Renewed(ctx, a.in.Project, owner, a.in.Item)
	if err != nil {
		e.logger.WarnContext(ctx, "cannot read the last renewal of the claim", "item", a.in.Item, "error", err)

		return
	}

	e.holds.remember(a, renewed)
}

// firstRenewal renews a claim of unknown age until a renewal works or the tracker says the claim is gone, as the tick does, so one busy tracker call
// does not end a run that took a fresh claim. It gives up when a limit of trust has passed since the first try: the lease of that take can have ended.
func (e *Engine) firstRenewal(ctx context.Context, a *attempt, owner string) error {
	first := e.deps.Clock.now()

	for {
		start := e.deps.Clock.now()

		text, err := e.beatOne(ctx, claimHold{project: a.in.Project, owner: owner}, trustLimit-e.deps.Clock.age(first))
		if err == nil || text != "" {
			e.settle(a, start, text, err)

			return nil
		}

		if e.deps.Clock.age(first) >= trustLimit {
			return fmt.Errorf("renew the claim of item %s before the first agent starts: %w", a.in.Item, err)
		}

		if err := proc.Sleep(ctx, cmp.Or(e.deps.Clock.Poll, tickInterval)); err != nil {
			return err
		}
	}
}

// finish releases the item of an attempt. The token file of the run ends with a release that worked, while it still holds the token of the attempt:
// a later run that reuses the run id must not read a dead token, and the token of a later resume stays.
func (e *Engine) finish(ctx dbos.Context, a *attempt) error {
	err := e.release(ctx, a.in, a.owner)
	if err == nil {
		e.forgetAttempt(a.id, a.owner)
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

// begin reads who wrote the item, then claims it. A foreign item that the run may not hold ends the run before the claim.
// A login in the input, from a spawn or the queue, is the login of every agent step that names no runner.
// A run with no login in its input picks one at its first agent step, and a resumed run replays that pick.
func (e *Engine) begin(ctx dbos.Context, in ItemInput) (login, owner string, took time.Time, err error) {
	if err = e.origin(ctx, in); err != nil {
		return "", "", time.Time{}, err
	}

	owner, took, err = e.claim(ctx, in)

	return in.Login, owner, took, err
}

// claim takes the claim of an item and returns its owner token and the time the take began. A resumed or forked run replays this step, makes no take and waits for nothing, so its time is zero.
// The take waits for the release of an earlier attempt of the item first, as a replace starts the next attempt before the engine released the one it canceled.
func (e *Engine) claim(ctx dbos.Context, in ItemInput) (string, time.Time, error) {
	var took time.Time

	recorded, err := runStep(e, ctx, string(model.StepClaim), func(c context.Context) (string, error) {
		if err := e.awaitRelease(c, in.Item, workflowID(ctx)); err != nil {
			return "", err
		}

		took = e.deps.Clock.now()

		return e.deps.Lifecycle.Claim(c, in.Project, in.Item)
	})
	if err != nil {
		return "", time.Time{}, fmt.Errorf("%s of item %s: %w", model.StepClaim, in.Item, err)
	}

	owner, err := e.ownerOf(ctx, in, recorded)

	return owner, took, err
}

// ownerOf returns the token of the claim of the run: the token that beehave resume or beehave fork saved for it, or else the recorded one. It then shows the other orchestrators of the project.
func (e *Engine) ownerOf(ctx dbos.Context, in ItemInput, recorded string) (string, error) {
	saved, err := savedOwner(e.dir, workflowID(ctx))
	if err != nil {
		return "", err
	}

	owner := cmp.Or(saved, recorded)

	return owner, e.listOthers(ctx, in, owner)
}

// listOthers reads the other orchestrators that hold items in the project and shows them, so status names them. The read is a recorded step.
// A run with no one else shows nothing. A read that fails ends the run, because a failed read is not "nobody else runs".
func (e *Engine) listOthers(ctx dbos.Context, in ItemInput, owner string) error {
	if e.deps.Others == nil {
		return nil
	}

	lines, err := runStep(e, ctx, othersStep, func(c context.Context) ([]string, error) {
		return e.deps.Others.Others(c, in.Project, owner)
	})
	if err != nil {
		return fmt.Errorf("%s of item %s: %w", othersStep, in.Item, err)
	}

	if len(lines) == 0 {
		return nil
	}

	return show(ctx, in.Item, othersKey, lines, "the other orchestrators")
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

// waitOwner blocks until the owner answers through beehave answer. The wait holds no queue slot.
// The findings are the question: status shows them while the run waits.
func (s *script) waitOwner(question string) (string, error) {
	answer, _, err := s.waitOwnerUntil(question, 0)
	return answer, err
}

// waitOwnerUntil is waitOwner with a deadline. A deadline of zero is no deadline. When the deadline passes first, the answer is empty and timedOut is true.
func (s *script) waitOwnerUntil(question string, deadline time.Duration) (answer string, timedOut bool, err error) {
	item := s.in.Item
	if err := show(s.ctx, item, questionKey, s.e.redact(question), "its question"); err != nil {
		return "", false, err
	}

	s.e.announce(s.ctx, s.in.Project, item, question)

	answer, timedOut, err = s.receiveAnswer(deadline)
	if err == nil {
		err = dbos.SetEvent(s.ctx, questionKey, "")
	}

	if err != nil {
		return "", false, fmt.Errorf("item %s waits for the owner: %w. Answer with beehave answer %s <text>", item, err, item)
	}

	return answer, timedOut, nil
}

// show sets an event of the run that status reads. what names the event for the error.
func show[T any](ctx dbos.Context, item, key string, value T, what string) error {
	if err := dbos.SetEvent(ctx, key, value); err != nil {
		return fmt.Errorf("item %s cannot show %s: %w", item, what, err)
	}

	return nil
}

// receiveAnswer waits for the answer of the owner: the first answer that names no wait. An answer to a wait that names people is not one.
// The end of the deadline is a recorded step, so a replay waits no longer than the first run did.
func (s *script) receiveAnswer(deadline time.Duration) (answer string, timedOut bool, err error) {
	end, err := s.e.endOf(s.ctx, deadline)
	if err != nil {
		return "", false, err
	}

	for {
		v, got, late, err := s.nextVoice(end)

		switch {
		case err != nil:
			return "", false, err
		case late:
			return "", true, nil
		case got && v.Wait == "":
			return v.Text, false, nil
		}
	}
}
