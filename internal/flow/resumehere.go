package flow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"

	"github.com/tobocop2/obee/internal/claim"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/workflow"
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

	if err := c.requireMovable(item, last); err != nil {
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
		return fmt.Errorf("run %s is now canceled and still stored under version %s: %w. Run obee resume %s --on-this-build again", last.Status.ID, last.Status.ApplicationVersion, err, item)
	}

	return nil
}

// requireMovable refuses a run that cannot move to this build: one recorded under another step list, a finished one, one whose holder is alive, and one whose files changed.
func (c *Client) requireMovable(item string, last run) error {
	// A run replays its recorded steps. Under another step list the replay does not match them, whatever the build.
	if was, now := stepListOf(last.Status.ApplicationVersion), stepListOf(c.version); was != now {
		return fmt.Errorf("%w: %s was recorded under %s and this build has %s. A run cannot replay under another step list. Start the item again: obee run %s",
			errStranded, last.Status.ID, describeStepList(was), describeStepList(now), item)
	}

	if state := stateOf(last.Status.Status); last.terminal() && state != runCancelled && state != runFailed {
		return fmt.Errorf("%w: %s is %s and only a cancelled, failed or open run moves to this build. Run obee run %s to start again", errWrongState, last.Status.ID, state, item) //nolint:misspell // user-facing state word, same as runCancelled
	}

	if err := c.requireNoLiveHolder(item, last); err != nil {
		return err
	}

	return requirePinned(last)
}

// requireNoLiveHolder refuses an open run whose claim lease has not ended: the build that holds it still runs, and a move would take the run from it.
// The store keeps the lease, and the engine renews it, so no new signal is needed. A run with no claim, or a client with no lease reader, has no holder to protect.
func (c *Client) requireNoLiveHolder(item string, last run) error {
	if last.terminal() || c.give.Beats == nil {
		return nil
	}

	owner, ends, err := c.leaseOf(last)
	if err != nil || !ends.After(c.give.Clock.now()) {
		return err
	}

	holder, holderErr := claim.OwnerOf(owner)
	if holderErr != nil {
		holder = owner
	}

	return fmt.Errorf("%w: %s still runs on version %s. The claim lease of %s ends at %s. Stop that build first, or wait for the lease to end, then run obee resume %s --on-this-build again",
		errStranded, last.Status.ID, last.Status.ApplicationVersion, holder, ends.UTC().Format(time.RFC3339), item)
}

// leaseOf returns the owner of the claim of a run and when its lease in the store ends. A run with no claim gives the zero time. A claim with no lease gives an end that is long past.
func (c *Client) leaseOf(last run) (owner string, ends time.Time, err error) {
	owner, err = currentOwner(c.flow, c.dir, last.Status.ID)
	if errors.Is(err, errNoOwner) {
		return "", time.Time{}, nil
	}

	in, inErr := last.input()
	if err = errors.Join(err, inErr); err != nil {
		return "", time.Time{}, err
	}

	ctx, cancel := c.give.limit(context.Background())
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
