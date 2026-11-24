package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"go.starlark.net/starlark"

	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/workflow"
)

// The step kinds and the builtin names of the three reads of the open work. The library queue.star builds the queue from them.
const (
	itemListKind = "item_list"
	prListKind   = "pr_list"
	runReadKind  = "run_read"
)

// holdStep is the name of the step that reads the hold of an item at the start of a run that spawn started.
const holdStep = kindHold + ":start"

// heldCode is the exit code of a stop for a held item. It is the code that claim_stop gives for the hold label.
const heldCode = 9

var (
	errNoItemList = &absentError{message: "this build cannot read the open items. Use a build that sets a queue reader", step: "read the open items of the project"}
	errNoPullList = &absentError{message: "this build cannot read the open pull requests. Use a build that sets a queue reader", step: "read the open pull requests of the project"}
)

// queueReader reads the open work of a project: the open items of its tracker and the open pull requests of its forge.
// ItemList reads the items that carry the label, or every open item when label is empty. OpenPulls reads the open pull requests on the base, or every one when base is empty.
type queueReader interface {
	ItemList(ctx context.Context, project, label string) ([]model.Item, error)
	OpenPulls(ctx context.Context, project, base string) ([]model.OpenPull, error)
	// PullState reads the state of one pull request, open or not: OPEN, CLOSED or MERGED.
	PullState(ctx context.Context, project string, pr int) (string, error)
	// IssueState reads the state of one issue: OPEN or CLOSED. A forge that cannot answer is an error, never a state.
	IssueState(ctx context.Context, project string, number int) (string, error)
	// PullClosing reads the issues that merging the pull request with the number closes, as ClosingIssues of the forge of a run lists them. A pull request that is gone is an error.
	PullClosing(ctx context.Context, project string, pr int) ([]model.IssueRef, error)
}

// listDefs lists the builtins that read the open work of the project.
func listDefs() []builtinDef {
	return []builtinDef{
		{name: itemListKind, call: (*script).itemList},
		{name: prListKind, call: (*script).prList},
		{name: runReadKind, call: (*script).runRead},
	}
}

// listedItem is one open item of the result of item_list. Labels is an empty list, never null.
type listedItem struct {
	ID       string   `json:"id"`
	Severity string   `json:"severity"`
	Hold     bool     `json:"hold"`
	Parent   string   `json:"parent"`
	Assignee string   `json:"assignee"`
	Title    string   `json:"title"`
	Labels   []string `json:"labels"`
}

// listedPull is one open pull request of the result of pr_list. Labels is an empty list, never null.
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

// listedRun is the last run of one item in the result of run_read. Value is the JSON value of a done run, and null for any other.
type listedRun struct {
	Item       string          `json:"item"`
	State      string          `json:"state"`
	Value      json.RawMessage `json:"value"`
	StopStep   string          `json:"stop_step"`
	StopReason string          `json:"stop_reason"`
}

// itemListOutputs are the fields of item_list: the open items, in the order of the tracker.
func itemListOutputs(items []model.Item) map[string]any {
	listed := make([]listedItem, len(items))

	for i, item := range items {
		listed[i] = listedItem{
			ID: item.ID, Severity: string(item.Severity), Hold: item.Hold, Parent: item.Parent, Assignee: item.Assignee, Title: item.Title,
			Labels: append([]string{}, item.Labels...),
		}
	}

	return map[string]any{workflow.ItemsKeyword: listed}
}

// prListOutputs are the fields of pr_list: the open pull requests, in the order of the forge.
func prListOutputs(pulls []model.OpenPull) map[string]any {
	listed := make([]listedPull, len(pulls))

	for i, pull := range pulls {
		listed[i] = listedPull{
			Number: pull.Number, Branch: pull.Branch, Base: pull.Base, Author: pull.Author, Draft: pull.Draft, Title: pull.Title,
			Labels: append([]string{}, pull.Labels...), ReviewDecision: pull.ReviewDecision,
		}
	}

	return map[string]any{pullsField: listed}
}

// runReadOutputs are the fields of run_read: the last run of each item, in the order asked, and the free slots of the items queue.
func runReadOutputs(views []runView, free int) map[string]any {
	runs := make([]listedRun, len(views))

	for i, view := range views {
		runs[i] = listedRun{Item: view.Item, State: view.State, Value: view.Value, StopStep: view.StopStep, StopReason: view.StopReason}
	}

	return map[string]any{runsField: runs, "free": free}
}

// itemList reads the open items of the project that carry the label, or every open item when label is empty, in one recorded step.
// A replay returns the first answer. A read that fails ends the step.
func (s *script) itemList(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	var label, name string

	if err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{"label": &label, nameKeyword: &name}); err != nil {
		return nil, err
	}

	if s.e.deps.Queue == nil {
		return nil, errNoItemList
	}

	return s.durable(itemListKind, name, func(ctx context.Context, _ string) (stepResult, error) {
		items, err := s.e.itemsOf(ctx, s.in.Project, label)

		return stepResult{Outputs: itemListOutputs(items)}, err
	})
}

// prList reads the open pull requests of the project on the base, or every open pull request when base is empty, in one recorded step.
func (s *script) prList(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	var base, name string

	if err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{baseKeyword: &base, nameKeyword: &name}); err != nil {
		return nil, err
	}

	if s.e.deps.Queue == nil {
		return nil, errNoPullList
	}

	return s.durable(prListKind, name, func(ctx context.Context, _ string) (stepResult, error) {
		pulls, err := s.e.pullsOf(ctx, s.in.Project, base)

		return stepResult{Outputs: prListOutputs(pulls)}, err
	})
}

// runRead reads the free slots of the items queue, then the last run of each item, in one recorded step. The slots come first, so a slot that a run left a moment ago does not show as taken.
func (s *script) runRead(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	var (
		listed starlark.Value
		name   string
	)

	if err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{workflow.ItemsKeyword: &listed, nameKeyword: &name}); err != nil {
		return nil, err
	}

	items, err := workflow.Words(workflow.ItemsKeyword, listed)
	if err != nil {
		return nil, err
	}

	return s.durable(runReadKind, name, func(context.Context, string) (stepResult, error) {
		views, free, err := s.e.lastRuns(items)

		return stepResult{Outputs: runReadOutputs(views, free)}, err
	})
}

// holdAtStart stops a run that spawn started when its item is on hold in the tracker now. The tracker can change between the queue read and the start of the run.
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
