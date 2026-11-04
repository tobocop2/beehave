package flow

import (
	"context"
	"strings"

	"go.starlark.net/starlark"

	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/workflow"
)

// repoKeyword is the keyword `repo` of a merge call and the field `repo` of the facts.
const repoKeyword = "repo"

// facts returns the facts of the project of the run as a struct. The step records them, so a replay returns the facts the run started with, not the ones in the file now.
func (s *script) facts(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	if err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{}); err != nil {
		return nil, err
	}

	return s.durable("facts", "", func(context.Context, string) (stepResult, error) {
		var project model.Project

		if s.e.deps.Facts != nil {
			project = s.e.deps.Facts(s.in.Project)
		}

		return stepResult{Outputs: factsOutputs(project)}, nil
	})
}

// factsOutputs holds the fields of the facts struct. An empty fact is an empty list, an empty dict or empty text, never a missing field, so a prompt can range over it.
func factsOutputs(p model.Project) map[string]any {
	return map[string]any{
		repoKeyword: p.Repo, "source": p.Source, "forge": string(p.Forge), "tracker": string(p.Tracker), "base_branch": p.BaseBranch, "gate": gateText(p), "gate_steps": gateStepNames(p), "build_host": p.BuildHost,
		"people": orEmptyDict(p.People), "epics": orEmptyDict(p.Epics),
		"gate_files": orEmptyList(p.GateFiles), "conventions": orEmptyList(p.Conventions), "brief_rules": orEmptyList(p.BriefRules), "stacked_rules": orEmptyList(p.StackedRules),
		"limits": orEmptyList(p.Limits), "traps": orEmptyList(p.Traps), "token_rules": orEmptyList(p.TokenRules), "changelog": p.Changelog,
		"board_number": p.Board.Number, "board_owner": p.Board.Owner, "board_started": p.Board.Started, "board_merged": p.Board.Merged,
	}
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

func orEmptyList(items []string) []string {
	if items == nil {
		return []string{}
	}

	return items
}

func orEmptyDict(items map[string]string) map[string]string {
	if items == nil {
		return map[string]string{}
	}

	return items
}
