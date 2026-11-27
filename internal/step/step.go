// Package step is the contract between the engine and the modules that hold the builtin steps of a workflow file.
// A module is a plain value that the composition root builds and passes to the engine. The engine builds its builtin map
// and its table of step kinds from the list of modules, and refuses a list in which two modules name the same builtin or the same step kind.
package step

import (
	"errors"
	"fmt"
	"maps"
	"slices"

	"go.starlark.net/starlark"

	"github.com/tobocop2/beehest/internal/model"
	"github.com/tobocop2/beehest/internal/workflow"
)

// Module is a named set of builtins and the step kinds that it records.
type Module struct {
	Name string
	// Version is the version of the recorded steps of the module. A change to a builtin of the module that changes its recorded steps bumps it.
	// It is 0 for a module that stands for several modules whose versions the host package keeps in a table of its own: the module of package flow.
	Version  int
	Builtins []Builtin
	// Kinds are the counted step kinds that the module records: the start of the name `<kind>:<label>#<n>` of each step.
	// A builtin that records no counted step, or records steps of another name, is not listed.
	Kinds []string
}

// Builtin is one function of a workflow file.
type Builtin struct {
	Name string
	// Params is the signature that the check and the run read. It is nil for a builtin that takes no fixed keywords, and an empty list for one that takes none.
	Params []workflow.Param
	// Results lists the fields of the struct that the builtin always returns. It is nil for a builtin whose result has no fixed fields.
	Results []string
	// NeedsRepository is true for a builtin that reads git or a forge. The engine refuses a call of it in a project with no repository, before the call reads its arguments.
	NeedsRepository bool
	// ExemptKeyword is the keyword that exempts a call of such a builtin when it holds a non-empty program, because a call that names its own program reads no git.
	// It is "" when no keyword exempts the call.
	ExemptKeyword string
	// Call runs one call of the builtin. b is the Starlark function that the engine made for the name, and c is the call that the engine gives it.
	Call func(c Call, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error)
}

// Call is what the engine gives a builtin call. A module reads the run through it and never reads the engine.
// The engine calls a builtin on the goroutine of the run, so a builtin never runs beside another builtin of its run.
type Call interface {
	// Project is the project of the run.
	Project() model.Project
	// Item is the id of the item that the run works.
	Item() string
}

// Registry is what the engine builds from a list of modules.
type Registry struct {
	builtins map[string]Builtin
	kinds    []string
}

// NewRegistry builds the registry of the modules. It fails when two modules, or one module twice, name the same builtin or the same step kind.
func NewRegistry(modules []Module) (Registry, error) {
	b := builder{registry: Registry{builtins: map[string]Builtin{}}, builtinOwner: map[string]string{}, kindOwner: map[string]string{}}

	for _, module := range modules {
		b.addBuiltins(module)
		b.addKinds(module)
	}

	if err := errors.Join(b.problems...); err != nil {
		return Registry{}, err
	}

	slices.Sort(b.registry.kinds)

	return b.registry, nil
}

// builder collects the registry and the clashes of a list of modules. The owner maps name the module that first took a builtin or a kind.
type builder struct {
	registry     Registry
	builtinOwner map[string]string
	kindOwner    map[string]string
	problems     []error
}

func (b *builder) addBuiltins(module Module) {
	for _, builtin := range module.Builtins {
		if owner, taken := b.builtinOwner[builtin.Name]; taken {
			b.problems = append(b.problems, fmt.Errorf("module %q and module %q both define the builtin %q. Rename one or remove one", owner, module.Name, builtin.Name))

			continue
		}

		b.builtinOwner[builtin.Name] = module.Name
		b.registry.builtins[builtin.Name] = builtin
	}
}

func (b *builder) addKinds(module Module) {
	for _, kind := range module.Kinds {
		if owner, taken := b.kindOwner[kind]; taken {
			b.problems = append(b.problems, fmt.Errorf("module %q and module %q both record the step kind %q. Rename one or remove one", owner, module.Name, kind))

			continue
		}

		b.kindOwner[kind] = module.Name
		b.registry.kinds = append(b.registry.kinds, kind)
	}
}

// Builtin returns the builtin of a name, and false when no module defines it.
func (r Registry) Builtin(name string) (Builtin, bool) {
	builtin, ok := r.builtins[name]

	return builtin, ok
}

// Names lists the names of the builtins, sorted.
func (r Registry) Names() []string {
	return slices.Sorted(maps.Keys(r.builtins))
}

// Kinds lists the counted step kinds of all modules, sorted.
func (r Registry) Kinds() []string {
	return slices.Clone(r.kinds)
}
