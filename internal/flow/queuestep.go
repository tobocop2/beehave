package flow

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/workflow"
)

// The states of a row of the queue.
const (
	queueReady   = "ready"
	queueRunning = "running"
	queueDone    = "done"
	queueFailed  = "failed"
	queueStopped = "stopped"
	queueHold    = "hold"
)

// foreignReason is the reason that the run of a foreign review ends with, and pullMerged is the state of a merged pull request.
const (
	foreignReason = "foreign"
	pullMerged    = "MERGED"
)

// holdStep is the name of the step that reads the hold of an item at the start of a run that spawn started.
const holdStep = kindHold + ":start"

// heldCode is the exit code of a stop for a held item. It is the code that claim_stop gives for the hold label.
const heldCode = 9

var errNoQueue = &absentError{message: "this build cannot read the open work. Use a build that sets a queue reader", step: "read the open work of the project"}

// queueReader reads the open work of a project: the open items of its tracker and the open pull requests of its forge.
// ItemList reads the items that carry the label, or every open item when label is empty. OpenPulls reads the open pull requests on the base, or every one when base is empty.
type queueReader interface {
	ItemList(ctx context.Context, project, label string) ([]model.Item, error)
	OpenPulls(ctx context.Context, project, base string) ([]model.OpenPull, error)
	// PullState reads the state of one pull request, open or not: OPEN, CLOSED or MERGED.
	PullState(ctx context.Context, project string, pr int) (string, error)
}

// queueRow is one open item of the queue, as the queue step records it. PR is 0 when the item has no open pull request.
// Parent is the container of the item, empty for loose work. Author and ChangesRequested come from the open pull request of the item's branch with the lowest number; Author is empty when there is none.
// Labels are the tracker labels of the item. StopStep and StopReason name the step that ended a stopped row and the start of what it printed; both are empty for any other row.
type queueRow struct {
	Item             string   `json:"item"`
	State            string   `json:"state"`
	Severity         string   `json:"severity"`
	PR               int      `json:"pr"`
	Parent           string   `json:"parent"`
	Author           string   `json:"author"`
	ChangesRequested bool     `json:"changes_requested"`
	Labels           []string `json:"labels"`
	StopStep         string   `json:"stop_step"`
	StopReason       string   `json:"stop_reason"`
	// Value is the JSON value that the last run of a done row returned, whoever started it. It is empty on any other row and for a run that returned none.
	Value json.RawMessage `json:"value,omitempty"`
}

// openPull is one open pull request of the forge, as the queue step records it. Item is the open item that its head branch names, or empty when it names none.
type openPull struct {
	Number int    `json:"number"`
	Branch string `json:"branch"`
	Base   string `json:"base"`
	Author string `json:"author"`
	Draft  bool   `json:"draft"`
	Title  string `json:"title"`
	Item   string `json:"item"`
}

// queued is the output of the queue step: the rows in the order of the queue, the number of free slots of the items queue, and every open pull request.
type queued struct {
	Rows  []queueRow `json:"rows"`
	Free  int        `json:"free"`
	Pulls []openPull `json:"pulls"`
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
	var hold, name, tracked string

	if err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{nameKeyword: &name, holdKeyword: &hold, "label": &tracked}); err != nil {
		return nil, err
	}

	if s.e.deps.Queue == nil {
		return nil, errNoQueue
	}

	got, err := countedStep(s, "queue", name, func(c context.Context) (queued, error) {
		return s.e.readQueue(c, s.in.Project, hold, tracked)
	})
	if err != nil {
		return nil, err
	}

	return queuedValue(got)
}

// readQueue reads the tracker and the forge, then the runs of each item, and sorts the rows by severity and item id.
// A tracked label other than "" keeps only the items that carry that tracker label. The open pull requests are all kept, also those of an item that the label leaves out.
func (e *Engine) readQueue(ctx context.Context, project, holdLabel, tracked string) (queued, error) {
	items, err := e.itemsOf(ctx, project, "")
	if err != nil {
		return queued{}, err
	}

	pulls, err := e.pullsOf(ctx, project, "")
	if err != nil {
		return queued{}, err
	}

	kept := withLabel(items, tracked)

	// lastRuns reads the slots before the runs, so a slot that a run left a moment ago does not show as taken.
	runs, free, err := e.lastRuns(itemIDsOf(kept))
	if err != nil {
		return queued{}, err
	}

	rows, err := e.queueRows(ctx, kept, runs, joinPulls(pulls, holdLabel))

	return queued{Rows: rows, Free: free, Pulls: openPulls(pulls, items)}, err
}

// itemIDsOf lists the ids of the items, in order.
func itemIDsOf(items []model.Item) []string {
	ids := make([]string, len(items))
	for i, item := range items {
		ids[i] = item.ID
	}

	return ids
}

