package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"

	"github.com/tobocop2/beehave/internal/model"
)

// spec is the schema of one constructor: its name and its argument names, in the order of the destinations that unpack takes.
// A name that ends in "?" is optional. The spec is the only place where the argument names are written: unpack reads them,
// and the refusal of a stray top-level name reads them to name the constructor.
type spec struct {
	name string
	args []string
}

// arguments lists the argument names without the optional mark.
func (s spec) arguments() []string {
	names := make([]string, len(s.args))
	for i, arg := range s.args {
		names[i] = strings.TrimSuffix(arg, "?")
	}

	return names
}

// unpack reads the keyword arguments of a call into the destinations, one for each argument of the spec.
// An argument that is not in the spec, or a required argument that is missing, is refused.
func (s spec) unpack(at syntax.Position, kwargs []starlark.Tuple, dests ...any) error {
	if len(dests) != len(s.args) {
		return fmt.Errorf("%s: the spec has %d arguments and the code passed %d destinations", s.name, len(s.args), len(dests))
	}

	var pairs []any
	for i, dest := range dests {
		pairs = append(pairs, s.args[i], dest)
	}

	if err := starlark.UnpackArgs(s.name, nil, kwargs, pairs...); err != nil {
		return refuseAt(at, "%w. Fix the call.", err)
	}

	return nil
}

// made is a block that a constructor built. Held is true once an argument of another block takes it.
type made struct {
	at    syntax.Position
	name  string
	owner string
	// usage is the argument that holds the block, as the refusal of a block that nothing holds writes it.
	usage string
	held  bool
}

// loader evaluates one config file. The file calls one constructor for each block, and a constructor that may appear once refuses a second call.
type loader struct {
	path   string
	specs  []spec
	called map[string]bool
	made   []*made
}

// build makes the block of one call. at is the position of the call in the file.
type build func(at syntax.Position, kwargs []starlark.Tuple) (starlark.Value, error)

func newLoader(path string, specs []spec) *loader {
	return &loader{path: path, specs: specs, called: map[string]bool{}}
}

// ownerOf returns the constructor that takes an argument of this name, or "" when none does.
func (l *loader) ownerOf(argument string) string {
	for _, s := range l.specs {
		if slices.Contains(s.arguments(), argument) {
			return s.name
		}
	}

	return ""
}

// constructor makes the builtin of a block that no other block holds. The call gives None.
func (l *loader) constructor(s spec, once bool, fn func(at syntax.Position, kwargs []starlark.Tuple) error) *starlark.Builtin {
	return l.builtin(s.name, once, func(at syntax.Position, kwargs []starlark.Tuple) (starlark.Value, error) {
		return starlark.None, fn(at, kwargs)
	})
}

// record makes the entry of a block. A block is held by the argument of its own name, or by the list argument of its name with an "s", such as schedules.
func (l *loader) record(at syntax.Position, name string) *made {
	if owner := l.ownerOf(name); owner != "" {
		return &made{at: at, name: name, owner: owner, usage: name + " = " + name + "(...)"}
	}

	return &made{at: at, name: name, owner: l.ownerOf(name + "s"), usage: name + "s = [" + name + "(...)]"}
}

// blocks makes the builtin of a block that another block holds as an argument. The call gives the block.
func blocks[T any](l *loader, s spec, once bool, fn func(at syntax.Position, kwargs []starlark.Tuple) (*T, error)) *starlark.Builtin {
	return l.builtin(s.name, once, func(at syntax.Position, kwargs []starlark.Tuple) (starlark.Value, error) {
		block, err := fn(at, kwargs)
		record := l.record(at, s.name)
		l.made = append(l.made, record)

		return &value[T]{block: block, made: record}, err
	})
}

// builtin makes the builtin of a block. It takes keyword arguments only. A block with once set refuses a second call.
// The arguments are checked before the second call is refused, so a block of the wrong kind in an argument gets its own refusal.
func (l *loader) builtin(name string, once bool, fn build) *starlark.Builtin {
	return starlark.NewBuiltin(name, func(thread *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		at := thread.CallFrame(1).Pos

		if len(args) > 0 {
			return nil, refuseAt(at, "`%s` takes keyword arguments only. Write %s(key = value).", name, name)
		}

		result, err := fn(at, kwargs)
		if err != nil {
			return nil, err
		}

		if once && l.called[name] {
			return nil, refuseAt(at, "block `%s` appears twice. Keep one.", name)
		}

		l.called[name] = true

		return result, nil
	})
}

