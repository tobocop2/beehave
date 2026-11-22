package flow

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"

	"github.com/tobocop2/beehave/internal/claim"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/workflow"
)

// ResumeOnThisBuild moves the canceled, failed or open run of an item that another build started to this build, from its last finished step.
// DBOS stores the version of the application on each run and recovers only the runs of its own version, so the rewind restamps the run (and the phases it reopens) with this version.
// The move is refused when the workflow file or a file it pins changed since the run started. A run of this build is an ordinary resume.
func (c *Client) ResumeOnThisBuild(item string) error {
	last, err := c.newest(item)
	if err != nil {
		return err
	}

	if last.Status.ApplicationVersion == c.version {
		return c.Resume(item)
	}

	if err := c.requireMovable(context.Background(), item, last); err != nil {
		return err
	}

	// DBOS rewinds only a run that ended, so an open run is canceled first.
	if last.terminal() {
		return c.reopenHere(item, last)
	}

	return c.moveOpen(item, last)
}

// moveOpen cancels an open run and reopens it on this build. When the reopen fails the run stays canceled, and the error says so and how to try again.
func (c *Client) moveOpen(item string, last run) error {
	if err := c.cancelRun(item, last); err != nil {
		return err
	}

	if err := c.reopenHere(item, last); err != nil {
		return canceledNotMoved(item, last, err)
	}

	return nil
}

// canceledNotMoved names a run that a move canceled and could not reopen, and how to try again.
func canceledNotMoved(item string, last run, err error) error {
	return fmt.Errorf("run %s is now canceled and still stored under version %s: %w. Run beehave resume %s --on-this-build again", last.Status.ID, last.Status.ApplicationVersion, err, item)
}

// requireMovable refuses a run that cannot move to this build: one recorded under another step list, a finished one, one whose holder is alive, and one whose files changed.
func (c *Client) requireMovable(ctx context.Context, item string, last run) error {
	if err := c.requireSameSteps(item, last); err != nil {
		return err
	}

	if state := stateOf(last.Status.Status); last.terminal() && state != runCancelled && state != runFailed {
		return fmt.Errorf("%w: %s is %s and only a cancelled, failed or open run moves to this build. Run beehave run %s to start again", errWrongState, last.Status.ID, state, item) //nolint:misspell // user-facing state word, same as runCancelled
	}

	if err := c.requireNoLiveHolder(ctx, item, last); err != nil {
		return err
	}

	return requirePinned(last)
}

// requireSameSteps refuses a run that this build cannot replay. A run replays its recorded steps, so it needs the step modules that it recorded
// at the same version as this build. A module that the run recorded no step of may differ. A run stored under a single version of the steps
// other than the one that stands for every module at version 1, or under none, cannot replay at all.
func (c *Client) requireSameSteps(item string, last run) error {
	stored, now := stepListOf(last.Status.ApplicationVersion), stepListOf(c.version)

	was, readErr := readVersions(stored)
	if readErr != nil && looksLikeModuleList(stored) {
		return fmt.Errorf("%w: %s was recorded under a step list that is not valid: %w. %sStart the item again: beehave run %s", errStranded, last.Status.ID, readErr, endFailedFirst(last, item), item)
	}

	if readErr != nil {
		return fmt.Errorf("%w: %s was recorded under %s and this build has %s. A run cannot replay under another step list. %sStart the item again: beehave run %s",
			errStranded, last.Status.ID, describeStepList(stored), describeStepList(now), endFailedFirst(last, item), item)
	}

	current, _ := parseVersions(now)

	return c.requireUsedModulesSame(item, last, was, current)
}

// requireUsedModulesSame refuses a run that recorded a step of a module whose version differs between the stored list and this build.
// It first refuses a run that recorded a step that this build does not record, even when the lists are equal: the replay cannot reach that step.
func (c *Client) requireUsedModulesSame(item string, last run, was, current map[string]int) error {
	names, err := c.stepNames(last.Status.ID)
	if err != nil {
		return err
	}

	if err := requireReplayableSteps(item, last, names); err != nil {
		return err
	}

	if maps.Equal(was, current) {
		return nil
	}

	if module, changed := changedModule(modulesUsed(names), was, current); changed {
		return fmt.Errorf("%w: %s recorded steps of the %s module at %s and this build has %s. A run cannot replay under another step list. %sStart the item again: beehave run %s",
			errStranded, last.Status.ID, module, describeModule(module, was), describeModule(module, current), endFailedFirst(last, item), item)
	}

	return requireSameRoundModule(item, last, was, current)
}

// requireReplayableSteps refuses a run that recorded a step that this build does not record, and names the step and the module of the older build.
func requireReplayableSteps(item string, last run, names []string) error {
	step, module, found := unreplayableStep(names)
	if !found {
		return nil
	}

	owner := "no module of this build records that step"
	if module != "" {
		owner = fmt.Sprintf("an older build recorded it for the %s module, and this build does not record it", module)
	}

	return fmt.Errorf("%w: %s recorded the step %q, and %s. A run cannot replay under another step list. %sStart the item again: beehave run %s",
		errStranded, last.Status.ID, step, owner, endFailedFirst(last, item), item)
}

