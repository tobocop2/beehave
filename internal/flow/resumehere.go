package flow

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/gofrs/flock"

	"github.com/tobocop2/beehave/internal/claim"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/workflow"
)

// ResumeOnThisBuild moves the canceled, failed or open run of an item that another build started to this build, from its last finished step.
// DBOS stores the version of the application on each run and recovers only the runs of its own version, so the rewind restamps the run (and the phases it reopens) with this version.
// The move is refused when the workflow file or a file it pins changed since the run started. A run of this build is an ordinary resume.
func (c *Client) ResumeOnThisBuild(item string) error {
	_, err := c.resumeMoved(item, true, false)

	return err
}

// ResumeOnThisFile reopens the open, canceled or failed run of an item on the workflow file as it is on disk now, from its last finished step.
// The run replays its recorded steps over the new file, and the engine of the store refuses the replay at the first step that the file no longer makes.
// The run must be of this build: a run of another build is refused, and ResumeOnThisBuildAndFile moves it. A file that did not change is no error: a run that ended resumes as `beehave resume` does, and an open run is reopened with no pin written.
func (c *Client) ResumeOnThisFile(item string) error {
	_, err := c.resumeMoved(item, false, true)

	return err
}

// ResumeOnThisBuildAndFile is ResumeOnThisBuild and ResumeOnThisFile in one move: one cancel and one rewind.
func (c *Client) ResumeOnThisBuildAndFile(item string) error {
	_, err := c.resumeMoved(item, true, true)

	return err
}

// resumeMoved reopens the run of an item and returns its id. build lets the run move from another build to this one, and file puts the run on the workflow file as it is now.
// A run of this build with no new file is an ordinary resume.
func (c *Client) resumeMoved(item string, build, file bool) (string, error) {
	last, err := c.newest(item)
	if err != nil {
		return "", err
	}

	same := last.Status.ApplicationVersion == c.version

	switch {
	case same && !file:
		return c.resumeRun(item)
	case !same && !build:
		return "", strandedError(last, c.version)
	}

	return c.moveOrResume(item, last, same, file)
}

// moveOrResume runs the refusals of a move on the run that resumeMoved read, then reopens it. It returns the id of that run.
func (c *Client) moveOrResume(item string, last run, same, file bool) (string, error) {
	pin, err := c.checkMove(item, last, file)
	if err != nil {
		return "", err
	}

	// A run of this build that has ended and has no new pin is an ordinary resume. An open run is reopened with no pin written.
	if same && pin == nil && last.terminal() {
		return c.resumeRun(item)
	}

	if err := c.reopenMoved(item, last, pin); err != nil {
		return "", err
	}

	return last.Status.ID, nil
}

// checkMove runs the refusals of a move and returns the pin to write.
func (c *Client) checkMove(item string, last run, file bool) ([]workflow.PinnedFile, error) {
	if err := c.requireMovableRun(context.Background(), item, last); err != nil {
		return nil, err
	}

	return c.pinToMove(item, last, file)
}

// reopenMoved reopens a run that passed the checks of its move. DBOS rewinds only a run that ended, so an open run is canceled first.
func (c *Client) reopenMoved(item string, last run, pin []workflow.PinnedFile) error {
	if last.terminal() {
		return c.reopenHere(item, last, pin)
	}

	return c.moveOpen(item, last, pin)
}

// requireUnchanged reads the newest run of the item again, under probe.lock, and refuses the move when another build or another command changed it since requireMovable read it.
// The run that the move checked and the run that it would move must be the same row in the same state.
func (c *Client) requireUnchanged(item string, last run) error {
	now, err := c.newest(item)
	if err != nil {
		return err
	}

	if now.Status.ID != last.Status.ID || now.Status.Status != last.Status.Status || now.Status.ApplicationVersion != last.Status.ApplicationVersion {
		return fmt.Errorf("%w: %s is now %s on version %s, and the move read it as %s on version %s. Run beehave status to see the run",
			ErrWrongState, now.Status.ID, stateOf(now.Status.Status), buildVersion(now.Status.ApplicationVersion), stateOf(last.Status.Status), buildVersion(last.Status.ApplicationVersion))
	}

	return nil
}

