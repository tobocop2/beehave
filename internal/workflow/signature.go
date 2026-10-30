package workflow

import (
	"errors"
	"fmt"
	"maps"
	"math/big"
	"slices"
	"strings"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

// Kind is the type that a keyword of a builtin takes. KindAny is a keyword that the check leaves to the run.
type Kind string

// The kinds of a keyword. A list is a list or a tuple, a dict is a dict, and text is a string.
const (
	KindAny  Kind = "any"
	KindText Kind = "text"
	KindInt  Kind = "int"
	KindBool Kind = "bool"
	KindList Kind = "list"
	KindDict Kind = "dict"
)

// Param is one keyword of a builtin. Values is the closed set of the literal values, or nil when the keyword takes any value of its kind.
type Param struct {
	Name     string
	Kind     Kind
	Required bool
	Values   []string
	// Why ends the message for a missing required keyword. It says what the keyword is for, when that is not plain.
	Why string
}

// StatusValues lists the values of the status argument of an item_status call.
func StatusValues() []string { return []string{"open", "closed", "hold"} }

// ChildrenValues lists the values of the children argument of a merge call.
func ChildrenValues() []string { return []string{"refuse", "retarget"} }

func req(name string, kind Kind) Param { return Param{Name: name, Kind: kind, Required: true} }
func opt(name string, kind Kind) Param { return Param{Name: name, Kind: kind} }
func one(name string, values []string) Param {
	return Param{Name: name, Kind: KindText, Values: values}
}

// signatures holds the keywords of each builtin that takes keywords, in the order of their positions. The run reads its keywords from here,
// so the check and the run cannot disagree. struct takes any keyword and json is a module, so neither is listed.
func signatures() map[string][]Param {
	all := stepSignatures()
	maps.Copy(all, swarmSignatures())
	maps.Copy(all, itemSignatures())

	return all
}

// stepSignatures holds the builtins that run something.
func stepSignatures() map[string][]Param {
	label := opt(nameKeyword, KindText)

	return map[string][]Param{
		agentName: {
			req("prompt", KindText), req("model", KindText), opt("runner", KindText), opt("inputs", KindDict), opt("outputs", KindList),
			opt("pin", KindText), opt("resume", KindText), opt("require_skills", KindList), opt("forbid_commands", KindList), opt("output_tries", KindInt),
			one("follow_ups", FollowUpModes()), opt("timeout", KindText), opt("idle_timeout", KindText), label,
		},
		commandName: {
			req("run", KindList), opt("host", KindText), opt("stdin", KindText), opt("env", KindDict), opt("timeout", KindText), opt("stop_on_rc", KindList), label,
		},
		"env": {req(nameKeyword, KindText), opt("default", KindText)},
		gateName: {
			opt("run", KindList), opt("host", KindText), opt("timeout", KindText), opt("head", KindText), label,
		},
		waitName:         {opt("question", KindText), label},
		hostTeardownName: {label},
	}
}

// swarmSignatures holds the builtins that start, wait for and land child runs.
func swarmSignatures() map[string][]Param {
	label := opt(nameKeyword, KindText)

	return map[string][]Param{
		batchMergeName: {
			req("prs", KindList), req("base", KindText), req("repo", KindText), opt("hold", KindText), opt("own", KindDict),
			opt("forbid_text", KindList), opt("gate_run", KindList),
			{Name: nameKeyword, Kind: KindText, Required: true, Why: "The name makes the step names of the call unique, so use one for each call of the run."},
		},
		gatherName: {req("runs", KindAny), one(failKeyword, FailModes()), one(untilKeyword, UntilModes()), label},
		spawnName:  {req("items", KindList), opt(entryKeyword, KindText), label},
		queueName:  {label, opt("hold", KindText)},
	}
}

// itemSignatures holds the builtins that write the tracker or merge.
func itemSignatures() map[string][]Param {
	label := opt(nameKeyword, KindText)

	return map[string][]Param{
		itemCommentName: {
			req("id", KindText), req("text", KindText), req("key", KindText), label,
		},
		itemCreateName: {
			req("title", KindText), req("key", KindText), opt("body", KindText), opt("parent", KindText), label,
		},
		itemStatusName: {req("id", KindText), {Name: "status", Kind: KindText, Required: true, Values: StatusValues()}, label},
		preflightName:  {opt("head", KindText), opt("base", KindText), opt("hold", KindText), opt("own", KindDict), opt("id", KindText), label},
		mergeName: {
			req("head", KindText), opt("repo", KindText), opt("base", KindText), opt("hold", KindText), opt("own", KindDict), opt("approved", KindBool),
			opt("tree", KindText), opt("forbid_text", KindList), one("children", ChildrenValues()), opt("merge", KindBool), label,
		},
	}
}

// Signature returns the keywords of a builtin, and false for a name that takes no fixed keywords.
func Signature(builtin string) ([]Param, bool) {
	params, ok := signatures()[builtin]

	return params, ok
}

// Signed lists the builtins that have a signature.
func Signed() []string {
	names := make([]string, 0, len(signatures()))
	for name := range signatures() {
		names = append(names, name)
	}

	slices.Sort(names)

	return names
}

// Unpack reads the arguments of a call of the builtin into the destinations, each named by its keyword.
// It is starlark.UnpackArgs with the keywords, their order and which of them are optional taken from the signature.
// The names bind each destination, so two destinations of one type cannot swap by their order.
func Unpack(builtin string, args starlark.Tuple, kwargs []starlark.Tuple, into map[string]any) error {
	pairs, ok := keywordPairs(builtin, into)
	if !ok {
		return errors.New(builtin + " is read with destinations that differ from the keywords of its signature")
	}

	return starlark.UnpackArgs(builtin, args, kwargs, pairs...)
}

// unpackName is the name of the keyword as starlark.UnpackArgs takes it, with a "?" on an optional one.
func (p Param) unpackName() string {
	if p.Required {
		return p.Name
	}

	return p.Name + "?"
}

// keywordPairs lists the keywords of the builtin in the order of its signature, each with its destination and a "?" on an optional one.
// It is false when the destinations do not name the keywords exactly.
func keywordPairs(builtin string, into map[string]any) ([]any, bool) {
	params, ok := Signature(builtin)
	if !ok || len(params) != len(into) {
		return nil, false
	}

	var pairs []any

	for _, param := range params {
		dest, found := into[param.Name]
		if !found {
			return nil, false
		}

		pairs = append(pairs, param.unpackName(), dest)
	}

	return pairs, true
}

// kindOf is the kind of a literal expression, or KindAny for an expression that the check cannot read before the run.
func kindOf(expr syntax.Expr) Kind {
	switch e := expr.(type) {
	case *syntax.Literal:
		switch e.Value.(type) {
		case string:
			return KindText
		case int64, *big.Int:
			return KindInt
		}
	case *syntax.ListExpr, *syntax.TupleExpr:
		return KindList
	case *syntax.DictExpr:
		return KindDict
	case *syntax.Ident:
		if e.Name == "True" || e.Name == "False" {
			return KindBool
		}
	}

	return KindAny
}

// splat reports whether a call passes *args or **kwargs, so the check cannot know which keywords it sets.
func splat(call *syntax.CallExpr) bool {
	return slices.ContainsFunc(call.Args, func(arg syntax.Expr) bool {
		unary, ok := arg.(*syntax.UnaryExpr)

		return ok && (unary.Op == syntax.STAR || unary.Op == syntax.STARSTAR)
	})
}

func paramNames(params []Param) []string {
	names := make([]string, len(params))
	for i, param := range params {
		names[i] = param.Name
	}

	return names
}

// signature checks one call of a builtin against its keywords: a keyword it does not take, a keyword it needs, a literal outside the closed set
// and a literal of the wrong kind. A value that is not a literal is left to the run.
func (g *graph) signature(call *syntax.CallExpr, builtin string, params []Param) {
	g.keywords(call, builtin, paramNames(params))

	if splat(call) {
		return
	}

	for i, param := range params {
		value := argument(call, param.Name, i)
		if value == nil {
			if param.Required {
				g.report(placeOf(call.Lparen), strings.TrimSpace(fmt.Sprintf("%s needs the `%s` keyword. Add %s = ... to the call. %s", builtin, param.Name, param.Name, param.Why)))
			}

			continue
		}

		g.value(value, param)
	}
}

// value checks one literal against the closed set and the kind of its keyword.
func (g *graph) value(value syntax.Expr, param Param) {
	got := kindOf(value)
	if got == KindAny {
		return
	}

	if param.Values != nil {
		if text, _ := literal(value); got != KindText || !slices.Contains(param.Values, text) {
			g.report(placeOf(syntax.Start(value)), fmt.Sprintf("%s is %s. Use one of: %s.", param.Name, rawText(value), strings.Join(param.Values, ", ")))
		}

		return
	}

	if param.Kind != KindAny && got != param.Kind {
		g.report(placeOf(syntax.Start(value)), fmt.Sprintf("%s must be %s, not %s.", param.Name, withArticle(param.Kind), withArticle(got)))
	}
}

// withArticle puts "a" or "an" before the name of a kind.
func withArticle(kind Kind) string {
	if kind == KindInt {
		return "an " + string(kind)
	}

	return "a " + string(kind)
}

// rawText is the source text of a literal, or the kind of any other expression.
func rawText(expr syntax.Expr) string {
	if lit, ok := expr.(*syntax.Literal); ok {
		return lit.Raw
	}

	return string(kindOf(expr))
}
