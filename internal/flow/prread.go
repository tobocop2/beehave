package flow

import (
	"context"
	"fmt"

	"go.starlark.net/starlark"

	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/workflow"
)

// The step kinds and builtin names of the reads that the claim stops of the workflow library use.
const (
	prReadKind         = "pr_read"
	issueAssigneesKind = "issue_assignees"
	itemStopKind       = "item_stop"
	assigneesField     = "assignees"
	idKeyword          = "id"
)

// unpackReads unpacks the keywords of a read builtin and refuses a build that has no reader.
func (s *script) unpackReads(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple, into map[string]any) error {
	if err := workflow.Unpack(b.Name(), args, kwargs, into); err != nil {
		return err
	}

	if s.e.deps.Preflight == nil {
		return errNoPreflight
	}

	return nil
}

// prRead reads the pull request of an item, found as the preflight finds it: the open one on its branch, then an open one that names the item, then the merged one, then the newest closed one.
// An item with no pull request in any state gives found false. The read is a recorded step, so a replay returns the answer of the first read, and a failed read ends the run.
// A read of the item of the run applies the rule of the start again to the pull request of the facts, so a foreign pull request that opened after the start ends
// a run that may not hold it. A read of another item reads no author.
func (s *script) prRead(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	var id, label string

	if err := s.unpackReads(b, args, kwargs, map[string]any{idKeyword: &id, nameKeyword: &label}); err != nil {
		return nil, err
	}

	item, err := s.itemOf(id)
	if err != nil {
		return nil, err
	}

	return s.durable(prReadKind, label, s.readPull(item))
}

// readPull is the recorded read of one pr_read call, on the pull request of the item.
func (s *script) readPull(item string) func(context.Context, string) (stepResult, error) {
	return func(ctx context.Context, _ string) (stepResult, error) {
		own := s.owns(item)

		facts, err := s.e.deps.Preflight.ReadPull(ctx, s.in.Project, item, own)
		if err != nil {
			return stepResult{}, err
		}

		if err := s.admitOwn(ctx, own, facts.Authored()); err != nil {
			return stepResult{}, err
		}

		return stepResult{Outputs: prReadOutputs(facts)}, nil
	}
}

// issueAssignees reads the logins that hold an issue. An empty repo is the repository of the project, and any other repo is read on that repository.
// The read is a recorded step. A failed read ends the run: no caller reads an empty list from it.
func (s *script) issueAssignees(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	var (
		repo, label string
		number      int
	)

	if err := s.unpackReads(b, args, kwargs, map[string]any{"repo": &repo, "number": &number, nameKeyword: &label}); err != nil {
		return nil, err
	}

	if number < 1 {
		return nil, fmt.Errorf("`number` is %d. Pass the number of an issue", number)
	}

	return s.durable(issueAssigneesKind, label, func(ctx context.Context, _ string) (stepResult, error) {
		return s.readAssignees(ctx, model.IssueLink{Repo: repo, Number: number})
	})
}

// readAssignees is the step of issueAssignees. A failed read names the issue.
func (s *script) readAssignees(ctx context.Context, issue model.IssueLink) (stepResult, error) {
	logins, err := s.e.deps.Preflight.IssueAssignees(ctx, s.in.Project, issue.Repo, issue.Number)
	if err != nil {
		return stepResult{}, fmt.Errorf("cannot read the assignees of %s: %w", issue, err)
	}

	return stepResult{Outputs: issueAssigneesOutputs(logins)}, nil
}

// unpackStop unpacks the keywords of item_stop: the stop, the item it names (empty for the item of the run) and the step label.
func unpackStop(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (stop Stopped, id, label string, err error) {
	err = workflow.Unpack(b.Name(), args, kwargs, map[string]any{stepField: &stop.Step, codeField: &stop.RC, "reason": &stop.Output, idKeyword: &id, nameKeyword: &label})
	if err == nil && stop.RC < 1 {
		err = fmt.Errorf("`code` is %d. Pass the exit code of the stop, a number from 1", stop.RC)
	}

	return stop, id, label, err
}

// itemStop records the stop of an item, as a command that stopped the item does: the step, the exit code and the reason show in the result of the run.
// A stop of any item but the item of the run is recorded as a step and changes nothing in the result of the run.
func (s *script) itemStop(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	stop, id, label, err := unpackStop(b, args, kwargs)
	if err != nil {
		return nil, err
	}

	own := id == "" || id == s.in.Item

	value, err := s.durable(itemStopKind, label, func(context.Context, string) (stepResult, error) {
		return stepResult{Outputs: itemStopOutputs(own)}, nil
	})
	if own && err == nil {
		s.stopped = &stop
	}

	return value, err
}
