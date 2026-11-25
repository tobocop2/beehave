package flow

import (
	"context"
	"errors"

	"go.starlark.net/starlark"

	"github.com/tobocop2/beehave/internal/workflow"
)

// pushKind is the step kind and the builtin name of push, and branchKeyword names the branch it pushes to.
const (
	pushKind      = "push"
	branchKeyword = "branch"
)

// push pushes a head of the clone of the project to a branch, from the engine process. The agent of a sandboxed step has no credential, so the engine pushes for it.
// The run must hold a trusted claim, so a run that lost its item pushes nothing.
// A push to a branch already at head lands nothing and is no error, so a replay after a crash between the push and the step record passes.
func (s *script) push(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	var branch, head, expect, label string

	err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{branchKeyword: &branch, headKeyword: &head, "expect": &expect, nameKeyword: &label})
	if err == nil {
		err = errors.Join(workflow.NotEmpty(branchKeyword, branch), workflow.NotEmpty(headKeyword, head))
	}

	if err != nil {
		return nil, err
	}

	return s.durable(pushKind, label, func(ctx context.Context, _ string) (stepResult, error) {
		err := s.forkMayPush(branch)
		if err == nil {
			err = s.writes().Push(ctx, s.in.Project, s.in.Item, branch, head, expect)
		}

		return stepResult{Outputs: pushOutputs(branch, head)}, err
	})
}