// run evaluates the file with the constructors as its only builtins. It has no clock, no I/O, no output and no loop.
// After the file runs, it refuses a block that no argument holds and a top-level name that is not a function or a helper.
func (l *loader) run(builtins ...*starlark.Builtin) error {
	src, err := os.ReadFile(filepath.Clean(l.path))
	if err != nil {
		return refuse("%s: cannot read the file: %w. Check that the file exists and that you can read it.", l.path, err)
	}

	file, err := l.exec(src, builtins)
	if err != nil {
		return explain(err)
	}

	if err = l.checkHeld(); err != nil {
		return err
	}

	return l.checkNames(file)
}

// exec compiles and runs the text of the file, and returns its syntax tree.
func (l *loader) exec(src []byte, builtins []*starlark.Builtin) (*syntax.File, error) {
	predeclared := starlark.StringDict{}
	for _, builtin := range builtins {
		predeclared[builtin.Name()] = builtin
	}

	file, program, err := starlark.SourceProgramOptions(model.StarlarkFileOptions(), l.path, src, predeclared.Has)
	if err != nil {
		return nil, err
	}

	thread := &starlark.Thread{Name: l.path, Print: refusePrint}
	thread.SetMaxExecutionSteps(model.StarlarkMaxSteps)

	globals, err := program.Init(thread, predeclared)
	globals.Freeze()

	return file, err
}

// refusePrint stops the file at its next instruction: a config file does no output.
func refusePrint(thread *starlark.Thread, _ string) {
	thread.Cancel("`print` writes output, and a config file does no output. Remove the print call")
}

// checkHeld refuses the first block that no argument holds, such as a host(...) call that is not passed to project.
func (l *loader) checkHeld() error {
	for _, block := range l.made {
		if !block.held {
			return refuseAt(block.at, "`%s(...)` builds a block that no `%s` argument holds. Write %s(%s).", block.name, block.owner, block.owner, block.usage)
		}
	}

	return nil
}

// checkNames refuses the first top-level name that the file assigns, unless it starts with an underscore. A function that the file defines is allowed.
// A config value belongs inside a constructor call, so a name that the file keeps outside one is a typo or a value that the loader never reads.
func (l *loader) checkNames(file *syntax.File) error {
	for _, stmt := range file.Stmts {
		assign, ok := stmt.(*syntax.AssignStmt)
		if !ok {
			continue
		}

		for _, name := range targets(assign.LHS) {
			if !strings.HasPrefix(name.Name, "_") {
				return refuseAt(name.NamePos, "`%s` is a variable, and a config file takes none. %s", name.Name, l.advice(name.Name))
			}
		}
	}

	return nil
}

// advice tells where a config value goes. It names the constructor when the name matches one of its arguments.
func (l *loader) advice(name string) string {
	if owner := l.ownerOf(name); owner != "" {
		return fmt.Sprintf("Put a config value inside a constructor call: pass it to %s(...).", owner)
	}

	return "Put a config value inside a constructor call, or start the name with an underscore for a helper."
}

// targets lists the names that an assignment sets.
func targets(expr syntax.Expr) []*syntax.Ident {
	switch target := expr.(type) {
	case *syntax.Ident:
		return []*syntax.Ident{target}
	case *syntax.ParenExpr:
		return targets(target.X)
	case *syntax.TupleExpr:
		return flatten(target.List)
	case *syntax.ListExpr:
		return flatten(target.List)
	}

	return nil
}

func flatten(list []syntax.Expr) []*syntax.Ident {
	var names []*syntax.Ident

	for _, item := range list {
		names = append(names, targets(item)...)
	}

	return names
}

// explain keeps the refusal of a constructor and words any other error of the file with its position.
func explain(err error) error {
	var (
		refused refusal
		eval    *starlark.EvalError
	)

	switch {
	case errors.As(err, &refused):
		return refused
	case errors.As(err, &eval):
		return refuse("%s: %s. Fix the file.", model.InnermostPosition(eval.CallStack), strings.TrimRight(eval.Msg, "."))
	default:
		return refuse("%s. Fix the syntax of the file.", strings.TrimRight(err.Error(), "."))
	}
}

// paths is an argument that takes a list of strings.
type paths []string

// Unpack stores a list or a tuple of strings. It refuses any other value.
func (p *paths) Unpack(v starlark.Value) error {
	seq, ok := v.(starlark.Sequence)
	if !ok || v.Type() == "dict" || v.Type() == "set" {
		return fmt.Errorf("got %s, want a list of strings", v.Type())
	}

	*p = nil

	return p.collect(seq)
}

// collect stores the items of a sequence. It refuses an item that is not a string.
func (p *paths) collect(seq starlark.Sequence) error {
	iter := seq.Iterate()
	defer iter.Done()

	var item starlark.Value
	for iter.Next(&item) {
		value, isText := starlark.AsString(item)
		if !isText {
			return fmt.Errorf("the list holds a %s, want a list of strings", item.Type())
		}

		*p = append(*p, value)
	}

	return nil
}

