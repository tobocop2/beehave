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

// watchReady reads the ready pull request of an item until it merges. A change request or a conflict sets the pull request to draft and ends the watch with the reason; a merge ends it with no reason; a close ends it with Closed set and no draft call.
// The wait between two reads is a durable sleep of the workflow, so a restart resumes the watch and no timer of ours runs.
// The count of failed reads is a local of the loop. The workflow replays the loop on a restart, so the count is rebuilt from the recorded steps.
// A head other than pinned, the head that was marked ready, counts as a send back: the review no longer covers the head.
func (e *Engine) watchReady(ctx dbos.Context, project, item, pinned string) (watchEnd, error) {
	pr, err := runStep(e, ctx, watchStep, func(c context.Context) (int, error) {
		found, err := e.deps.Forge.Pull(c, project, item)

		return found.Number, err
	})
	if err != nil {
		return watchEnd{}, fmt.Errorf("%s of item %s: %w", watchStep, item, err)
	}

	return e.watchLoop(ctx, project, pr, item, pinned)
}

// watchLoop reads the pull request until it merges, goes back to draft, or the reads fail watchFailures times in a row.
func (e *Engine) watchLoop(ctx dbos.Context, project string, pr int, item, pinned string) (watchEnd, error) {
	for failed := 0; ; {
		outcome, err := e.readWatch(ctx, project, pr, pinned)

		failed = nextFailed(failed, err)
		if failed >= watchFailures {
			return watchEnd{}, fmt.Errorf("%s of item %s: %w", watchStep, item, err)
		}

		if end, over, err := e.watchSettle(ctx, project, pr, item, pinned, outcome, err); over {
			return end, err
		}

		if err := e.pause(ctx, item); err != nil {
			return watchEnd{}, err
		}
	}
}

// closedText is what the owner reads when the watch ends on a pull request that was closed with no merge.
const closedText = "the pull request was closed"

// watchEnd is how a finished watch ended. A merge leaves it empty. A send back sets Reason. A close sets Closed, which is not a send back: nothing is drafted and no rework follows.
type watchEnd struct {
	Reason string
	Closed bool
}

// watchSettle acts on a good read. It reports whether the watch is over and how it ended. A failed read is never over here.
func (e *Engine) watchSettle(ctx dbos.Context, project string, pr int, item, pinned string, outcome review.Outcome, readErr error) (watchEnd, bool, error) {
	if readErr != nil {
		return watchEnd{}, false, nil
	}

	switch outcome.Kind {
	case review.Merged:
		return watchEnd{}, true, nil
	case review.Closed:
		return watchEnd{Reason: closedText, Closed: true}, true, nil
	case review.SendBack:
		return e.settleSendBack(ctx, project, pr, item, pinned, outcome)
	case review.Waiting:
	}

	return watchEnd{}, false, nil
}

// settleSendBack sends the pull request back, then settles on the fresh read of that step: a pull request that merged or closed since is not drafted.
func (e *Engine) settleSendBack(ctx dbos.Context, project string, pr int, item, pinned string, outcome review.Outcome) (watchEnd, bool, error) {
	fresh, err := e.sendBack(ctx, project, pr, item, pinned)
	if err != nil {
		return watchEnd{}, true, err
	}

	if fresh.Kind == review.SendBack {
		return watchEnd{Reason: outcome.Reason}, true, nil
	}

	return e.watchSettle(ctx, project, pr, item, pinned, fresh, nil)
}

// pause is the durable sleep between two reads.
func (e *Engine) pause(ctx dbos.Context, item string) error {
	if _, err := dbos.Sleep(ctx, cmp.Or(e.deps.Clock.Watch, watchEvery)); err != nil {
		return fmt.Errorf("%s of item %s: %w", watchStep, item, err)
	}

	return nil
}

// readWatch reads the pull request once, as one durable step.
func (e *Engine) readWatch(ctx dbos.Context, project string, pr int, pinned string) (review.Outcome, error) {
	return runStep(e, ctx, watchStep, func(c context.Context) (review.Outcome, error) {
		read, err := e.deps.Watch.Watch(c, project, pr)

		return movedHead(read, pinned), err
	})
}

// movedHead turns a waiting read into a send back when the head is not the one that was marked ready. A push after the review leaves a head that nobody reviewed.
// A read with no head, which a forge that cannot name one gives, is left as it is.
func movedHead(read review.Outcome, pinned string) review.Outcome {
	if read.Kind != review.Waiting || pinned == "" || read.Head == "" || read.Head == pinned {
		return read
	}

	return review.Outcome{Kind: review.SendBack, Reason: fmt.Sprintf("the head moved to %s after the review of %s", read.Head, pinned), Head: read.Head}
}

// nextFailed counts the failed reads in a row: a good read resets the count.
func nextFailed(failed int, err error) int {
	if err != nil {
		return failed + 1
	}

	return 0
}

// sendBack reads the pull request again and sets it to draft only if the fresh read still says send back. The read before it is a step ago:
// the pull request can have merged or closed since. It returns the fresh read, so a caller settles on what the pull request is now.
func (e *Engine) sendBack(ctx dbos.Context, project string, pr int, item, pinned string) (review.Outcome, error) {
	fresh, err := runStep(e, ctx, watchStep, func(c context.Context) (review.Outcome, error) {
		again, err := e.deps.Watch.Watch(c, project, pr)
		again = movedHead(again, pinned)

		if err != nil || again.Kind != review.SendBack || again.Draft {
			return again, err
		}

		return again, e.deps.Watch.SetDraft(c, project, pr, true)
	})
	if err != nil {
		return review.Outcome{}, fmt.Errorf("%s of item %s: %w", watchStep, item, err)
	}

	return fresh, nil
}
