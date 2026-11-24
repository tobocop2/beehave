package flow

import (
	"fmt"
	"slices"

	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/step"
	"github.com/tobocop2/beehave/internal/workflow"
)

// moduleName is the name of the module that holds the builtins that live in this package.
const moduleName = "flow"

// scriptCall is the Go function of a builtin of this package. It runs on the script of the run that made the call.
type scriptCall = func(s *script, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error)

// builtinDef is one builtin of this package, with how the engine guards the call.
type builtinDef struct {
	name string
	call scriptCall
	// repository is true for a builtin that reads git or a forge. exempt is the keyword that exempts a call that names its own program.
	repository bool
	exempt     string
}

// StepModule is the module of the builtins that live in this package. The composition root passes it to the engine in Options.Modules.
func StepModule() step.Module {
	defs := stepDefs()
	module := step.Module{Name: moduleName, Kinds: []string{loginStepName}}

	for _, def := range defs {
		params, _ := workflow.Signature(def.name)
		results, _ := workflow.ResultFields(def.name)
		module.Builtins = append(module.Builtins, step.Builtin{
			Name: def.name, Params: params, Results: results, NeedsRepository: def.repository, ExemptKeyword: def.exempt, Call: scripted(def.call),
		})

		if recordsCountedStep(def.name) {
			module.Kinds = append(module.Kinds, def.name)
		}
	}

	return module
}

// recordsCountedStep is false for the builtins that record no counted step. merge names its steps batch:<name>.<kind>#<item>, and sandbox, struct,
// paths_outside and closing_references record no step: the last two match text, so a replay computes the same answer from the same input.
// patched records the marker DBOS.patch-<name>, a step of DBOS and no counted step of a module.
func recordsCountedStep(builtin string) bool {
	return !slices.Contains([]string{mergeKey, handOffKeyword, recordedSandbox, "struct", "paths_outside", "closing_references", "patched"}, builtin)
}

// stepDefs lists the builtins of this package. The names are the builtins of workflow.Builtins less json, which is the module of the Starlark library.
func stepDefs() []builtinDef {
	return slices.Concat(repositoryDefs(), itemDefs(), eventDefs(), runDefs(), inboxDefs(), swarmDefs(), roundDefs(), listDefs())
}

// repositoryDefs lists the builtins that read git or a forge. The engine refuses a call of one of them in a project with no repository.
func repositoryDefs() []builtinDef {
	return []builtinDef{
		{name: "gate", call: (*script).gate, repository: true, exempt: "run"},
		{name: "merge", call: (*script).batchMerge, repository: true},
		{name: handOffKeyword, call: (*script).handOffCall, repository: true},
		{name: "pr_comment", call: (*script).prComment, repository: true},
		{name: prHistoryKind, call: (*script).prHistory, repository: true},
		{name: "pr_open", call: (*script).prOpen, repository: true},
		{name: prReadKind, call: (*script).prRead, repository: true},
		{name: prStateKind, call: (*script).prState, repository: true},
		{name: issueAssigneesKind, call: (*script).issueAssignees, repository: true},
		{name: issueStateKind, call: (*script).issueState, repository: true},
		{name: baseTipKind, call: (*script).baseTip, repository: true},
		{name: prClosingKind, call: (*script).prClosing, repository: true},
		{name: "preflight", call: (*script).preflight, repository: true},
		{name: "push", call: (*script).push, repository: true},
	}
}

// itemDefs lists the builtins that read or write the tracker.
func itemDefs() []builtinDef {
	return []builtinDef{
		{name: "item_comment", call: (*script).itemComment},
		{name: "item_create", call: (*script).itemCreate},
		{name: "item_read", call: (*script).itemRead},
		{name: "item_status", call: (*script).itemStatus},
		{name: itemStopKind, call: (*script).itemStop},
	}
}

// eventDefs lists the builtins that publish a value of this run and read the values and the agent streams of the child runs.
func eventDefs() []builtinDef {
	return []builtinDef{
		{name: progressKind, call: (*script).progress},
		{name: publishKind, call: (*script).publish},
		{name: streamKind, call: (*script).stream},
	}
}

// runDefs lists the builtins that need no repository and no tracker.
func runDefs() []builtinDef {
	return []builtinDef{
		{name: "agent", call: (*script).agent},
		{name: "closing_references", call: closingReferences},
		{name: "command", call: (*script).command},
		{name: "earlier_calls", call: (*script).earlierCalls},
		{name: earlierReadsKind, call: (*script).earlierReads},
		{name: "env", call: (*script).env},
		{name: "facts", call: (*script).facts},
		{name: "host_teardown", call: (*script).hostTeardown},
		{name: "host_up", call: (*script).hostUp},
		{name: "now", call: (*script).now},
		{name: "paths_outside", call: pathsOutside},
		{name: "patched", call: (*script).patched},
		{name: "sandbox", call: (*script).sandbox},
		{name: "sleep", call: (*script).sleep},
		{name: "struct", call: structCall},
		{name: "wait", call: (*script).wait},
		{name: "write", call: (*script).writeOutside},
	}
}

// swarmDefs lists the builtins that start child runs, wait for them and cancel them.
func swarmDefs() []builtinDef {
	return []builtinDef{
		{name: cancelKind, call: (*script).cancel},
		{name: firstOfKind, call: (*script).firstOf},
		{name: gatherKind, call: (*script).gather},
		{name: "spawn", call: (*script).spawn},
	}
}

// structCall is the builtin struct of the Starlark library. It takes any keyword and records no step.
func structCall(_ *script, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	return starlarkstruct.Make(nil, b, args, kwargs)
}

// scripted turns the function of a builtin of this package into the call of the step contract. Only the engine of this package makes a call,
// and it always gives the script of the run, so another kind of call is a fault of the build.
func scripted(call scriptCall) func(step.Call, *starlark.Builtin, starlark.Tuple, []starlark.Tuple) (starlark.Value, error) {
	return func(c step.Call, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		s, ok := c.(*script)
		if !ok {
			return nil, fmt.Errorf("%s runs only in a run of the engine that holds its module", b.Name())
		}

		return call(s, b, args, kwargs)
	}
}

// Project is the project of the run as the config holds it.
func (s *script) Project() model.Project {
	return s.e.facts(s.in.Project)
}

// Item is the id of the item of the run.
func (s *script) Item() string { return s.in.Item }
