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

	// previous holds the outputs that the steps of the loop around the expression recorded in the round before. It is nil outside a loop.
	previous map[Kind]map[string]cty.Value
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
	outer := s.previous
	s.previous = map[Kind]map[string]cty.Value{}

	for _, step := range loop.Steps {
		put(s.previous, step, object(step, nil))
	}

	return func() { s.previous = outer }
}

// EndRound records the outputs that the steps of the body of loop hold at the end of a round. The next round reads them as `loop.previous`.
// It runs after a round that did not end the loop, so every step of the body has run in that round, or was skipped.
func (s *Scope) EndRound(loop *Loop) {
	s.previous = map[Kind]map[string]cty.Value{}

	for _, step := range loop.Steps {
		put(s.previous, step, s.ran[step.Kind()][step.Label()])
	}
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
// expression, counted from 1, or 0 outside a loop. The context holds no function.
func (s *Scope) Context(round int) *hcl.EvalContext {
	vars := map[string]cty.Value{
		rootItem: texts(s.item),
		rootEnv:  texts(s.env),
	}

	for _, kind := range stepKinds() {
		vars[string(kind)] = cty.ObjectVal(valuesOr(s.ran[kind]))
	}

	vars[string(KindLoop)] = s.loops(round)

	return &hcl.EvalContext{Variables: vars}
}

// loops returns the value of `loop`: the loops that ran, `round` inside a loop and `previous` inside a loop.
func (s *Scope) loops(round int) cty.Value {
	loops := valuesOr(s.ran[KindLoop])
	if round > 0 {
		loops[roundName] = cty.NumberIntVal(int64(round))
	}

	if s.previous != nil {
		loops[previousName] = previousValue(s.previous)
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
func (s *Scope) Value(expr hcl.Expression, round int) (cty.Value, error) {
	if err := s.notRun(expr); err != nil {
		return cty.NilVal, err
	}

	value, diags := expr.Value(s.Context(round))
	if diags.HasErrors() {
		detail := strings.TrimSuffix(diags[0].Detail, ".")

		return cty.NilVal, hclfile.At(expr.Range(), "this expression cannot be evaluated: "+detail+". Check the names and the types it uses.")
	}

	return value, nil
}

// notRun reports the first reference to a step that has not run.
func (s *Scope) notRun(expr hcl.Expression) error {
	for _, tr := range expr.Variables() {
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