// pinToMove checks that the run may replay on the files of its move, and returns the pin to write. A move with no new file needs the pinned files unchanged.
// A move with a new file reads the workflow file now, and returns no pin when it is the pin that the run replays against.
func (c *Client) pinToMove(item string, last run, file bool) ([]workflow.PinnedFile, error) {
	if !file {
		return nil, requirePinned(item, last)
	}

	in, err := last.input()
	if err != nil {
		return nil, err
	}

	now, err := workflow.Pin(in.File)
	if err != nil {
		return nil, fmt.Errorf("%s was not moved onto the workflow file: %w", last.Status.ID, err)
	}

	have, err := pinOf(last.Status, in)
	if err != nil || slices.Equal(now, have) {
		return nil, err
	}

	return now, nil
}

// moveOpen cancels an open run and reopens it on this build, under one hold of probe.lock, so no reader sees the run canceled in between.
// The run is read again first, and the pin is written after the cancel.
func (c *Client) moveOpen(item string, last run, pin []workflow.PinnedFile) error {
	return c.underProbe(func() error {
		if err := c.requireUnchanged(item, last); err != nil {
			return err
		}

		return c.cancelAndReopenHeld(item, last, pin)
	})
}

// cancelAndReopenHeld is the move of an open run while probe.lock is held. The run and the open runs below it carry the move mark, and move.lock is held, from before the cancel to after the rewind,
// so a reader reads the run as open (see storeView). When the reopen fails the run stays canceled with no mark, and the error says so and how to try again.
// A pin is written after the cancel and before the rewind, so a cancel that fails leaves no pin on a run that did not move.
func (c *Client) cancelAndReopenHeld(item string, last run, pin []workflow.PinnedFile) (err error) {
	span, err := c.beginMove(last)
	if err != nil {
		return err
	}

	defer func() { err = errors.Join(err, span.end(err != nil)) }()

	if err := c.cancelHeld(item, last); err != nil {
		return err
	}

	if err := c.setPin(last, pin); err != nil {
		return fmt.Errorf("run %s is now canceled and not on the new file: %w. Run beehave resume %s --on-this-file again, with --on-this-build when another build started the run", last.Status.ID, err, item)
	}

	if err := c.reopenHeld(item, last); err != nil {
		return canceledNotMoved(item, last, err)
	}

	// The run is open again, so no mark may stay on it or on the runs below it.
	return c.clearMarks(last.Status.ID)
}

// moveSpan is the hold of move.lock and the marks of a move.
type moveSpan struct {
	client *Client
	root   string
	lock   *flock.Flock
}

// beginMove takes move.lock and marks the run and each open run below it with the version that the move goes to, before the cancel. A failure leaves no mark and no lock.
func (c *Client) beginMove(last run) (*moveSpan, error) {
	lock := lockFile(c.dir, moveLockName)
	if err := lock.Lock(); err != nil {
		return nil, fmt.Errorf("lock %s: %w", moveLockName, err)
	}

	span := &moveSpan{client: c, root: last.Status.ID, lock: lock}

	below, err := c.openRowsUnder(last.Status.ID)
	if err == nil {
		err = c.markMove(append([]dbos.WorkflowStatus{last.Status}, below...))
	}

	if err != nil {
		return nil, errors.Join(err, span.end(true))
	}

	return span, nil
}

// markMove sets the move mark on each row, with the queue that the cancel takes from the row.
func (c *Client) markMove(rows []dbos.WorkflowStatus) error {
	for _, row := range rows {
		mark := map[string]any{moveAttribute: moveMark{To: c.version, Queue: row.QueueName}}
		if err := dbos.SetWorkflowAttributes(c.flow, row.ID, mark, dbos.WithMergeAttributes()); err != nil {
			return fmt.Errorf("mark run %s as moving: %w", row.ID, err)
		}
	}

	return nil
}

// end frees move.lock after it clears the marks when the move failed: a move that succeeded cleared them.
func (s *moveSpan) end(failed bool) error {
	var err error
	if failed {
		err = s.client.clearMarks(s.root)
	}

	return errors.Join(err, s.lock.Unlock())
}

// clearMarks removes the move mark from the run with this id and from every run below it, whatever the mark says. A mover that died leaves its marks, and the next reopen clears them here.
func (c *Client) clearMarks(id string) error {
	root, err := c.row(id)
	if err != nil {
		return err
	}

	below, err := c.rowsUnder(id)
	if err != nil {
		return err
	}

	for _, row := range append([]dbos.WorkflowStatus{root}, below...) {
		if _, marked := moveMarkOf(row); marked {
			if err := c.clearMark(row.ID); err != nil {
				return err
			}
		}
	}

	return nil
}

