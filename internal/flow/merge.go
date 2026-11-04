package flow

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"

	"github.com/tobocop2/beehave/internal/merge"
)

// mergeStep is the name of the step that merges the ready pull request.
const mergeStep = "merge"

// queuedEvery is the time between two reads of a pull request whose merge the forge holds. queuedReads is the number of reads, the last one included:
// the wait is about 20 minutes. They are constants, not settings. A wait that ends unmerged disarms the queued merge and defers the item.
const (
	queuedEvery = 30 * time.Second
	queuedReads = 40
)

// errMergeRefused is returned when the merge refuses a pull request for a reason other than its owner: the owner has to act.
var errMergeRefused = errors.New("the merge was refused")

// merger merges the pull request of an item, pinned to head. It returns the verdict of the merge with the lines to show.
// A pull request of an own identity is refused with merge.OwnWork unless the settings say another step approved it.
// A pull request in a native stack gets a merge.Queued result: the forge holds the merge, and Poll reads it. Last is true for the final read of the wait.
type merger interface {
	Merge(ctx context.Context, project, item, head string, settings MergeSettings) (merge.Result, error)
	Poll(ctx context.Context, project, item, head string, settings MergeSettings, last bool) (merge.Result, error)
	// Disarm turns off the queued merge of the pull request of an item, for a wait that ends with no verdict. It returns an error when the merge may still be armed.
	Disarm(ctx context.Context, project, item, head string, settings MergeSettings) error
}

// mergeReady merges the ready pull request, pinned to the head that the gate and the review read.
// A merge that was done, or that was refused as our own work, goes on to the watch, which ends when the owner merges. Any other refusal ends the item.
// The caller makes sure that the build has a merger.
func (e *Engine) mergeReady(ctx dbos.Context, project, item, head string, settings MergeSettings) error {
	result, err := e.mergeQueued(ctx, project, item, head, settings)
	if err != nil {
		return fmt.Errorf("%s of item %s: %w", mergeStep, item, err)
	}

	switch result.Stop {
	case merge.Done, merge.OwnWork:
		return nil
	case merge.Refused, merge.NoWorkflowScope:
	}

	return fmt.Errorf("%w: item %s: %s", errMergeRefused, item, strings.Join(result.Lines, "; "))
}

// mergeQueued writes the intent, runs the merge on the exclusive queue, and waits while the forge holds a queued merge.
func (e *Engine) mergeQueued(ctx dbos.Context, project, item, head string, settings MergeSettings) (merge.Result, error) {
	settings, err := e.withIntent(ctx, project, item, "merge:intent", settings)
	if err != nil {
		return merge.Result{}, err
	}

	ran, err := e.queueExclusive(ctx, ExclusiveInput{Kind: kindMerge, Project: project, Item: item, Head: head, Settings: settings})
	if err != nil {
		return merge.Result{}, err
	}

	return e.awaitQueued(ctx, "merge:poll", queuedRead{project: project, item: item, head: head, settings: settings}, ran.Merge)
}

// withIntent records the intent of a merge as a step of its own, before the merge call queues anything. The intent is the record that batch_merge writes:
// a run that repeats after a crash finds the pull request MERGED at the gated head, and the intent shows the merge is ours. A build with no batch host
// writes none, and a pull request that is not OPEN gets none: the merge refuses it as before.
func (e *Engine) withIntent(ctx dbos.Context, project, item, name string, settings MergeSettings) (MergeSettings, error) {
	if e.deps.Batch == nil {
		return settings, nil
	}

	read, err := runStep(e, ctx, name, func(c context.Context) (intentRead, error) {
		return e.plainIntent(c, project, item, settings)
	}, dbos.WithStepMaxRetries(stepRetries))
	if err != nil {
		return settings, err
	}

	if read.Refused == "" {
		settings.Intent = &read.Intent
	}

	return settings, nil
}

// plainIntent reads the intent of a merge. A pull request that is not OPEN is a result and not an error: DBOS keeps the text of a recorded error and not its type.
func (e *Engine) plainIntent(ctx context.Context, project, item string, settings MergeSettings) (intentRead, error) {
	intent, err := e.deps.Batch.Intent(ctx, project, item, settings.Repo, settings.Base, "", e.deps.Clock.now())
	if errors.Is(err, ErrMemberNotOpen) {
		return intentRead{Refused: err.Error()}, nil
	}

	return intentRead{Intent: intent}, err
}

// queuedRead names the pull request whose queued merge awaitQueued reads.
type queuedRead struct {
	project, item, head string
	settings            MergeSettings
}

// awaitQueued waits for a merge that the forge holds. Each round is a durable sleep, then a recorded read step, so a crash in the wait resumes at the round
// it reached and does not queue the merge again. The last read disarms a merge that is still open. A result that is not merge.Queued ends the wait.
func (e *Engine) awaitQueued(ctx dbos.Context, name string, q queuedRead, result merge.Result) (merge.Result, error) {
	for round := 1; result.Reason == merge.Queued && round <= queuedReads; round++ {
		if _, err := dbos.Sleep(ctx, cmp.Or(e.deps.Clock.Queued, queuedEvery)); err != nil {
			return merge.Result{}, e.abandoned(ctx, q, err)
		}

		read, err := runStep(e, ctx, fmt.Sprintf("%s@%d", name, round), func(c context.Context) (merge.Result, error) {
			return e.deps.Merge.Poll(c, q.project, q.item, q.head, q.settings, round == queuedReads)
		})
		if err != nil {
			return merge.Result{}, e.abandoned(ctx, q, err)
		}

		result = read
	}

	if result.Reason == merge.Queued {
		return merge.Result{}, fmt.Errorf("%w: the queued merge was still open after the last read", errMergeRefused)
	}

	return result, nil
}

// disarmTimeout bounds the call that turns off a queued merge after a wait that ended with no verdict.
const disarmTimeout = 30 * time.Second

// abandoned turns off the queued merge when the wait ends with no verdict, and returns the error of the wait. A merge that stays armed on the forge can land
// at a head that no review saw, with no run left to read it. Two cases leave it armed: the owner cancels the run, and the engine fails.
//
// The call is not a recorded step. A canceled run cannot start a step, and the call is safe to repeat: the forge succeeds when no merge is armed, and a
// replay that reaches the same error calls it again. It runs on a context that the cancel does not reach, with a timeout of its own.
// A stop of the engine (a shutdown or a restart) does not disarm: the run resumes at the same round, and the merge it waits for must stay armed.
func (e *Engine) abandoned(ctx context.Context, q queuedRead, err error) error {
	if !endsTheMerge(ctx) {
		return err
	}

	off, cancel := context.WithTimeout(context.WithoutCancel(ctx), disarmTimeout)
	defer cancel()

	if offErr := e.deps.Merge.Disarm(off, q.project, q.item, q.head, q.settings); offErr != nil {
		return fmt.Errorf("%w; the queued merge of item %s may still be armed and must be turned off by hand: %w", err, q.item, offErr)
	}

	return err
}

// endsTheMerge says if an error of the wait means the run is over for good. An owner cancel and a timeout of the run do. An engine stop does not: DBOS
// cancels the context of every run with a cause of its own when it shuts down, and the run resumes later. Any other error, with a live context, is a failure of the run.
func endsTheMerge(ctx context.Context) bool {
	if ctx.Err() == nil {
		return true
	}

	cause := context.Cause(ctx)

	return errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded)
}
