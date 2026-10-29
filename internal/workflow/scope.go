package workflow

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"

	"github.com/tobocop2/obee/internal/hclfile"
)

// Output is one value that a step records, with its type.
type Output struct {
	Name string
	Type cty.Type
}

// KindOutputs returns the outputs that every step of kind records, sorted by name.
// This is the only list of outputs. The checker and the scope read it, and so does every executor.
func KindOutputs(kind Kind) []Output {
	switch kind {
	case KindAgent:
		return []Output{{"head_sha", cty.String}, {"session", cty.String}, {"text", cty.String}, {"transcript", cty.String}, {"violations", cty.String}}
	case KindCommand:
		return []Output{{"rc", cty.Number}, {"stdout", cty.String}}
	case KindGate:
		return []Output{{"head", cty.String}, {"passed", cty.Bool}, {"report", cty.String}}
	case KindMerge:
		return []Output{{"reason", cty.String}, {"state", cty.String}}
	case KindWait:
		return []Output{{"answer", cty.String}}
	case KindLoop:
	}

	return []Output{{"exhausted", cty.Bool}}
}

// StepOutputs returns the outputs that a step records: those of its kind and, for an agent, the names in its `outputs` list.
// An agent whose `outputs` is not a list of plain strings declares none beyond its kind.
func StepOutputs(step Step) []Output {
	all, _ := stepOutputs(step)

	return all
}

// stepOutputs returns the outputs of a step: those of its kind and, for an agent, the names in its
// `outputs` argument, which are strings. The second result is true when the agent declares its
// outputs with an expression that is not a list of strings, so any name may be valid.
func stepOutputs(step Step) ([]Output, bool) {
	all := KindOutputs(step.Kind())

	agent, ok := step.(*Agent)
	if !ok {
		return all, false
	}

	declared, open := declaredOutputs(agent.Outputs)
	if open {
		return all, true
	}

	for _, name := range declared {
		if !slices.ContainsFunc(all, func(o Output) bool { return o.Name == name }) {
			all = append(all, Output{name, cty.String})
		}
	}

	return all, false
}

// Scope holds what an expression may read: the item, the environment and the outputs of the steps that ran.
// It uses no DBOS state. The caller rebuilds it from recorded results.
type Scope struct {
	item map[string]string
	env  map[string]string
	ran  map[Kind]map[string]cty.Value

	// frames holds the locals that an expression may read: those of the file, then those of each loop around the expression.
	// A frame holds expressions and no value. Each read evaluates them again from the recorded outputs.
	frames []frame
}

// frame is the locals of the file or of one loop, with the round of that loop.
// A local is evaluated in the frame that declares it, so it sees the round and the previous round of that loop.
type frame struct {
	locals []Local

	// round is the round of the loop, counted from 1, or 0 for the file.
	round int

	// previous holds the outputs that the steps of the loop recorded in the round before. It is nil for the file.
	previous map[Kind]map[string]cty.Value
}

// placed is a local with the index of the frame that declares it.
type placed struct {
	Local

	frame int
}

// Define adds the locals of the file to the scope.
func (s *Scope) Define(locals []Local) { s.frames = append(s.frames, frame{locals: locals}) }

// innermost returns the previous round of the closest loop, or nil outside a loop.
func (s *Scope) innermost() map[Kind]map[string]cty.Value {
	if len(s.frames) == 0 {
		return nil
	}

	return s.frames[len(s.frames)-1].previous
}

// NewScope returns a scope with an item and an environment and no step that ran.
func NewScope(item, env map[string]string) *Scope {
	return &Scope{item: item, env: env, ran: map[Kind]map[string]cty.Value{}}
}

// Record stores the outputs of a step that ran. An output that values does not hold is a typed null.
func (s *Scope) Record(step Step, values map[string]cty.Value) {
	put(s.ran, step, object(step, values))
}

// Skip stores a step that did not run because its `when` was false. All its outputs are typed nulls.
func (s *Scope) Skip(step Step) { s.Record(step, nil) }

// EnterLoop starts a loop. Until the first round ends, `loop.previous` holds a typed null for each output of each step of the body.
// It returns the function that leaves the loop and gives back the `loop.previous` of the loop around it.
func (s *Scope) EnterLoop(loop *Loop) (leave func()) {
	depth := len(s.frames)
	previous := map[Kind]map[string]cty.Value{}

	for _, step := range loop.Steps {
		put(previous, step, object(step, nil))
	}

	s.frames = append(s.frames, frame{locals: loop.Locals, round: 1, previous: previous})

	return func() { s.frames = s.frames[:depth] }
}

// EndRound records the outputs that the steps of the body of loop hold at the end of a round. The next round reads them as `loop.previous`.
// It runs after a round that did not end the loop, so every step of the body has run in that round, or was skipped.
// It also starts the next round, so a local of the loop reads the new `loop.round`.
func (s *Scope) EndRound(loop *Loop) {
	previous := map[Kind]map[string]cty.Value{}

	for _, step := range loop.Steps {
		put(previous, step, s.ran[step.Kind()][step.Label()])
	}

	closest := &s.frames[len(s.frames)-1]
	closest.previous = previous
	closest.round++
}

