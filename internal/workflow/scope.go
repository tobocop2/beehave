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
		return []Output{{"head_sha", cty.String}, {"session", cty.String}, {"text", cty.String}, {"transcript", cty.String}}
	case KindCommand:
		return []Output{{"rc", cty.Number}, {"stdout", cty.String}}
	case KindGate:
		return []Output{{"head", cty.String}, {"passed", cty.Bool}, {"rc", cty.Number}, {"report", cty.String}}
	case KindWait:
		return []Output{{"answer", cty.String}}
	case KindLoop:
	}

	return []Output{{"exhausted", cty.Bool}}
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
}

// NewScope returns a scope with an item and an environment and no step that ran.
func NewScope(item, env map[string]string) *Scope {
	return &Scope{item: item, env: env, ran: map[Kind]map[string]cty.Value{}}
}

// Record stores the outputs of a step that ran. An output that values does not hold is a typed null.
func (s *Scope) Record(step Step, values map[string]cty.Value) {
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

	if s.ran[step.Kind()] == nil {
		s.ran[step.Kind()] = map[string]cty.Value{}
	}

	s.ran[step.Kind()][step.Label()] = cty.ObjectVal(attrs)
}

// Skip stores a step that did not run because its `when` was false. All its outputs are typed nulls.
func (s *Scope) Skip(step Step) { s.Record(step, nil) }

// Context returns the names an expression may read. round is the round of the loop around the
// expression, counted from 1, or 0 outside a loop. The context holds no function.
func (s *Scope) Context(round int) *hcl.EvalContext {
	vars := map[string]cty.Value{
		rootItem: texts(s.item),
		rootEnv:  texts(s.env),
	}

	for _, kind := range []Kind{KindAgent, KindCommand, KindGate, KindWait} {
		vars[string(kind)] = cty.ObjectVal(valuesOr(s.ran[kind]))
	}

	loops := valuesOr(s.ran[KindLoop])
	if round > 0 {
		loops[roundName] = cty.NumberIntVal(int64(round))
	}

	vars[string(KindLoop)] = cty.ObjectVal(loops)

	return &hcl.EvalContext{Variables: vars}
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

// stepReference returns the kind and the name that a traversal points at. It reports false for a root that is not a step or a loop, and for loop.round.
func stepReference(tr hcl.Traversal) (Kind, string, bool) {
	kind := Kind(tr.RootName())
	if kind != KindAgent && kind != KindCommand && kind != KindGate && kind != KindWait && kind != KindLoop {
		return "", "", false
	}

	name, named := attribute(tr, 1)

	return kind, name, named && (kind != KindLoop || name != roundName)
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