// clearMark removes the move mark of one run.
func (c *Client) clearMark(id string) error {
	if err := dbos.SetWorkflowAttributes(c.flow, id, map[string]any{moveAttribute: nil}, dbos.WithMergeAttributes()); err != nil {
		return fmt.Errorf("clear the move mark of run %s: %w", id, err)
	}

	return nil
}

// canceledNotMoved names a run that a move canceled and could not reopen, and how to try again.
func canceledNotMoved(item string, last run, err error) error {
	return fmt.Errorf("run %s is now canceled and still stored under version %s: %w. Run beehave resume %s --on-this-build again", last.Status.ID, last.Status.ApplicationVersion, err, item)
}

// requireMovable refuses a run that cannot move to this build: one recorded under another step list, a finished one, one whose holder is alive, and one whose files changed.
func (c *Client) requireMovable(ctx context.Context, item string, last run) error {
	if err := c.requireMovableRun(ctx, item, last); err != nil {
		return err
	}

	return requirePinned(item, last)
}

// requireMovableRun is requireMovable for a move that checks no file: a move onto the new workflow file reads the file itself.
func (c *Client) requireMovableRun(ctx context.Context, item string, last run) error {
	if err := c.requireSameSteps(item, last); err != nil {
		return err
	}

	if state := stateOf(last.Status.Status); last.terminal() && state != runCancelled && state != runFailed {
		return fmt.Errorf("%w: %s is %s and only a cancelled, failed or open run moves to this build. Run beehave run %s to start again", ErrWrongState, last.Status.ID, state, item) //nolint:misspell // user-facing state word, same as runCancelled
	}

	return c.requireNoLiveHolder(ctx, item, last)
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

	return fmt.Errorf("%w: %s still runs on version %s. The claim lease of %s ends at %s. Stop that build first, or wait for the lease to end, then run beehave resume %s again with the same flags",
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

// requirePinned refuses a run whose workflow file or pinned file changed since the run started, and names the file. The refusal also counts the command
// calls whose script is not known before the run, which a run does not pin.
func requirePinned(item string, last run) error {
	in, readErr := last.input()
	pin, pinErr := pinOf(last.Status, in)
	_, openErr := workflow.Open(in.File, pin)

	if err := errors.Join(readErr, pinErr, openErr); err != nil {
		text := fmt.Sprintf("%s was not moved to this build: %v. To keep its finished steps and run the changed file, run beehave resume %s --on-this-build --on-this-file", last.Status.ID, err, item)

		if dynamic := workflow.DynamicCommands(in.File); dynamic > 0 {
			text += fmt.Sprintf(". %d command calls have a run list that is not a literal, so their scripts are not pinned", dynamic)
		}

		return errors.New(text)
	}

	return nil
}

// reopenHere takes a fresh claim, as Resume does, and rewinds the run and the phases the cancel reached onto this build.
// probe.lock keeps a cancel of the tree out while the reopen reads it (see docs/design/durable-loop.md).
func (c *Client) reopenHere(item string, last run, pin []workflow.PinnedFile) error {
	return c.underProbe(func() error {
		if err := c.requireUnchanged(item, last); err != nil {
			return err
		}

		if err := c.setPin(last, pin); err != nil {
			return err
		}

		if err := c.reopenHeld(item, last); err != nil {
			return err
		}

		// A mover that died after its cancel left its marks, and the reopen clears them.
		return c.clearMarks(last.Status.ID)
	})
}

// reopenHeld takes a fresh claim and rewinds the phases that the cancel reached and the run onto this build. A rewind that fails gives the claim back. The caller holds probe.lock.
func (c *Client) reopenHeld(item string, last run) error {
	project, owner, err := c.takeClaim(item, last)
	if err != nil {
		return err
	}

	restamp := dbos.WithRewindApplicationVersion(c.version)

	err = c.reopenCanceledPhases(last.Status.ID, restamp)
	if err == nil {
		err = c.reopen(last.Status.ID, restamp)
	}

	if err != nil {
		return errors.Join(err, c.dropClaim(project, item, last.Status.ID, owner))
	}

	return nil
}
