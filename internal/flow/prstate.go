package flow

import (
	"context"
	"fmt"

	"go.starlark.net/starlark"

	"github.com/tobocop2/beehest/internal/workflow"
)

// prStateKind is the step kind and the builtin name of pr_state, and stateField is the field of a result that holds the state of a pull request or a merge.
const (
	prStateKind = "pr_state"
	stateField  = "state"
)

var errNoStateReader = &absentError{message: "this build cannot read the state of a pull request. Use a build that sets a queue reader", step: "read the state of a pull request"}

// prState reads the state of the pull request with the number pr, open or not: OPEN, CLOSED or MERGED. It reads that number and looks no pull request up by a branch.
// The read is a recorded step, so a replay returns the state of the first read.
func (s *script) prState(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	pr, label, err := s.prStateArgs(b, args, kwargs)
	if err != nil {
		return nil, err
	}

	return s.durable(prStateKind, label, func(ctx context.Context, _ string) (stepResult, error) {
		state, err := s.e.deps.Queue.PullState(ctx, s.in.Project, pr)
		if err != nil {
			return stepResult{}, fmt.Errorf("read the state of pull request %d: %w", pr, err)
		}

		return stepResult{Outputs: prStateOutputs(state)}, nil
	})
}

// prStateArgs reads the number and the step name of a pr_state call. A number below 1 and a build with no queue reader are refused before any read.
func (s *script) prStateArgs(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (int, string, error) {
	var (
		pr    int
		label string
	)

	if err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{"pr": &pr, nameKeyword: &label}); err != nil {
		return 0, "", err
	}

	if pr < 1 {
		return 0, "", fmt.Errorf("`pr` is %d. Pass the number of a pull request, such as the pr of the preflight that reviewed it", pr)
	}

	if s.e.deps.Queue == nil {
		return 0, "", errNoStateReader
	}

	return pr, label, nil
}
