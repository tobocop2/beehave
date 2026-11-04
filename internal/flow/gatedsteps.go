package flow

import (
	"context"
	"errors"

	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	gatepkg "github.com/tobocop2/obee/internal/gate"
	"github.com/tobocop2/obee/internal/workflow"
)

// errStepsNotFromGate refuses a merge call whose `steps` no gate call of the run gave at the head of the merge.
// The text names the limit of a child run: the gates of its parent do not count, so the gate call and the merge call must be in the same run.
var errStepsNotFromGate = errors.New("`steps` is not the steps of a gate call in this run at the head of this merge. " +
	"Pass `steps = <gate>.steps` from a gate call with the same `head`, and do not write or edit the list. " +
	"A run counts only its own gate calls: a child run cannot merge with steps that its parent gated, so call the gate in the run that merges")

// gatedCall is one gate call of a run: the head it gated, the steps it gave and the names its gate list had, as the Starlark values of the record.
type gatedCall struct {
	head     starlark.Value
	steps    starlark.Value
	required starlark.Value
}

// holdMerge runs before a merge. The steps must be the steps of a gate of this run at the head of the merge, they must hold every name
// that the gate list had when that gate ran, and the scope must hold.
func (s *script) holdMerge(m mergeArgs) error {
	gated, err := s.checkGateSteps(m.head, m.steps)
	if err != nil {
		return err
	}

	if err := workflow.RequiredSteps(m.steps, recordedNames(gated.required)); err != nil {
		return err
	}

	return s.holdScope(m.label)
}

// recordedNames reads the required names of a gate record. A record without the field, or with anything else in it, asks for none.
func recordedNames(v starlark.Value) []string {
	list, ok := v.(*starlark.List)
	if !ok {
		return nil
	}

	names := make([]string, 0, list.Len())

	for i := range list.Len() {
		if name, isText := starlark.AsString(list.Index(i)); isText {
			names = append(names, name)
		}
	}

	return names
}

// requiredSteps are the names of the steps that the gate list of the project gives now. A project with a gate string has no list, and a gate records none.
// The gate calls it and records the names, so the merge reads the record and never the config.
func (e *Engine) requiredSteps(project string) []string {
	if e.deps.Facts == nil {
		return nil
	}

	facts := e.deps.Facts(project)
	if len(facts.GateSteps) == 0 {
		return nil
	}

	return gatepkg.StepNames(facts)
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

	// gateOutputs writes the head, the steps and the required names of every gate record, so the fields hold all three.
	s.gated = append(s.gated, gatedCall{head: fields["head"], steps: fields["steps"], required: fields["required"]})

	return starlarkstruct.FromStringDict(starlarkstruct.Default, fields), nil
}

// checkGateSteps refuses a `steps` argument that is not the steps of a gate call of this run at the head of the merge, and returns that call.
// The steps compare by value, the head by text.
func (s *script) checkGateSteps(head string, steps starlark.Value) (gatedCall, error) {
	for _, gated := range s.gated {
		if equal, err := starlark.Equal(gated.steps, steps); err == nil && equal && gated.head == starlark.Value(starlark.String(head)) {
			return gated, nil
		}
	}

	return gatedCall{}, errStepsNotFromGate
}
