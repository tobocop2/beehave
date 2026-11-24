package flow

import (
	"context"
	"maps"

	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	gatepkg "github.com/tobocop2/beehave/internal/gate"
)

// requiredSteps are the names of the steps that the gate list of the project gives now. A project with a gate string has no list, and a gate records none.
// The gate calls it and records the names in the required field of its result.
func (e *Engine) requiredSteps(project string) []string {
	facts := e.facts(project)
	if len(facts.GateSteps) == 0 {
		return nil
	}

	return gatepkg.StepNames(facts)
}

// recordGate records the outputs of a gate call as its own step, so a replay reads them and runs no gate again.
// The record holds the name of its own step in the field step, so a later call can name the gate that a result came from.
func (s *script) recordGate(label string, outputs map[string]any) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	fields, err := s.durableFields("gate", label, nil, func(_ context.Context, name string) (stepResult, error) {
		recorded := maps.Clone(outputs)
		recorded[stepField] = name

		return stepResult{Outputs: recorded}, nil
	})
	if err != nil {
		return nil, err
	}

	return starlarkstruct.FromStringDict(starlarkstruct.Default, fields), nil
}
