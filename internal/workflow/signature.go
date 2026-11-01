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

	"github.com/tobocop2/obee/internal/model"
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
	// Each is the closed set of the literal text elements of a list keyword. The check reads each literal element and leaves a list or an element that is not a literal to the run.
	Each []string
	// Min and Max bound a whole number. Both are zero for a keyword with no bounds.
	Min, Max int
	// Why ends the message for a missing required keyword. It says what the keyword is for, when that is not plain.
	Why string
}

// StatusValues lists the values of the status argument of an item_status call.
func StatusValues() []string { return []string{"open", "closed", "hold"} }

// ChildrenValues lists the values of the children argument of a merge call.
func ChildrenValues() []string { return []string{"refuse", "retarget"} }

// ForbidKinds lists the values that an element of the forbid_commands argument takes: the names of the finding kinds.
// The check and the run both read it.
func ForbidKinds() []string { return model.FindingKindNames() }

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
			opt("pin", KindText), opt("resume", KindText), opt("require_skills", KindList), {Name: "forbid_commands", Kind: KindList, Each: ForbidKinds()}, opt("allow_paths", KindList), opt("protect_paths", KindList), opt("output_tries", KindInt),
			opt("edits", KindBool), one("follow_ups", FollowUpModes()), one("kind", AgentKinds()), opt("timeout", KindText), opt("idle_timeout", KindText), label,
		},
		commandName: {
			req("run", KindList), opt("host", KindText), opt("stdin", KindText), opt("env", KindDict), opt("timeout", KindText), opt("stop_on_rc", KindList), {Name: "max_new_processes", Kind: KindInt, Min: MinProcessCap, Max: MaxProcessCap}, label,
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
			opt("forbid_text", KindList), opt("gate_run", KindList), opt("integration", KindInt),
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
			opt("tree", KindText), opt("steps", KindList), opt("forbid_text", KindList), one("children", ChildrenValues()), opt("merge", KindBool), label,
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

// literalKind is the kind of a text or a number literal, and KindAny for any other.
func literalKind(lit *syntax.Literal) Kind {
	switch lit.Value.(type) {
	case string:
		return KindText
	case int64, *big.Int:
		return KindInt
	}

	return KindAny
}

// negativeKind is KindInt for a minus sign before a whole number, and KindAny for any other unary expression.
func negativeKind(e *syntax.UnaryExpr) Kind {
	if e.Op == syntax.MINUS && kindOf(e.X) == KindInt {
		return KindInt
	}

	return KindAny
}

// kindOf is the kind of a literal expression, or KindAny for an expression that the check cannot read before the run.
func kindOf(expr syntax.Expr) Kind {
	switch e := expr.(type) {
	case *syntax.Literal:
		return literalKind(e)
	case *syntax.UnaryExpr:
		return negativeKind(e)
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

	if builtin == agentName {
		g.protection(call, params)
	}
}

// protection checks the two rules of the run that tie forbid_commands to protect_paths: a kind that needs paths takes at least one, and paths
// need such a kind. An empty path is always wrong. Both rules need both sides to be absent or list literals of text literals, and every kind to be
// a known one (the check of each kind reports the rest). Any other value is left to the run.
func (g *graph) protection(call *syntax.CallExpr, params []Param) {
	index := func(name string) int {
		return slices.IndexFunc(params, func(p Param) bool { return p.Name == name })
	}

	pathsArg := argument(call, "protect_paths", index("protect_paths"))
	paths, pathsKnown := textList(pathsArg)

	if items, isList := members(pathsArg); isList && slices.ContainsFunc(items, func(item syntax.Expr) bool { text, isText := literal(item); return isText && text == "" }) {
		g.report(placeOf(call.Lparen), "protect_paths holds an empty path, which would name the folder of the step. Give a path.")

		return
	}

	kinds, kindsKnown := textList(argument(call, "forbid_commands", index("forbid_commands")))
	if !pathsKnown || !kindsKnown || slices.ContainsFunc(kinds, func(kind string) bool { return !slices.Contains(ForbidKinds(), kind) }) {
		return
	}

	needs := slices.ContainsFunc(kinds, func(kind string) bool { return model.FindingKind(kind).NeedsPaths() })

	switch {
	case needs && len(paths) == 0:
		g.report(placeOf(call.Lparen), fmt.Sprintf("forbid_commands lists %s, so protect_paths must list at least one directory.", model.FindingProtected))
	case !needs && len(paths) > 0:
		g.report(placeOf(call.Lparen), fmt.Sprintf("protect_paths is set, but forbid_commands lists no kind that uses it. Add %q to forbid_commands.", model.FindingProtected))
	}
}

// textList returns the text of the members of a list literal whose members are all text literals. An absent argument is an empty list. ok is false
// for any other expression, and for a list with a member that is not a text literal.
func textList(value syntax.Expr) (texts []string, ok bool) {
	if value == nil {
		return nil, true
	}

	items, isList := members(value)
	if !isList {
		return nil, false
	}

	for _, item := range items {
		text, isText := literal(item)
		if !isText {
			return nil, false
		}

		texts = append(texts, text)
	}

	return texts, true
}

// mergeNeedsSteps reports a merge call that can merge and names no gate steps: it leaves out `steps` or passes an empty list literal.
// A call that sets `merge` to False, or leaves `merge` out, is an owner merge and needs none. A computed `merge` can be True, so it needs them.
func (g *graph) mergeNeedsSteps(call *syntax.CallExpr, params []Param) {
	if splat(call) {
		return
	}

	index := func(name string) int {
		return slices.IndexFunc(params, func(p Param) bool { return p.Name == name })
	}

	merges := argument(call, "merge", index("merge"))
	if ident, isIdent := merges.(*syntax.Ident); merges == nil || (isIdent && ident.Name == "False") {
		return
	}

	steps := argument(call, "steps", index("steps"))
	if steps != nil && !emptyList(steps) {
		return
	}

	g.report(placeOf(call.Lparen), "merge needs the gate steps when it merges. Add steps = <gate>.steps to the call, or set merge = False and the owner merges.")
}

// emptyList reports whether an expression is a list or a tuple literal with no item.
func emptyList(expr syntax.Expr) bool {
	switch e := expr.(type) {
	case *syntax.ListExpr:
		return len(e.List) == 0
	case *syntax.TupleExpr:
		return len(e.List) == 0
	}

	return false
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

		return
	}

	g.bounds(value, param)
	g.each(value, param)
}

// each checks the literal elements of a list literal against the closed set of its keyword. An element that is not a literal is left to the run.
func (g *graph) each(value syntax.Expr, param Param) {
	if param.Each == nil {
		return
	}

	items, ok := members(value)
	if !ok {
		return
	}

	for _, item := range items {
		if kindOf(item) == KindAny {
			continue
		}

		if text, _ := literal(item); kindOf(item) != KindText || !slices.Contains(param.Each, text) {
			g.report(placeOf(syntax.Start(item)), fmt.Sprintf("%s has the unknown kind %s. Use one of: %s.", param.Name, rawText(item), strings.Join(param.Each, ", ")))
		}
	}
}

// bounds reports an integer literal that is outside the bounds of its keyword. A literal that is not a small whole number is outside.
func (g *graph) bounds(value syntax.Expr, param Param) {
	if param.Max == 0 {
		return
	}

	lit, isLit := value.(*syntax.Literal) // the only other expression of the int kind is a negative number
	n, small := int64(0), false

	if isLit {
		n, small = lit.Value.(int64)
	}

	if !small || n < int64(param.Min) || n > int64(param.Max) {
		g.report(placeOf(syntax.Start(value)), fmt.Sprintf("%s is %s. Write a whole number from %d to %d.", param.Name, rawText(value), param.Min, param.Max))
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

	if neg, ok := expr.(*syntax.UnaryExpr); ok && neg.Op == syntax.MINUS {
		return "-" + rawText(neg.X)
	}

	return string(kindOf(expr))
}