// table is an argument that takes a dict of strings to strings.
type table map[string]string

// Unpack stores a dict of strings. It refuses any other value.
func (t *table) Unpack(v starlark.Value) error {
	dict, ok := v.(*starlark.Dict)
	if !ok {
		return fmt.Errorf("got %s, want a dict of strings", v.Type())
	}

	*t = table{}

	for _, item := range dict.Items() {
		key, keyOK := starlark.AsString(item[0])
		value, valueOK := starlark.AsString(item[1])

		if !keyOK || !valueOK {
			return fmt.Errorf("the entry %s holds a %s and a %s, want a dict of strings", item[0].String(), item[0].Type(), item[1].Type())
		}

		(*t)[key] = value
	}

	return nil
}

// text is a string argument that records whether the file gave it.
type text struct {
	Value string
	Set   bool
}

// Unpack stores a string. It refuses any other type.
func (t *text) Unpack(v starlark.Value) error {
	value, ok := starlark.AsString(v)
	if !ok {
		return fmt.Errorf("got %s, want string", v.Type())
	}

	t.Value, t.Set = value, true

	return nil
}

// number is an argument that takes an int or a float.
type number float64

// Unpack stores the number. It refuses any other type.
func (n *number) Unpack(v starlark.Value) error {
	value, ok := starlark.AsFloat(v)
	if !ok {
		return fmt.Errorf("got %s, want int or float", v.Type())
	}

	*n = number(value)

	return nil
}

// whole is an argument of an adapter block that takes a whole number and records whether the call gave it.
type whole struct {
	Value int
	Set   bool
}

// Unpack stores a whole number. It refuses any other type.
func (w *whole) Unpack(v starlark.Value) error {
	number, ok := v.(starlark.Int)
	if !ok {
		return fmt.Errorf("got %s, want a whole number", v.Type())
	}

	value, fits := number.Int64()
	if !fits {
		return fmt.Errorf("the number %s does not fit a whole number", number.String())
	}

	w.Value, w.Set = int(value), true

	return nil
}

// blockBuilt unpacks the arguments of one adapter block against the schema that the adapter declared, and returns the typed block.
func blockBuilt(at syntax.Position, declared Block, kwargs []starlark.Tuple) (*model.Block, error) {
	texts := make([]text, len(declared.Args))
	wholes := make([]whole, len(declared.Args))
	dests := make([]any, len(declared.Args))

	for i, arg := range declared.Args {
		dests[i] = &texts[i]
		if arg.Kind == WholeArg {
			dests[i] = &wholes[i]
		}
	}

	if err := declared.spec().unpack(at, kwargs, dests...); err != nil {
		return nil, err
	}

	return model.NewBlock(at.String(), blockEntries(declared, texts, wholes)), nil
}

// blockEntries returns the entries of the arguments that the call gave, in the order of the schema.
func blockEntries(declared Block, texts []text, wholes []whole) []model.BlockEntry {
	entries := make([]model.BlockEntry, 0, len(declared.Args))

	for i, arg := range declared.Args {
		switch {
		case arg.Kind == WholeArg && wholes[i].Set:
			entries = append(entries, model.WholeEntry(arg.Key, wholes[i].Value))
		case arg.Kind == TextArg && texts[i].Set:
			entries = append(entries, model.TextEntry(arg.Key, texts[i].Value))
		}
	}

	return entries
}

// value carries the block that a constructor made, so another block can hold it as an argument.
type value[T any] struct {
	block *T
	made  *made
}

func (v *value[T]) call() string        { return v.made.name }
func (*value[T]) String() string        { return "<block>" }
func (*value[T]) Type() string          { return "block" }
func (*value[T]) Freeze()               {}
func (*value[T]) Truth() starlark.Bool  { return starlark.True }
func (*value[T]) Hash() (uint32, error) { return 0, errors.New("a block is not hashable") }

// describe names a value for a refusal. A block reads as the call that made it.
func describe(v starlark.Value) string {
	if held, ok := v.(interface{ call() string }); ok {
		return held.call() + "(...)"
	}

	return v.Type()
}

// nested is an argument that holds the block of another constructor.
type nested[T any] struct {
	want  string
	block *T
}

// Unpack stores the block. It refuses any other value, and names the constructor that the argument takes.
func (n *nested[T]) Unpack(v starlark.Value) error {
	held, ok := v.(*value[T])
	if !ok {
		return fmt.Errorf("got %s, want %s(...)", describe(v), n.want)
	}

	n.block = held.block
	held.made.held = true

	return nil
}
