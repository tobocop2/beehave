package flow

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/workflow"
)

// The step kinds and builtin names of the three typed reads.
const (
	// itemsKeyword is the argument of run_read and swarm that lists items, and the field of item_list. authorField and labelsField are fields of the rows of the typed reads.
	itemsKeyword = "items"
	authorField  = "author"
	labelsField  = "labels"

	itemListKind = "item_list"
	prListKind   = "pr_list"
	runReadKind  = "run_read"
)

// runNone is the state of an item that has no run to settle: it never ran, or its last run was canceled.
const runNone = "none"

var (
	errNoItemList = &absentError{message: "this build cannot list the items of the tracker. Use a build that sets a queue reader", step: "list the items of the tracker"}
	errNoPrList   = &absentError{message: "this build cannot list the open pull requests. Use a build that sets a queue reader", step: "list the open pull requests"}
)

// listedItem is one open item as item_list records it.
type listedItem struct {
	Item     string   `json:"item"`
	Severity string   `json:"severity"`
	Hold     bool     `json:"hold"`
	Parent   string   `json:"parent"`
	Assignee string   `json:"assignee"`
	Title    string   `json:"title"`
	Labels   []string `json:"labels"`
}

// listedPull is one open pull request as pr_list records it.
type listedPull struct {
	Number         int      `json:"number"`
	Branch         string   `json:"branch"`
	Base           string   `json:"base"`
	Author         string   `json:"author"`
	Draft          bool     `json:"draft"`
	Title          string   `json:"title"`
	Labels         []string `json:"labels"`
	ReviewDecision string   `json:"review_decision"`
}

// runRecord is the last run of one item as run_read records it. State is none, running, failed, stopped or done. Value is the JSON value that the last run of a done row returned.
// StopStep and StopReason name the step that ended a stopped run and the start of what it printed.
type runRecord struct {
	Item       string          `json:"item"`
	State      string          `json:"state"`
	Value      json.RawMessage `json:"value,omitempty"`
	StopStep   string          `json:"stop_step"`
	StopReason string          `json:"stop_reason"`
}

// runsRead is the output of run_read: the last run of each item, in the order of the call, and the number of free slots of the items queue.
type runsRead struct {
	Runs []runRecord `json:"runs"`
	Free int         `json:"free"`
}

// itemList lists the open items of the tracker of the project, and with a label only the items that carry it. The read is one recorded step, so a replay returns the first answer.
func (s *script) itemList(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	var name, label string

	if err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{"label": &label, nameKeyword: &name}); err != nil {
		return nil, err
	}

	if s.e.deps.Queue == nil {
		return nil, errNoItemList
	}

	items, err := countedStep(s, itemListKind, name, func(c context.Context) ([]listedItem, error) {
		return s.e.readItems(c, s.in.Project, label)
	})
	if err != nil {
		return nil, err
	}

	return starlarkstruct.FromStringDict(starlarkstruct.Default, starlark.StringDict{itemsKeyword: itemsValue(items)}), nil
}

// readItems reads the open items of the project that carry the label.
func (e *Engine) readItems(ctx context.Context, project, label string) ([]listedItem, error) {
	items, err := e.itemsOf(ctx, project, label)
	if err != nil {
		return nil, err
	}

	listed := make([]listedItem, len(items))

	for i, one := range items {
		listed[i] = listedItem{Item: one.ID, Severity: string(one.Severity), Hold: one.Hold, Parent: one.Parent, Assignee: one.Assignee, Title: one.Title, Labels: one.Labels}
	}

	return listed, nil
}

// itemsOf reads the open items of the project that carry the label. An empty label reads every open item.
func (e *Engine) itemsOf(ctx context.Context, project, label string) ([]model.Item, error) {
	items, err := e.deps.Queue.ItemList(ctx, project, label)
	if err != nil {
		return nil, fmt.Errorf("read the open items of project %s: %w", project, err)
	}

	return items, nil
}

// itemsValue makes the list of structs that item_list returns, with the fields item, severity, hold, parent, assignee, title and labels.
func itemsValue(items []listedItem) *starlark.List {
	values := make([]starlark.Value, len(items))

	for i, one := range items {
		values[i] = starlarkstruct.FromStringDict(starlarkstruct.Default, starlark.StringDict{
			itemName: starlark.String(one.Item), severityField: starlark.String(one.Severity), "hold": starlark.Bool(one.Hold), parentKeyword: starlark.String(one.Parent),
			"assignee": starlark.String(one.Assignee), titleKeyword: starlark.String(one.Title), labelsField: textList(one.Labels),
		})
	}

	return starlark.NewList(values)
}

// prList lists the open pull requests of the forge of the project, and with a base only the pull requests on that base. A project with no forge has none.
// The read is one recorded step, so a replay returns the first answer.
func (s *script) prList(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	var name, base string

	if err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{baseKeyword: &base, nameKeyword: &name}); err != nil {
		return nil, err
	}

	if s.e.deps.Queue == nil {
		return nil, errNoPrList
	}

	pulls, err := countedStep(s, prListKind, name, func(c context.Context) ([]listedPull, error) {
		return s.e.readPulls(c, s.in.Project, base)
	})
	if err != nil {
		return nil, err
	}

	return starlarkstruct.FromStringDict(starlarkstruct.Default, starlark.StringDict{pullsField: listedPullsValue(pulls)}), nil
}

