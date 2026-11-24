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

// prRead reads the pull request of an item, found as the preflight finds it: the open one on its branch, then an open one that names the item, then the merged one, then the newest closed one.
// An item with no pull request in any state gives found false. The read is a recorded step, so a replay returns the answer of the first read, and a failed read ends the run.
func (s *script) prRead(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	var id, label string

	if err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{idKeyword: &id, nameKeyword: &label}); err != nil {
		return nil, err
	}

	if s.e.deps.Preflight == nil {
		return nil, errNoPreflight
	}

	if id == "" {
		id = s.in.Item
	}

	return s.durable(prReadKind, label, func(ctx context.Context, _ string) (stepResult, error) {
		facts, err := s.e.deps.Preflight.ReadPull(ctx, s.in.Project, id)
		if err != nil {
			return stepResult{}, err
		}

		return stepResult{Outputs: prReadOutputs(facts)}, nil
	})
}

// issueAssignees reads the logins that hold an issue. An empty repo is the repository of the project, and any other repo is read on that repository.
// The read is a recorded step. A failed read ends the run: no caller reads an empty list from it.
func (s *script) issueAssignees(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	var (
		repo, label string
		number      int
	)

	if err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{"repo": &repo, "number": &number, nameKeyword: &label}); err != nil {
		return nil, err
	}

	if number < 1 {
		return nil, fmt.Errorf("`number` is %d. Pass the number of an issue", number)
	}

	if s.e.deps.Preflight == nil {
		return nil, errNoPreflight
	}

	return s.durable(issueAssigneesKind, label, func(ctx context.Context, _ string) (stepResult, error) {
		logins, err := s.e.deps.Preflight.IssueAssignees(ctx, s.in.Project, repo, number)
		if err != nil {
			return stepResult{}, fmt.Errorf("cannot read the assignees of %s: %w", model.IssueLink{Repo: repo, Number: number}, err)
		}

		return stepResult{Outputs: issueAssigneesOutputs(logins)}, nil
	})
}

// itemStop records the stop of an item, as a command that stopped the item does: the step, the exit code and the reason show in the result of the run.
// A stop of any item but the item of the run is recorded as a step and changes nothing in the result of the run.
func (s *script) itemStop(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	var (
		step, reason, id, label string
		code                    int
	)

	if err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{"step": &step, "code": &code, "reason": &reason, idKeyword: &id, nameKeyword: &label}); err != nil {
		return nil, err
	}

	if code < 1 {
		return nil, fmt.Errorf("`code` is %d. Pass the exit code of the stop, a number from 1", code)
	}

	own := id == "" || id == s.in.Item

	value, err := s.durable(itemStopKind, label, func(context.Context, string) (stepResult, error) {
		return stepResult{Outputs: itemStopOutputs(own)}, nil
	})
	if err != nil {
		return nil, err
	}

	if own {
		s.stopped = &Stopped{Step: step, RC: code, Output: reason}
	}

	return value, nil
}
