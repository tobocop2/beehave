package flow

import (
	"context"
	"fmt"

	"go.starlark.net/starlark"

	"github.com/tobocop2/beehest/internal/workflow"
)

// issueStateKind is the step kind and the builtin name of issue_state, and the two states that an issue has.
const (
	issueStateKind = "issue_state"
	issueOpen      = "OPEN"
	issueClosed    = "CLOSED"
)

var errNoIssueStateReader = &absentError{message: "this build cannot read the state of an issue. Use a build that sets a queue reader", step: "read the state of an issue"}

// issueState reads the state of the issue with the number: OPEN or CLOSED. The read is a recorded step, so a replay returns the state of the first read.
// A read that fails, or an answer that is neither state, gives no state and the cause in error, so the check after a batch makes it a finding and goes on.
// found is false, with no state, for a project whose tracker has no issue numbers: no read ran, as in the check after a batch.
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

// readIssueState reads the state of the issue. An answer that is neither OPEN nor CLOSED is a failed read. A read that the end of the run stops ends the step: it is no answer of the forge.
func (s *script) readIssueState(ctx context.Context, number int) (stepResult, error) {
	state, found, err := s.e.deps.Queue.IssueState(ctx, s.in.Project, number)

	switch {
	case ctx.Err() != nil:
		return stepResult{}, fmt.Errorf("read the state of issue %d: %w", number, context.Cause(ctx))
	case err != nil:
		return stepResult{Outputs: issueStateOutputs("", false, s.e.redact(err.Error()))}, nil
	case !found:
		return stepResult{Outputs: issueStateOutputs("", false, "")}, nil
	case state != issueOpen && state != issueClosed:
		return stepResult{Outputs: issueStateOutputs("", false, fmt.Sprintf("the forge answered %q, not %s or %s", state, issueOpen, issueClosed))}, nil
	}

	return stepResult{Outputs: issueStateOutputs(state, true, "")}, nil
}