func put(into map[Kind]map[string]cty.Value, step Step, value cty.Value) {
	if into[step.Kind()] == nil {
		into[step.Kind()] = map[string]cty.Value{}
	}

	into[step.Kind()][step.Label()] = value
}

// object returns the outputs of a step as one value. An output that values does not hold is a typed null.
func object(step Step, values map[string]cty.Value) cty.Value {
	declared, _ := stepOutputs(step)
	attrs := maps.Clone(values)

	if attrs == nil {
		attrs = map[string]cty.Value{}
	}

	for _, o := range declared {
		if _, ok := attrs[o.Name]; !ok {
			attrs[o.Name] = cty.NullVal(o.Type)
		}
	}

	return cty.ObjectVal(attrs)
}

// Context returns the names an expression may read. round is the round of the loop around the
// expression, counted from 1, or 0 outside a loop. The context holds the functions of Functions and no local:
// Value adds the locals that its expression reads.
func (s *Scope) Context(round int) *hcl.EvalContext {
	return s.contextAt(round, s.innermost())
}

// contextAt is Context with the `loop.previous` of the loop whose round is given.
func (s *Scope) contextAt(round int, previous map[Kind]map[string]cty.Value) *hcl.EvalContext {
	vars := map[string]cty.Value{
		rootItem:  texts(s.item),
		rootEnv:   texts(s.env),
		rootLocal: cty.EmptyObjectVal,
	}

	for _, kind := range stepKinds() {
		vars[string(kind)] = cty.ObjectVal(valuesOr(s.ran[kind]))
	}

	vars[string(KindLoop)] = s.loops(round, previous)

	return &hcl.EvalContext{Variables: vars, Functions: Functions()}
}

// loops returns the value of `loop`: the loops that ran, `round` inside a loop and `previous` inside a loop.
func (s *Scope) loops(round int, previous map[Kind]map[string]cty.Value) cty.Value {
	loops := valuesOr(s.ran[KindLoop])
	if round > 0 {
		loops[roundName] = cty.NumberIntVal(int64(round))
	}

	if previous != nil {
		loops[previousName] = previousValue(previous)
	}

	return cty.ObjectVal(loops)
}

// previousValue returns the object that `loop.previous` reads: the outputs of each step, by kind and name.
func previousValue(previous map[Kind]map[string]cty.Value) cty.Value {
	kinds := map[string]cty.Value{}
	for kind, steps := range previous {
		kinds[string(kind)] = cty.ObjectVal(steps)
	}

	return cty.ObjectVal(kinds)
}

func texts(m map[string]string) cty.Value {
	attrs := map[string]cty.Value{}
	for k, v := range m {
		attrs[k] = cty.StringVal(v)
	}

	return cty.ObjectVal(attrs)
}

// valuesOr returns a copy of m that is safe to add to.
func valuesOr(m map[string]cty.Value) map[string]cty.Value {
	if m == nil {
		return map[string]cty.Value{}
	}

	return maps.Clone(m)
}

// Value evaluates expr. A failure is an error of the form `file:line:col: <what>. <fix>.`.
// A local that expr reads is evaluated first, from the recorded outputs, and holds no value after the call.
func (s *Scope) Value(expr hcl.Expression, round int) (cty.Value, error) {
	locals, err := s.chain(expr)
	if err != nil {
		return cty.NilVal, err
	}

	if err := s.notRun(append(readsOf(expr), readsOfLocals(locals)...)); err != nil {
		return cty.NilVal, err
	}

	ctx := s.Context(round)
	if err := s.bind(ctx, locals); err != nil {
		return cty.NilVal, err
	}

	value, diags := expr.Value(ctx)
	if diags.HasErrors() {
		return cty.NilVal, failure(expr, "this expression", diags)
	}

	return value, nil
}

// frameContext returns the context of frame i. It builds the context once and keeps it in contexts.
func (s *Scope) frameContext(contexts map[int]*hcl.EvalContext, i int) *hcl.EvalContext {
	if ctx, ok := contexts[i]; ok {
		return ctx
	}

	contexts[i] = s.contextAt(s.frames[i].round, s.frames[i].previous)

	return contexts[i]
}

// bind evaluates the locals in order, each with the locals before it, and puts them in ctx as `local`.
// A local is evaluated with the round and the `loop.previous` of the frame that declares it, whichever loop reads it.
func (s *Scope) bind(ctx *hcl.EvalContext, locals []placed) error {
	bound := map[string]cty.Value{}
	contexts := map[int]*hcl.EvalContext{}

	for _, l := range locals {
		own := s.frameContext(contexts, l.frame)
		own.Variables[rootLocal] = cty.ObjectVal(bound)

		value, diags := l.Expr.Value(own)
		if diags.HasErrors() {
			return failure(l.Expr, "local `"+l.Name+"`", diags)
		}

		bound[l.Name] = value
	}

	ctx.Variables[rootLocal] = cty.ObjectVal(bound)

	return nil
}

