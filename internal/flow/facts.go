package flow

import (
	"context"
	"strings"

	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/workflow"
)

// repoKeyword is the keyword `repo` of a merge call and the field `repo` of the facts.
const repoKeyword = "repo"

// facts returns the facts of the project of the run as a struct. The step records them, so a replay returns the facts the run started with, not the ones in the file now.
func (s *script) facts(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	if err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{}); err != nil {
		return nil, err
	}

	fields, err := s.durableFields("facts", "", map[string]any{varsField: map[string]any{}}, func(context.Context, string) (stepResult, error) {
		return stepResult{Outputs: factsOutputs(s.Project())}, nil
	})
	if err != nil {
		return nil, err
	}

	// The settings that the project file wrote stay as written. Every call builds a fresh value, so the freeze is local to the caller.
	fields[varsField].Freeze()

	return starlarkstruct.FromStringDict(starlarkstruct.Default, fields), nil
}

// varsField is the field `vars` of the facts: the free settings of the project file.
const varsField = "vars"

// factsOutputs holds the fields of the facts struct. An empty fact is an empty list, an empty dict or empty text, never a missing field.
func factsOutputs(p model.Project) map[string]any {
	facts := map[string]any{
		repoKeyword: p.Repo, "source": p.Source, "forge": string(p.Forge), "tracker": string(p.Tracker), "base_branch": p.BaseBranch, "gate": gateText(p), "gate_steps": gateStepNames(p), "gate_program": append([]string{}, p.GateProgram...), "build_host": p.BuildHost,
		varsField: orEmptyVars(p.Vars), "runners": runnerFacts(p.RunnerSkillLoads),
	}

	return facts
}

// gateText is what a prompt shows as the gate of the project: the gate string, or the gate list as "name: command" joined by "; ", in order.
// A project has one of the two, so the text is never empty for a project that has a gate.
func gateText(p model.Project) string {
	if len(p.GateSteps) == 0 {
		return p.Gate
	}

	parts := make([]string, 0, len(p.GateSteps))

	for _, spec := range p.GateSteps {
		parts = append(parts, spec.Name+": "+spec.Command)
	}

	return strings.Join(parts, "; ")
}

// gateStepNames is the names of the gate list of the project, in order. A project with a gate string has none.
func gateStepNames(p model.Project) []string {
	names := make([]string, 0, len(p.GateSteps))

	for _, spec := range p.GateSteps {
		names = append(names, spec.Name)
	}

	return names
}

func orEmptyVars(items map[string]any) map[string]any {
	if items == nil {
		return map[string]any{}
	}

	return items
}

// runnerFacts is the field `runners` of the facts: one entry for each runner of the config, with the fact loads_skills. It is an empty dict when the config has no runner.
func runnerFacts(loads map[string]bool) map[string]any {
	runners := make(map[string]any, len(loads))

	for name, loading := range loads {
		runners[name] = map[string]any{"loads_skills": loading}
	}

	return runners
}