// requireSameRoundModule refuses a run of a round item when the round module changed. Every round run uses the module, because its version covers the step
// that makes the worktree at the start of the run, so the refusal does not wait for a recorded step: a run of an older build has none.
func requireSameRoundModule(item string, last run, was, current map[string]int) error {
	if model.ItemKind(item) != model.KindRound {
		return nil
	}

	if _, changed := changedModule([]string{moduleRound}, was, current); !changed {
		return nil
	}

	return fmt.Errorf("%w: %s is a run of a round item, recorded with the round module at %s, and this build has %s. This build makes the worktree in the step %q "+
		"before the workflow file runs, so the run cannot replay here. Finish the run on the build that started it, or start the item again: beehave run %s",
		errStranded, last.Status.ID, describeModule(moduleRound, was), describeModule(moduleRound, current), treeMakeStepName, item)
}

// stepNames reads the names of the steps that a run recorded.
func (c *Client) stepNames(id string) ([]string, error) {
	records, err := dbos.GetWorkflowSteps(c.flow, id)
	if err != nil {
		return nil, fmt.Errorf("read the steps of %s: %w", id, err)
	}

	names := make([]string, len(records))
	for i, record := range records {
		names[i] = record.StepName
	}

	return names, nil
}

// requireNoLiveHolder refuses an open run whose claim lease has not ended: the build that holds it still runs, and a move would take the run from it.
// The store keeps the lease, and the engine renews it, so no new signal is needed. A run with no claim, or a client with no lease reader, has no holder to protect.
func (c *Client) requireNoLiveHolder(ctx context.Context, item string, last run) error {
	if last.terminal() || c.give.Beats == nil {
		return nil
	}

	owner, ends, err := c.leaseOf(ctx, last)
	if err != nil || !ends.After(c.give.Clock.now()) {
		return err
	}

	holder, holderErr := claim.OwnerOf(owner)
	if holderErr != nil {
		holder = owner
	}

	return fmt.Errorf("%w: %s still runs on version %s. The claim lease of %s ends at %s. Stop that build first, or wait for the lease to end, then run beehave resume %s --on-this-build again",
		errStranded, last.Status.ID, last.Status.ApplicationVersion, holder, ends.UTC().Format(time.RFC3339), item)
}

// leaseOf returns the owner of the claim of a run and when its lease in the store ends. A run with no claim gives the zero time. A claim with no lease gives an end that is long past.
func (c *Client) leaseOf(ctx context.Context, last run) (owner string, ends time.Time, err error) {
	owner, err = currentOwner(c.flow, c.dir, last.Status.ID)
	if errors.Is(err, errNoOwner) {
		return "", time.Time{}, nil
	}

	in, inErr := last.input()
	if err = errors.Join(err, inErr); err != nil {
		return "", time.Time{}, err
	}

	ctx, cancel := c.give.limit(ctx)
	defer cancel()

	renewed, err := c.give.Beats.Renewed(ctx, in.Project, owner, in.Item)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("read the claim lease of %s: %w", last.Status.ID, err)
	}

	return owner, renewed.Add(model.ClaimLease), nil
}

// requirePinned refuses a run whose workflow file or pinned file changed since the run started, and names the file. The refusal also lists the scripts
// outside the repository, which a run does not pin.
func requirePinned(last run) error {
	in, readErr := last.input()
	_, openErr := workflow.Open(in.File, in.Files)

	if err := errors.Join(readErr, openErr); err != nil {
		text := fmt.Sprintf("%s was not moved to this build: %v", last.Status.ID, err)

		if outside := workflow.OutsideScripts(in.File); len(outside) > 0 {
			text += ". These scripts are outside the repository and are not pinned: " + strings.Join(outside, ", ")
		}

		if dynamic := workflow.DynamicCommands(in.File); dynamic > 0 {
			text += fmt.Sprintf(". %d command calls have a run list that is not a literal, so their scripts are not pinned", dynamic)
		}

		return errors.New(text)
	}

	return nil
}

// reopenHere takes a fresh claim, as Resume does, and rewinds the run and the phases the cancel reached onto this build.
func (c *Client) reopenHere(item string, last run) error {
	project, owner, err := c.takeClaim(item, last)
	if err != nil {
		return err
	}

	// probe.lock keeps a cancel of the tree out while the reopen reads it (see docs/design/durable-loop.md).
	err = c.underProbe(func() error { return c.rewindHere(last) })
	if err != nil {
		return errors.Join(err, c.dropClaim(project, item, last.Status.ID, owner))
	}

	return nil
}

// rewindHere rewinds the phases that the cancel reached and the run onto this build. The caller holds probe.lock.
func (c *Client) rewindHere(last run) error {
	restamp := dbos.WithRewindApplicationVersion(c.version)

	if err := c.reopenCanceledPhases(last.Status.ID, restamp); err != nil {
		return err
	}

	return c.reopen(last.Status.ID, restamp)
}
