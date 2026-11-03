package flow

import (
	"context"
	"errors"

	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"
)

// errStepsNotFromGate refuses a merge call whose `steps` no gate call of the run gave at the head of the merge.
// The text names the limit of a child run: the gates of its parent do not count, so the gate call and the merge call must be in the same run.
var errStepsNotFromGate = errors.New("`steps` is not the steps of a gate call in this run at the head of this merge. " +
	"Pass `steps = <gate>.steps` from a gate call with the same `head`, and do not write or edit the list. " +
	"A run counts only its own gate calls: a child run cannot merge with steps that its parent gated, so call the gate in the run that merges")

// gatedCall is one gate call of a run: the head it gated and the steps it gave, as the Starlark values of the record.
type gatedCall struct {
	head  starlark.Value
	steps starlark.Value
}

// holdMerge runs before a merge. The steps must be the steps of a gate of this run at the head of the merge, and the scope must hold.
func (s *script) holdMerge(m mergeArgs) error {
	if err := s.checkGateSteps(m.head, m.steps); err != nil {
		return err
	}

	return s.holdScope(m.label)
}

// recordGate records the outputs of a gate call as its own step, so a replay reads them and runs no gate again, and remembers the head and steps of the call.
// A replay makes the same gate calls in the same order, so it fills the same list.
func (s *script) recordGate(label string, outputs map[string]any) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	fields, err := s.durableFields("gate", label, nil, func(context.Context, string) (stepResult, error) {
		return stepResult{Outputs: outputs}, nil
	})
	if err != nil {
		return nil, err
	}

	// gateOutputs writes the head and the steps of every gate record, so the fields hold both.
	s.gated = append(s.gated, gatedCall{head: fields["head"], steps: fields["steps"]})

	return starlarkstruct.FromStringDict(starlarkstruct.Default, fields), nil
}

// checkGateSteps refuses a `steps` argument that is not the steps of a gate call of this run at the head of the merge. The steps compare by value, the head by text.
func (s *script) checkGateSteps(head string, steps starlark.Value) error {
	for _, gated := range s.gated {
		if equal, err := starlark.Equal(gated.steps, steps); err == nil && equal && gated.head == starlark.Value(starlark.String(head)) {
			return nil
		}
	}

	return errStepsNotFromGate
}
