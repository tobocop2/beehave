package flow

import (
	"cmp"
	"context"
	"fmt"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"

	"github.com/tobocop2/obee/internal/review"
)

// watchEvery is the time between two reads of a ready pull request. A day of waiting costs 48 reads and 48 sleeps in the durable history.
const watchEvery = time.Hour

// watchStep is the name of the step that reads a ready pull request.
const watchStep = "watch"

// watchFailures is the number of failed reads in a row that end the watch with the last error.
const watchFailures = 3

// watcher reads a ready pull request and returns it to draft. The forge backend implements both.
type watcher interface {
	Watch(ctx context.Context, project string, pr int) (review.Outcome, error)
	SetDraft(ctx context.Context, project string, pr int, draft bool) error
}

// watchReady reads the ready pull request of an item until it merges. A change request or a conflict sets the pull request to draft and ends the watch with the reason; a merge ends it with no reason.
// The wait between two reads is a durable sleep of the workflow, so a restart resumes the watch and no timer of ours runs.
// The count of failed reads is a local of the loop. The workflow replays the loop on a restart, so the count is rebuilt from the recorded steps.
func (e *Engine) watchReady(ctx dbos.Context, project, item string) (string, error) {
	pr, err := runStep(e, ctx, watchStep, func(c context.Context) (int, error) {
		found, err := e.deps.Forge.Pull(c, project, item)

		return found.Number, err
	})
	if err != nil {
		return "", fmt.Errorf("%s of item %s: %w", watchStep, item, err)
	}

	return e.watchLoop(ctx, project, pr, item)
}

// watchLoop reads the pull request until it merges, goes back to draft, or the reads fail watchFailures times in a row.
func (e *Engine) watchLoop(ctx dbos.Context, project string, pr int, item string) (string, error) {
	for failed := 0; ; {
		outcome, err := e.readWatch(ctx, project, pr)

		failed = nextFailed(failed, err)
		if failed >= watchFailures {
			return "", fmt.Errorf("%s of item %s: %w", watchStep, item, err)
		}

		if reason, over, err := e.watchSettle(ctx, project, pr, item, outcome, err); over {
			return reason, err
		}

		if err := e.pause(ctx, item); err != nil {
			return "", err
		}
	}
}

// watchSettle acts on a good read. It reports whether the watch is over and, after a send back, the reason. A failed read is never over here.
func (e *Engine) watchSettle(ctx dbos.Context, project string, pr int, item string, outcome review.Outcome, readErr error) (string, bool, error) {
	if readErr != nil {
		return "", false, nil
	}

	switch outcome.Kind {
	case review.Merged:
		return "", true, nil
	case review.SendBack:
		return outcome.Reason, true, e.sendBack(ctx, project, pr, item)
	case review.Waiting:
	}

	return "", false, nil
}

// pause is the durable sleep between two reads.
func (e *Engine) pause(ctx dbos.Context, item string) error {
	if _, err := dbos.Sleep(ctx, cmp.Or(e.deps.Clock.Watch, watchEvery)); err != nil {
		return fmt.Errorf("%s of item %s: %w", watchStep, item, err)
	}

	return nil
}

// readWatch reads the pull request once, as one durable step.
func (e *Engine) readWatch(ctx dbos.Context, project string, pr int) (review.Outcome, error) {
	return runStep(e, ctx, watchStep, func(c context.Context) (review.Outcome, error) {
		return e.deps.Watch.Watch(c, project, pr)
	})
}

// nextFailed counts the failed reads in a row: a good read resets the count.
func nextFailed(failed int, err error) int {
	if err != nil {
		return failed + 1
	}

	return 0
}

// sendBack sets the pull request to draft.
func (e *Engine) sendBack(ctx dbos.Context, project string, pr int, item string) error {
	_, err := runStep(e, ctx, watchStep, func(c context.Context) (struct{}, error) {
		return struct{}{}, e.deps.Watch.SetDraft(c, project, pr, true)
	})
	if err != nil {
		return fmt.Errorf("%s of item %s: %w", watchStep, item, err)
	}

	return nil
}