// openPulls keeps every open pull request, with the open item that its head branch names. The items are all the open items, before the tracked label.
func openPulls(pulls []model.OpenPull, items []model.Item) []openPull {
	kept := make([]openPull, 0, len(pulls))

	for _, pull := range pulls {
		named := ""

		if slices.ContainsFunc(items, func(item model.Item) bool { return model.ItemBranch(item.ID) == pull.Branch }) {
			named = pull.Branch
		}

		kept = append(kept, openPull{Number: pull.Number, Branch: pull.Branch, Base: pull.Base, Author: pull.Author, Draft: pull.Draft, Title: pull.Title, Item: named})
	}

	return kept
}

// withLabel returns the items that carry the tracker label. An empty label keeps every item.
func withLabel(items []model.Item, tracked string) []model.Item {
	if tracked == "" {
		return items
	}

	return slices.DeleteFunc(slices.Clone(items), func(item model.Item) bool { return !slices.Contains(item.Labels, tracked) })
}

// queueRows gives one row for each item, sorted by severity and then by item id.
func (e *Engine) queueRows(ctx context.Context, items []model.Item, runs []runView, joined map[string]joinedPull) ([]queueRow, error) {
	rows := make([]queueRow, 0, len(items))

	for i, item := range items {
		row, err := e.rowOf(ctx, item, joined[item.ID], runs[i])
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
// and one with the hold label holds the item.
func joinPulls(pulls []model.OpenPull, holdLabel string) map[string]joinedPull {
	joined := map[string]joinedPull{}

	for _, pull := range pulls {
		one := joined[pull.Branch]
		if one.number == 0 || pull.Number < one.number {
			one.number = pull.Number
			one.author = pull.Author
			one.changesRequested = pull.ReviewDecision == string(model.ReviewChangesRequested)
		}

		one.held = one.held || (holdLabel != "" && slices.Contains(pull.Labels, holdLabel))
		joined[pull.Branch] = one
	}

	return joined
}

// rowOf gives the state of one item from its last run. A hold decides only for an item that spawn could start: the run of an item has its own state.
func (e *Engine) rowOf(ctx context.Context, item model.Item, pull joinedPull, view runView) (queueRow, error) {
	settled, err := e.stateOf(ctx, view, item.Hold || pull.held)
	if err != nil {
		return queueRow{}, err
	}

	return rowWith(item, pull, settled), nil
}

// rowState is the state of a row and, for a stopped row, the stop that its run ended with.
type rowState struct {
	state      string
	stopStep   string
	stopReason string
	value      json.RawMessage
}

// stateOf is the state of an item from its last run. A run that succeeded is done, or stopped when its result records a stop, unless it is owed another visit.
// held is the hold of the item.
func (e *Engine) stateOf(ctx context.Context, view runView, held bool) (rowState, error) {
	switch view.State {
	case queueRunning, queueFailed:
		return rowState{state: view.State}, nil
	case queueStopped:
		return rowState{state: queueStopped, stopStep: view.StopStep, stopReason: view.StopReason}, nil
	case queueDone:
		return e.doneState(ctx, view, held)
	}

	return rowState{state: idleState(held)}, nil
}

// doneState is the state of an item whose last run succeeded: idle when the run is owed another visit, hold when the item is held, else done with the value of the run.
func (e *Engine) doneState(ctx context.Context, view runView, held bool) (rowState, error) {
	revisit, err := e.owesVisit(ctx, view)
	if err != nil {
		return rowState{}, err
	}

	switch {
	case revisit:
		return rowState{state: idleState(held)}, nil
	case held:
		// A held item is not done: a landing that the round rejected puts the item on hold, and the next pass must not offer it again.
		return rowState{state: queueHold}, nil
	}

	return rowState{state: queueDone, value: view.Value}, nil
}

// idleState is the state of an item with no run to settle: hold, or ready.
func idleState(held bool) string {
	if held {
		return queueHold
	}

	return queueReady
}

// foreignRecord is what the run of a foreign review records in its value: the reason, the number of the reviewed pull request and the findings it left open.
type foreignRecord struct {
	Reason   string            `json:"reason"`
	PR       int               `json:"pr"`
	Findings []json.RawMessage `json:"findings"`
}

// owesVisit reports whether a run that ended with success is owed another visit: its last run ended with the reason foreign, it recorded open findings,
// and its recorded pull request merged. The state is read for the recorded number: the pull request is not looked up again.
// A value that is not a foreign record, or a run that recorded no value, owes none.
func (e *Engine) owesVisit(ctx context.Context, view runView) (bool, error) {
	recorded, ok := foreignRecordOf(view.Value)
	if !ok {
		return false, nil
	}

	project, err := view.last.project()
	if err != nil {
		return false, err
	}

	state, err := e.deps.Queue.PullState(ctx, project, recorded.PR)
	if err != nil {
		return false, fmt.Errorf("read the state of pull request %d for run %s: %w", recorded.PR, view.last.Status.ID, err)
	}

	return state == pullMerged, nil
}

// foreignRecordOf reads the foreign record in the value of a result. It is false when the result holds no value, or a value that is not a foreign record with a pull request and open findings.
func foreignRecordOf(value json.RawMessage) (foreignRecord, bool) {
	var recorded foreignRecord

	if json.Unmarshal(value, &recorded) != nil {
		return foreignRecord{}, false
	}

	return recorded, recorded.Reason == foreignReason && recorded.PR != 0 && len(recorded.Findings) > 0
}

// rowWith joins the item, its pull request and its state into a row.
func rowWith(item model.Item, pull joinedPull, settled rowState) queueRow {
	row := queueRow{
		Item: item.ID, State: settled.state, Severity: string(item.Severity), PR: pull.number,
		Parent: item.Parent, Author: pull.author, ChangesRequested: pull.changesRequested, Labels: append([]string{}, item.Labels...), Value: settled.value,
	}

	row.StopStep, row.StopReason = settled.stopStep, settled.stopReason

	return row
}

// freeSlots is the cap of the items queue less the runs that the library holds on it as open. The library keeps the count: the engine keeps none.
func (e *Engine) freeSlots() (int, error) {
	open, err := e.store().Open(itemsQueueName)
	if err != nil {
		return 0, fmt.Errorf("count the open runs of the %s queue: %w", itemsQueueName, err)
	}

	return max(0, e.maxConcurrent-len(open)), nil
}

// queuedValue makes the struct that queue returns: rows, a list of structs with the fields item, state, severity, pr, parent, author, changes_requested, labels, stop_step, stop_reason and value;
// free; and pulls, a list of structs with the fields number, branch, base, author, draft, title and item.
func queuedValue(got queued) (*starlarkstruct.Struct, error) {
	rows := make([]starlark.Value, len(got.Rows))

	for i, row := range got.Rows {
		value, err := valueOf(row.Value)
		if err != nil {
			return nil, fmt.Errorf("read the value of the last run of item %s: %w", row.Item, err)
		}

		rows[i] = starlarkstruct.FromStringDict(starlarkstruct.Default, starlark.StringDict{
			"value":  value,
			itemName: starlark.String(row.Item), stateField: starlark.String(row.State), severityField: starlark.String(row.Severity), "pr": starlark.MakeInt(row.PR),
			parentKeyword: starlark.String(row.Parent), authorField: starlark.String(row.Author), "changes_requested": starlark.Bool(row.ChangesRequested),
			"labels": textList(row.Labels), "stop_step": starlark.String(row.StopStep), "stop_reason": starlark.String(row.StopReason),
		})
	}

	return starlarkstruct.FromStringDict(starlarkstruct.Default, starlark.StringDict{
		"rows": starlark.NewList(rows), "free": starlark.MakeInt(got.Free), pullsField: openPullsValue(got.Pulls),
	}), nil
}

// openPullsValue makes the list of open pull requests that queue returns.
func openPullsValue(pulls []openPull) *starlark.List {
	values := make([]starlark.Value, len(pulls))

	for i, pull := range pulls {
		values[i] = starlarkstruct.FromStringDict(starlarkstruct.Default, starlark.StringDict{
			numberField: starlark.MakeInt(pull.Number), "branch": starlark.String(pull.Branch), baseKeyword: starlark.String(pull.Base),
			authorField: starlark.String(pull.Author), "draft": starlark.Bool(pull.Draft), titleKeyword: starlark.String(pull.Title), itemName: starlark.String(pull.Item),
		})
	}

	return starlark.NewList(values)
}

// holdAtStart stops a run that spawn started when its item is on hold in the tracker now. The tracker can change between the queue call and the start of the run.
// The read is a recorded step, so a replay takes the same branch. A build with no queue reader checks nothing.
func (s *script) holdAtStart() error {
	if s.in.Entry == "" || s.e.deps.Queue == nil {
		return nil
	}

	held, err := runStep(s.e, s.ctx, holdStep, func(c context.Context) (bool, error) {
		items, err := s.e.deps.Queue.ItemList(c, s.in.Project, "")

		return slices.ContainsFunc(items, func(i model.Item) bool { return i.ID == s.in.Item && i.Hold }), err
	})
	if err != nil {
		return fmt.Errorf("read the hold of item %s: %w", s.in.Item, err)
	}

	if held {
		s.stopped = &Stopped{Step: "queue", RC: heldCode, Output: "item " + s.in.Item + " is on hold"}

		return errStopped
	}

	return nil
}