// readPulls reads the open pull requests of the project on the base. An empty base reads every open pull request.
func (e *Engine) readPulls(ctx context.Context, project, base string) ([]listedPull, error) {
	pulls, err := e.pullsOf(ctx, project, base)
	if err != nil {
		return nil, err
	}

	listed := make([]listedPull, len(pulls))

	for i, one := range pulls {
		listed[i] = listedPull{
			Number: one.Number, Branch: one.Branch, Base: one.Base, Author: one.Author, Draft: one.Draft, Title: one.Title, Labels: one.Labels, ReviewDecision: one.ReviewDecision,
		}
	}

	return listed, nil
}

// pullsOf reads the open pull requests of the project on the base. An empty base reads every open pull request.
func (e *Engine) pullsOf(ctx context.Context, project, base string) ([]model.OpenPull, error) {
	pulls, err := e.deps.Queue.OpenPulls(ctx, project, base)
	if err != nil {
		return nil, fmt.Errorf("read the open pull requests of project %s: %w", project, err)
	}

	return pulls, nil
}

// listedPullsValue makes the list of structs that pr_list returns, with the fields number, branch, base, author, draft, title, labels and review_decision.
func listedPullsValue(pulls []listedPull) *starlark.List {
	values := make([]starlark.Value, len(pulls))

	for i, one := range pulls {
		values[i] = starlarkstruct.FromStringDict(starlarkstruct.Default, starlark.StringDict{
			numberField: starlark.MakeInt(one.Number), branchKeyword: starlark.String(one.Branch), baseKeyword: starlark.String(one.Base), authorField: starlark.String(one.Author),
			"draft": starlark.Bool(one.Draft), titleKeyword: starlark.String(one.Title), labelsField: textList(one.Labels), "review_decision": starlark.String(one.ReviewDecision),
		})
	}

	return starlark.NewList(values)
}

// runRead reads the last run of each item and the free slots. The read is one recorded step, so a replay returns the first answer.
func (s *script) runRead(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	name, ids, err := runReadArguments(b, args, kwargs)
	if err != nil {
		return nil, err
	}

	got, err := countedStep(s, runReadKind, name, func(context.Context) (runsRead, error) {
		views, free, err := s.e.lastRuns(ids)

		return runsRead{Runs: records(views), Free: free}, err
	})
	if err != nil {
		return nil, err
	}

	return runsReadValue(got)
}

// runReadArguments reads the name of the call and the item ids. An id that is no item id is an error.
func runReadArguments(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (string, []string, error) {
	var (
		name  string
		items starlark.Value
	)

	if err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{itemsKeyword: &items, nameKeyword: &name}); err != nil {
		return "", nil, err
	}

	ids, err := workflow.Words(itemsKeyword, items)
	if err != nil {
		return "", nil, err
	}

	return name, ids, checkItemIDs(ids)
}

// checkItemIDs is nil when each id is an item id.
func checkItemIDs(ids []string) error {
	for _, id := range ids {
		if model.ItemKind(id) == "" {
			return fmt.Errorf("%w %q: use an item id such as 12 or fix-login", ErrBadItem, id)
		}
	}

	return nil
}

// records takes the record of each view.
func records(views []runView) []runRecord {
	out := make([]runRecord, len(views))
	for i, view := range views {
		out[i] = view.runRecord
	}

	return out
}

// runView is the last run of one item: the record that run_read gives, and the run that the queue reads a foreign record from.
type runView struct {
	runRecord

	last run
}

// lastRuns reads the free slots, then the last run of each item. The slots come first: a run that ended before the slots were read shows as not running in the runs,
// so a caller can tell a slot that is free from a slot that a run left a moment ago. In the other order, a run could end between the two reads and show as running with a free slot.
// It reads the last run with latestRun, as spawn does, so the queue and spawn agree on what is open, failed and done.
func (e *Engine) lastRuns(ids []string) ([]runView, int, error) {
	free, err := e.freeSlots()
	if err != nil {
		return nil, 0, err
	}

	views := make([]runView, len(ids))

	for i, id := range ids {
		if views[i], err = e.runOf(id); err != nil {
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

	view := runView{runRecord: runRecord{Item: item, State: runNone}, last: last}

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
// A result that cannot be read fails the read. A new run of the item replaces that run as the last one, so the error names beehave run.
func (v *runView) settle() error {
	state, result, err := settledOf(v.last.Status)
	if err != nil {
		return fmt.Errorf("%w. Run beehave run %s to start a new attempt: the queue reads the new run in its place", err, v.Item)
	}

	if state == runStopped {
		v.State, v.StopStep, v.StopReason = queueStopped, result.Stopped.Step, model.Clip(result.Stopped.Output, StoppedOutputLimit, "…")

		return nil
	}

	v.State, v.Value = queueDone, result.Value

	return nil
}

// runsReadValue makes the struct that run_read returns: runs, a list of structs with the fields item, state, value, stop_step and stop_reason, and free.
func runsReadValue(got runsRead) (*starlarkstruct.Struct, error) {
	runs := make([]starlark.Value, len(got.Runs))

	for i, one := range got.Runs {
		value, err := valueOf(one.Value)
		if err != nil {
			return nil, fmt.Errorf("read the value of the last run of item %s: %w", one.Item, err)
		}

		runs[i] = starlarkstruct.FromStringDict(starlarkstruct.Default, starlark.StringDict{
			itemName: starlark.String(one.Item), stateField: starlark.String(one.State), "value": value,
			"stop_step": starlark.String(one.StopStep), "stop_reason": starlark.String(one.StopReason),
		})
	}

	return starlarkstruct.FromStringDict(starlarkstruct.Default, starlark.StringDict{runsField: starlark.NewList(runs), "free": starlark.MakeInt(got.Free)}), nil
}