// failure is the error for an expression that cannot be evaluated.
func failure(expr hcl.Expression, what string, diags hcl.Diagnostics) error {
	detail := strings.TrimSuffix(diags[0].Detail, ".")

	return hclfile.At(expr.Range(), what+" cannot be evaluated: "+detail+". Check the names and the types it uses.")
}

// readsOf returns the references of expr that are not locals.
func readsOf(expr hcl.Expression) []hcl.Traversal {
	return slices.DeleteFunc(expr.Variables(), func(tr hcl.Traversal) bool { return tr.RootName() == rootLocal })
}

// readsOfLocals returns the references of the expressions of locals that are not locals.
func readsOfLocals(locals []placed) []hcl.Traversal {
	var out []hcl.Traversal

	for _, l := range locals {
		out = append(out, readsOf(l.Expr)...)
	}

	return out
}

// Reads returns the references that expr makes to a name that is not a local, and those of every local that it reads, directly or through other locals.
// A read of a local that does not exist adds nothing.
func (s *Scope) Reads(expr hcl.Expression) []hcl.Traversal {
	locals, err := s.chain(expr)
	if err != nil {
		return readsOf(expr)
	}

	return append(readsOf(expr), readsOfLocals(locals)...)
}

// chain returns the locals that expr reads, directly or through other locals, each after the locals that it reads.
// A read of a name that is no local, and a loop of locals, are errors.
func (s *Scope) chain(expr hcl.Expression) ([]placed, error) {
	c := &chainer{scope: s, done: map[string]bool{}}
	err := c.visit(expr, nil)

	return c.out, err
}

// chainer collects the locals that an expression reads. done holds the names already in out.
type chainer struct {
	scope *Scope
	done  map[string]bool
	out   []placed
}

// visit adds the locals that expr reads to out. path holds the names being visited.
func (c *chainer) visit(expr hcl.Expression, path []string) error {
	for _, tr := range expr.Variables() {
		if tr.RootName() != rootLocal {
			continue
		}

		if err := c.read(tr, path); err != nil {
			return err
		}
	}

	return nil
}

// read adds the local that tr names, after the locals that it reads.
func (c *chainer) read(tr hcl.Traversal, path []string) error {
	l, err := c.scope.find(tr)
	if err != nil {
		return err
	}

	if slices.Contains(path, l.Name) {
		return loopOfLocals(tr, path, l.Name)
	}

	if c.done[l.Name] {
		return nil
	}

	err = c.visit(l.Expr, append(path, l.Name))
	if err == nil {
		c.done[l.Name] = true
		c.out = append(c.out, l)
	}

	return err
}

// loopOfLocals is the error for a local that reads itself. It shows the names from the first local of the loop, closed back to it.
func loopOfLocals(tr hcl.Traversal, path []string, name string) error {
	names := append(slices.Clone(path[slices.Index(path, name):]), name)

	return hclfile.At(tr.SourceRange(), "cycle `"+strings.Join(names, " -> ")+"`. Remove one of the references.")
}

// find returns the local that a reference names. Loading refuses a name that two visible frames share, so at most one frame holds it.
func (s *Scope) find(tr hcl.Traversal) (placed, error) {
	name, named := attribute(tr, 1)

	for i, f := range s.frames {
		if j := slices.IndexFunc(f.locals, func(l Local) bool { return l.Name == name }); named && j >= 0 {
			return placed{Local: f.locals[j], frame: i}, nil
		}
	}

	return placed{}, hclfile.At(tr.SourceRange(), "`local."+name+"` names no local that this expression can read. Declare it in a `locals` block of the file or of the loop around it.")
}

// notRun reports the first reference to a step that has not run.
func (s *Scope) notRun(reads []hcl.Traversal) error {
	for _, tr := range reads {
		kind, name, named := stepReference(tr)
		if !named {
			continue
		}

		if _, ok := s.ran[kind][name]; !ok {
			return hclfile.At(tr.SourceRange(), fmt.Sprintf("`%s.%s` has not run yet. Move the step up or remove the reference.", kind, name))
		}
	}

	return nil
}

// stepReference returns the kind and the name that a traversal points at. It reports false for a root that is not a step or a loop, and for loop.round and loop.previous.
func stepReference(tr hcl.Traversal) (Kind, string, bool) {
	kind := Kind(tr.RootName())
	if !isStepKind(kind) {
		return "", "", false
	}

	name, named := attribute(tr, 1)

	return kind, name, named && (kind != KindLoop || (name != roundName && name != previousName))
}

// Bool evaluates a condition. A value that is null or is not a bool is an error.
func (s *Scope) Bool(expr hcl.Expression, round int) (bool, error) {
	value, err := s.Value(expr, round)
	if err != nil {
		return false, err
	}

	switch {
	case value.IsNull():
		return false, hclfile.At(expr.Range(), "this condition has no value, because a skipped step gave null. Compare the output with a value or guard it.")
	case value.Type() != cty.Bool:
		return false, hclfile.At(expr.Range(), fmt.Sprintf("this condition is a %s, not true or false. Write a comparison.", value.Type().FriendlyName()))
	}

	return value.True(), nil
}
