package flow

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/preflight"
	"github.com/tobocop2/obee/internal/workflow"
)

// The states of a row of the queue.
const (
	queueReady   = "ready"
	queueRunning = "running"
	queueDone    = "done"
	queueFailed  = "failed"
	queueHold    = "hold"
)

// holdStep is the name of the step that reads the hold of an item at the start of a run that spawn started.
const holdStep = "hold:start"

var errNoQueue = &absentError{message: "this build cannot read the open work. Use a build that sets a queue reader", step: "read the open work of the project"}

// queueReader reads the open work of a project: the open items of its tracker and the open pull requests of its forge.
type queueReader interface {
	OpenItems(ctx context.Context, project string) ([]model.Item, error)
	OpenPulls(ctx context.Context, project string) ([]model.PullRequest, error)
}

// queueRow is one open item of the queue, as the queue step records it. PR is 0 when the item has no open pull request.
// Parent is the container of the item, empty for loose work. Author and ChangesRequested come from the open pull request of the item's branch with the lowest number; Author is empty when there is none.
type queueRow struct {
	Item             string `json:"item"`
	State            string `json:"state"`
	Severity         string `json:"severity"`
	PR               int    `json:"pr"`
	Parent           string `json:"parent"`
	Author           string `json:"author"`
	ChangesRequested bool   `json:"changes_requested"`
}

// queued is the output of the queue step: the rows in the order of the queue, and the number of free slots of the items queue.
type queued struct {
	Rows []queueRow `json:"rows"`
	Free int        `json:"free"`
}

// joinedPull is what the open pull requests of one branch say about the item of that branch.
type joinedPull struct {
	number           int
	held             bool
	author           string
	changesRequested bool
}

// queue reads the open items and the open pull requests and returns one row for each item, with its state, and the number of free slots.
// The read is one recorded step, so a replay returns the first answer. A read that fails ends the step: the call returns no guess.
func (s *script) queue(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	var hold, label string

	if err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{nameKeyword: &label, holdKeyword: &hold}); err != nil {
		return nil, err
	}

	if s.e.deps.Queue == nil {
		return nil, errNoQueue
	}

	got, err := countedStep(s, "queue", label, func(c context.Context) (queued, error) {
		return s.e.readQueue(c, s.in.Project, hold)
	})
	if err != nil {
		return nil, err
	}

	return queuedValue(got), nil
}

// readQueue reads the tracker and the forge, then the runs of each item, and sorts the rows by severity and item id.
func (e *Engine) readQueue(ctx context.Context, project, holdLabel string) (queued, error) {
	items, err := e.deps.Queue.OpenItems(ctx, project)
	if err != nil {
		return queued{}, fmt.Errorf("read the open items of project %s: %w", project, err)
	}

	pulls, err := e.deps.Queue.OpenPulls(ctx, project)
	if err != nil {
		return queued{}, fmt.Errorf("read the open pull requests of project %s: %w", project, err)
	}

	// The slots are read before the rows. A run that ended before the slots were read shows as not running in the rows, so a caller can tell a slot
	// that is free from a slot that a run left a moment ago. In the other order, a run could end between the two reads and show as running with a free slot.
	free, err := e.freeSlots()
	if err != nil {
		return queued{}, err
	}

	rows, err := e.queueRows(items, joinPulls(pulls, holdLabel))

	return queued{Rows: rows, Free: free}, err
}

// queueRows gives one row for each item, sorted by severity and then by item id.
func (e *Engine) queueRows(items []model.Item, joined map[string]joinedPull) ([]queueRow, error) {
	rows := make([]queueRow, 0, len(items))

	for _, item := range items {
		row, err := e.rowOf(item, joined[item.ID])
		if err != nil {
			return nil, err
		}

		rows = append(rows, row)
	}

	slices.SortFunc(rows, func(a, b queueRow) int {
		return cmp.Or(strings.Compare(a.Severity, b.Severity), strings.Compare(a.Item, b.Item))
	})

	return rows, nil
}

