package flow

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/dbos-inc/dbos-transact-golang/dbos"

	"github.com/tobocop2/beehest/internal/model"
)

// The states of the last run of an item. none is the state of an item that has no run to settle: it never ran, or its last run was canceled.
const (
	runNone      = "none"
	queueRunning = "running"
	queueDone    = "done"
	queueFailed  = "failed"
	queueStopped = "stopped"
)

// runView is the last run of one item. State is none, running, failed, stopped or done. Value is the JSON value that the last run of a done item returned.
// StopStep and StopReason name the step that ended a stopped run and the start of what it printed.
type runView struct {
	Item       string
	State      string
	Value      json.RawMessage
	StopStep   string
	StopReason string

	last run
}

// itemsOf reads the open items of the project that carry the label. An empty label reads every open item.
func (e *Engine) itemsOf(ctx context.Context, project, label string) ([]model.Item, error) {
	items, err := e.deps.Queue.ItemList(ctx, project, label)
	if err != nil {
		return nil, fmt.Errorf("read the open items of project %s: %w", project, err)
	}

	return items, nil
}

// pullsOf reads the open pull requests of the project on the base. An empty base reads every open pull request.
func (e *Engine) pullsOf(ctx context.Context, project, base string) ([]model.OpenPull, error) {
	pulls, err := e.deps.Queue.OpenPulls(ctx, project, base)
	if err != nil {
		return nil, fmt.Errorf("read the open pull requests of project %s: %w", project, err)
	}

	return pulls, nil
}

// lastRuns reads the free slots, then the last run of each item. The slots come first: a run that ended before the slots were read shows as not running in the runs,
// so a caller can tell a slot that is free from a slot that a run left a moment ago. In the other order, a run could end between the two reads and show as running with a free slot.
// It reads the last run with latestRun, as spawn does, so the queue and spawn agree on what is open, failed and done.
func (e *Engine) lastRuns(ids []string) ([]runView, int, error) {
	return readRuns(ids, e.freeSlots, e.runOf)
}

// freeSlots is the cap of the items queue less the runs that the library holds on it as open. The library keeps the count: the engine keeps none.
func (e *Engine) freeSlots() (int, error) {
	open, err := e.store().Open(itemsQueueName)
	if err != nil {
		return 0, fmt.Errorf("count the open runs of the %s queue: %w", itemsQueueName, err)
	}

	return max(0, e.maxConcurrent-len(open)), nil
}

// readRuns calls slots once, then runOf for each id in order. The order is the contract of lastRuns.
func readRuns(ids []string, slots func() (int, error), runOf func(string) (runView, error)) ([]runView, int, error) {
	free, err := slots()
	if err != nil {
		return nil, 0, err
	}

	views := make([]runView, len(ids))

	for i, id := range ids {
		if views[i], err = runOf(id); err != nil {
			return nil, 0, err
		}
	}

	return views, free, nil
}

// runOf reads the last run of one item. A run that is open is running, one that ended with an error is failed, one that ended with success is stopped or done, and any other has no state to settle.
func (e *Engine) runOf(item string) (runView, error) {
	last, found, err := latestRun(e.store(), item)
	if err != nil {
		return runView{}, err
	}

	view := runView{Item: item, State: runNone, last: last}

	switch {
	case found && !last.terminal():
		view.State = queueRunning
	case found && last.failed():
		view.State = queueFailed
	case found && last.Status.Status == dbos.WorkflowStatusSuccess:
		err = view.settle()
	}

	return view, err
}

// settle reads the result of a run that ended with success: stopped when the result records a stop, else done with its value.
// A result that cannot be read fails the read. A new run of the item replaces that run as the last one, so the error names beehest run.
func (v *runView) settle() error {
	state, result, err := settledOf(v.last.Status)
	if err != nil {
		return fmt.Errorf("%w. Run beehest run %s to start a new attempt: the queue reads the new run in its place", err, v.Item)
	}

	if state == runStopped {
		v.State, v.StopStep, v.StopReason = queueStopped, result.Stopped.Step, model.Clip(result.Stopped.Output, StoppedOutputLimit, "…")

		return nil
	}

	v.State, v.Value = queueDone, result.Value

	return nil
}
