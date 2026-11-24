package flow

import (
	"context"
	"fmt"

	"go.starlark.net/starlark"

	"github.com/tobocop2/beehave/internal/workflow"
)

// issueStateKind is the step kind and the builtin name of issue_state, and the two states that an issue has.
const (
	issueStateKind = "issue_state"
	issueOpen      = "OPEN"
	issueClosed    = "CLOSED"
)

var errNoIssueStateReader = &absentError{message: "this build cannot read the state of an issue. Use a build that sets a queue reader", step: "read the state of an issue"}

// issueState reads the state of the issue with the number: OPEN or CLOSED. The read is a recorded step, so a replay returns the state of the first read.
// A read that fails ends the step, and so does an answer that is neither state: no caller reads a guess from it.
func (s *script) issueState(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	var (
		number int
		label  string
	)

	if err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{"number": &number, nameKeyword: &label}); err != nil {
		return nil, err
	}

	if number < 1 {
		return nil, fmt.Errorf("`number` is %d. Pass the number of an issue", number)
	}

	if s.e.deps.Queue == nil {
		return nil, errNoIssueStateReader
	}

	return s.durable(issueStateKind, label, func(ctx context.Context, _ string) (stepResult, error) { return s.readIssueState(ctx, number) })
}

// readIssueState reads the state of the issue. An answer that is neither OPEN nor CLOSED is a failed read.
func (s *script) readIssueState(ctx context.Context, number int) (stepResult, error) {
	state, err := s.e.deps.Queue.IssueState(ctx, s.in.Project, number)
	if err != nil {
		return stepResult{}, fmt.Errorf("read the state of issue %d: %w", number, err)
	}

	if state != issueOpen && state != issueClosed {
		return stepResult{}, fmt.Errorf("read the state of issue %d: the forge answered %q, not %s or %s", number, state, issueOpen, issueClosed)
	}

	return stepResult{Outputs: issueStateOutputs(state)}, nil
}