// joinPulls groups the open pull requests by their head branch, which is the id of the item. Of many pull requests on one branch, the lowest number counts,
// and one with the hold label holds the item. A pull request with no head branch joins no item.
func joinPulls(pulls []model.PullRequest, holdLabel string) map[string]joinedPull {
	joined := map[string]joinedPull{}

	for _, pull := range pulls {
		if pull.HeadRefName == nil {
			continue
		}

		one := joined[*pull.HeadRefName]
		if one.number == 0 || pull.Number < one.number {
			one.number = pull.Number
			one.author = pull.Author.Login
			one.changesRequested = pull.ReviewDecision != nil && *pull.ReviewDecision == string(model.ReviewChangesRequested)
		}

		one.held = one.held || (holdLabel != "" && hasLabel(pull, holdLabel))
		joined[*pull.HeadRefName] = one
	}

	return joined
}

// hasLabel reports whether the pull request carries the label.
func hasLabel(pull model.PullRequest, name string) bool {
	for _, l := range pull.Labels {
		if l.Name == name {
			return true
		}
	}

	return false
}

// rowOf gives the state of one item. It reads the last run with latestRun, as spawn does, so the queue and spawn agree on what is open, failed and done.
// A hold decides only for an item that spawn could start: the run of an item has its own state.
func (e *Engine) rowOf(item model.Item, pull joinedPull) (queueRow, error) {
	last, found, err := latestRun(e.ctx, item.ID)
	if err != nil {
		return queueRow{}, err
	}

	state := queueReady

	switch {
	case found && !last.terminal():
		state = queueRunning
	case found && last.failed():
		state = queueFailed
	case found && last.Status.Status == dbos.WorkflowStatusSuccess:
		state = queueDone
	case item.Hold || pull.held:
		state = queueHold
	}

	return rowWith(item, pull, state), nil
}

// rowWith joins the item, its pull request and its state into a row.
func rowWith(item model.Item, pull joinedPull, state string) queueRow {
	return queueRow{
		Item: item.ID, State: state, Severity: string(item.Severity), PR: pull.number,
		Parent: item.Parent, Author: pull.author, ChangesRequested: pull.changesRequested,
	}
}

// freeSlots is the cap of the items queue less the runs that the library holds on it as open. The library keeps the count: the engine keeps none.
func (e *Engine) freeSlots() (int, error) {
	open, err := dbos.ListWorkflows(e.ctx, dbos.WithFilterQueueName(itemsQueueName), dbos.WithFilterStatus(dbos.WorkflowStatusPending, dbos.WorkflowStatusEnqueued))
	if err != nil {
		return 0, fmt.Errorf("count the open runs of the %s queue: %w", itemsQueueName, err)
	}

	return max(0, e.maxConcurrent-len(open)), nil
}

// queuedValue makes the struct that queue returns: rows, a list of structs with the fields item, state, severity, pr, parent, author and changes_requested, and free.
func queuedValue(got queued) *starlarkstruct.Struct {
	rows := make([]starlark.Value, len(got.Rows))

	for i, row := range got.Rows {
		rows[i] = starlarkstruct.FromStringDict(starlarkstruct.Default, starlark.StringDict{
			itemName: starlark.String(row.Item), "state": starlark.String(row.State), severityField: starlark.String(row.Severity), "pr": starlark.MakeInt(row.PR),
			parentKeyword: starlark.String(row.Parent), "author": starlark.String(row.Author), "changes_requested": starlark.Bool(row.ChangesRequested),
		})
	}

	return starlarkstruct.FromStringDict(starlarkstruct.Default, starlark.StringDict{"rows": starlark.NewList(rows), "free": starlark.MakeInt(got.Free)})
}

// holdAtStart stops a run that spawn started when its item is on hold in the tracker now. The tracker can change between the queue call and the start of the run.
// The read is a recorded step, so a replay takes the same branch. A build with no queue reader checks nothing.
func (s *script) holdAtStart() error {
	if s.in.Entry == "" || s.e.deps.Queue == nil {
		return nil
	}

	held, err := runStep(s.e, s.ctx, holdStep, func(c context.Context) (bool, error) {
		items, err := s.e.deps.Queue.OpenItems(c, s.in.Project)

		return slices.ContainsFunc(items, func(i model.Item) bool { return i.ID == s.in.Item && i.Hold }), err
	})
	if err != nil {
		return fmt.Errorf("read the hold of item %s: %w", s.in.Item, err)
	}

	if held {
		s.stopped = &Stopped{Step: "queue", RC: preflight.Held.Code(), Output: "item " + s.in.Item + " is on hold"}

		return errStopped
	}

	return nil
}
