package workflow

import (
	"errors"
	"fmt"
	"maps"
	"math/big"
	"slices"
	"strings"
	"time"

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
	// Rule is the validation that the run applies to the value of the keyword. The check applies it to a value that is a literal, and the run applies
	// it to the value it got, so one function owns the rule. It is nil for a keyword with no rule beyond its kind.
	Rule func(starlark.Value) error
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

// isBoolName reports whether name is one of the two boolean constants of Starlark.
func isBoolName(name string) bool { return name == trueName || name == falseName }

const (
	trueName  = "True"
	falseName = "False"
)

func opt(name string, kind Kind) Param { return Param{Name: name, Kind: kind} }

// words is an optional list keyword whose elements must be text.
func words(name string) Param { return Param{Name: name, Kind: KindList, Rule: wordsRule(name)} }

// ruled is an optional keyword of a kind with the rule of the run.
func ruled(name string, kind Kind, rule func(starlark.Value) error) Param {
	return Param{Name: name, Kind: kind, Rule: rule}
}

// wordsRule is the rule of a keyword that takes a list of text.
func wordsRule(name string) func(starlark.Value) error {
	return func(v starlark.Value) error {
		_, err := Words(name, v)

		return err
	}
}

// durationRule is the rule of a timeout keyword.
func durationRule(name string) func(starlark.Value) error {
	return timeoutRule(name, Duration)
}

// deadlineRule is the rule of a timeout keyword that sets a deadline: the value must be above zero.
func deadlineRule(name string) func(starlark.Value) error {
	return timeoutRule(name, Deadline)
}

func timeoutRule(name string, read func(name, text string) (time.Duration, error)) func(starlark.Value) error {
	return func(v starlark.Value) error {
		if v == starlark.None {
			var text string

			return noneFor(name, &text)
		}

		text, _ := starlark.AsString(v)
		_, err := read(name, text)

		return err
	}
}

// noneFor is the error of the run for a None in a keyword that it reads into a Go value of the type of dest.
func noneFor(name string, dest any) error {
	err := starlark.UnpackArgs("", nil, []starlark.Tuple{{starlark.String(name), starlark.None}}, name+"?", dest)

	// A None never fits a text, a whole number or a flag, so err is set. The step name goes in front when the check reports it.
	return errors.New(strings.TrimPrefix(err.Error(), ": "))
}

func triesRule(v starlark.Value) error {
	if v == starlark.None {
		var tries int

		return noneFor("output_tries", &tries)
	}

	n, err := starlark.AsInt32(v)
	if err != nil {
		if i, isInt := v.(starlark.Int); isInt && i.Sign() < 0 { // a number beyond 32 bits is below one only when it is negative
			return OutputTries(0)
		}

		return nil
	}

	return OutputTries(n)
}

func stopRule(v starlark.Value) error {
	_, err := StopCodes(v)

	return err
}

func inputsRule(v starlark.Value) error {
	_, err := InputMap(v)

	return err
}

func allowRule(v starlark.Value) error {
	_, _, err := AllowPaths(v)

	return err
}

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

// agentParams holds the keywords of the agent builtin.
func agentParams() []Param {
	return []Param{
		req("prompt", KindText), req("model", KindText), opt("runner", KindText), ruled("inputs", KindDict, inputsRule), words("outputs"),
		opt("pin", KindText), opt("resume", KindText), words("require_skills"),
		{Name: "forbid_commands", Kind: KindList, Each: ForbidKinds()},
		ruled("allow_paths", KindList, allowRule), words("protect_paths"), ruled("output_tries", KindInt, triesRule),
		opt("edits", KindBool), one("follow_ups", FollowUpModes()), one("kind", AgentKinds()), one("lane", Lanes()), ruled("timeout", KindText, durationRule("timeout")),
		ruled("idle_timeout", KindText, durationRule("idle_timeout")), opt(sandboxKeyword, KindAny), opt(nameKeyword, KindText),
	}
}

// stepSignatures holds the builtins that run something.
func stepSignatures() map[string][]Param {
	label := opt(nameKeyword, KindText)

	return map[string][]Param{
		agentName:   agentParams(),
		sandboxName: {opt("network", KindList), opt("allow_write", KindList)},
		commandName: {
			{Name: "run", Kind: KindList, Required: true, Rule: wordsRule("run")},
			opt("host", KindText), opt("stdin", KindText), opt("env", KindDict),
			ruled("timeout", KindText, durationRule("timeout")), ruled("stop_on_rc", KindList, stopRule), opt("max_new_processes", KindInt), label,
		},
		"env": {req(nameKeyword, KindText), opt("default", KindText)},
		gateName: {
			words("run"), opt("host", KindText), ruled("timeout", KindText, durationRule("timeout")), opt("head", KindText), label,
		},
		waitName:         {opt("question", KindText), ruled("timeout", KindText, deadlineRule("timeout")), label},
		hostTeardownName: {label},
		hostUpName:       {label},
	}
}

// swarmSignatures holds the builtins that start, wait for and land child runs.
func swarmSignatures() map[string][]Param {
	label := opt(nameKeyword, KindText)

	return map[string][]Param{
		batchMergeName: {
			req("prs", KindList), req("base", KindText), req("repo", KindText), opt("hold", KindText), opt("own", KindDict),
			words("forbid_text"), words("gate_run"), opt("integration", KindInt),
			{Name: nameKeyword, Kind: KindText, Required: true, Why: "The name makes the step names of the call unique, so use one for each call of the run."},
		},
		gatherName: {req("runs", KindAny), one(failKeyword, FailModes()), one(untilKeyword, UntilModes()), label},
		spawnName:  {{Name: "items", Kind: KindList, Required: true, Rule: wordsRule("items")}, opt(entryKeyword, KindText), label},
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
			opt("tree", KindText), opt("steps", KindList), words("forbid_text"), one("children", ChildrenValues()), opt("merge", KindBool), label,
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
	case *syntax.ParenExpr:
		return kindOf(e.X)
	case *syntax.Literal:
		return literalKind(e)
	case *syntax.UnaryExpr:
		return negativeKind(e)
	case *syntax.ListExpr, *syntax.TupleExpr:
		return KindList
	case *syntax.DictExpr:
		return KindDict
	case *syntax.Ident:
		if isBoolName(e.Name) {
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
			g.missing(call, builtin, param)

			continue
		}

		g.value(builtin, value, param)
	}

	g.pairs(call, builtin, params)
}

// pairs checks the rules of a call that tie two keywords together.
func (g *graph) pairs(call *syntax.CallExpr, builtin string, params []Param) {
	switch builtin {
	case agentName:
		g.protection(call, params)
	case commandName:
		g.hostWithCap(call, params)
	}
}

// hostWithCap checks max_new_processes of a command call with the rule of the run and the real pair of values the run passes it: the value and
// the host. It reports one error for the argument. A value that is not a literal, or a host that is neither absent nor a text literal, is left
// to the run, and so is a value of the wrong kind, which the kind check reports.
func (g *graph) hostWithCap(call *syntax.CallExpr, params []Param) {
	index := func(name string) int {
		return slices.IndexFunc(params, func(p Param) bool { return p.Name == name })
	}

	limit := argument(call, "max_new_processes", index("max_new_processes"))
	value, isLiteral := capLiteral(limit)
	host, known := hostText(argument(call, "host", index("host")))

	if !isLiteral || !known {
		return
	}

	if _, err := ProcessCap(value, host); err != nil {
		g.report(placeOf(syntax.Start(limit)), commandName+": "+err.Error()+".")
	}
}

// capLiteral is the value of a max_new_processes argument that is a literal of the kind int or none. A value of another kind is left to the kind check.
func capLiteral(limit syntax.Expr) (starlark.Value, bool) { //nolint:ireturn // the value is any of the Starlark types
	if limit == nil {
		return nil, false
	}

	if kind := kindOf(limit); kind != KindInt && kind != KindAny {
		return nil, false
	}

	return literalValue(limit)
}

// hostText is the host of a call: empty when the call leaves it out. known is false for a host that is not a text literal.
func hostText(arg syntax.Expr) (host string, known bool) {
	if arg == nil {
		return "", true
	}

	return literal(arg)
}

// missing reports a keyword that the call needs and leaves out.
func (g *graph) missing(call *syntax.CallExpr, builtin string, param Param) {
	if param.Required {
		g.report(placeOf(call.Lparen), strings.TrimSpace(fmt.Sprintf("%s needs the `%s` keyword. Add %s = ... to the call. %s", builtin, param.Name, param.Name, param.Why)))
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

	if hasEmptyText(pathsArg) {
		g.report(placeOf(call.Lparen), "protect_paths holds an empty path, which would name the folder of the step. Give a path.")

		return
	}

	kinds, kindsKnown := textList(argument(call, "forbid_commands", index("forbid_commands")))
	if pathsKnown && kindsKnown {
		g.pair(call, kinds, paths)
	}
}

// pair reports a list of kinds and a list of paths that do not go together. A list with an unknown kind is left to the check of each kind.
func (g *graph) pair(call *syntax.CallExpr, kinds, paths []string) {
	if slices.ContainsFunc(kinds, func(kind string) bool { return !slices.Contains(ForbidKinds(), kind) }) {
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

// hasEmptyText reports whether a list literal has a member that is an empty text literal.
func hasEmptyText(value syntax.Expr) bool {
	items, isList := members(value)

	return isList && slices.ContainsFunc(items, func(item syntax.Expr) bool {
		text, isText := literal(item)

		return isText && text == ""
	})
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
	if ident, isIdent := merges.(*syntax.Ident); merges == nil || (isIdent && ident.Name == falseName) {
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
func (g *graph) value(builtin string, value syntax.Expr, param Param) {
	got := kindOf(value)
	if got == KindAny {
		g.none(builtin, value, param)

		return
	}

	if param.Values != nil {
		g.closedSet(value, got, param)

		return
	}

	if !g.kindFits(value, got, param) {
		return
	}

	g.each(value, param)
	g.rule(builtin, value, param)
}

// kindFits reports whether the kind of a literal is the kind of its keyword, and reports the literal when it is not.
func (g *graph) kindFits(value syntax.Expr, got Kind, param Param) bool {
	if param.Kind == KindAny || got == param.Kind {
		return true
	}

	g.report(placeOf(syntax.Start(value)), fmt.Sprintf("%s must be %s, not %s.", param.Name, withArticle(param.Kind), withArticle(got)))

	return false
}

// none applies the rule of the run to a literal None, which has no kind. The rule says whether the run takes it.
func (g *graph) none(builtin string, value syntax.Expr, param Param) {
	if v, ok := literalValue(value); ok && v == starlark.None {
		g.rule(builtin, value, param)
	}
}

// closedSet reports a literal that is not one of the values of its keyword.
func (g *graph) closedSet(value syntax.Expr, got Kind, param Param) {
	if text, _ := literal(value); got != KindText || !slices.Contains(param.Values, text) {
		g.report(placeOf(syntax.Start(value)), fmt.Sprintf("%s is %s. Use one of: %s.", param.Name, rawText(value), strings.Join(param.Values, ", ")))
	}
}

// rule applies the rule of the run to a value that is a literal, and reports the error of the run with the name of the step. A value that is
// not a literal is left to the run.
func (g *graph) rule(builtin string, value syntax.Expr, param Param) {
	if param.Rule == nil {
		return
	}

	v, ok := literalValue(value)
	if !ok {
		return
	}

	if err := param.Rule(v); err != nil {
		g.report(placeOf(syntax.Start(value)), builtin+": "+err.Error()+".")
	}
}

// literalValue is the Starlark value of an expression made of literals only: text, numbers, True, False, None, and lists, tuples and dicts of them.
// It reads the syntax and runs no code. ok is false for any other expression.
func literalValue(expr syntax.Expr) (starlark.Value, bool) { //nolint:ireturn // the value is any of the Starlark types
	switch e := expr.(type) {
	case *syntax.ParenExpr:
		return literalValue(e.X)
	case *syntax.Literal:
		return literalOf(e)
	case *syntax.UnaryExpr:
		return negated(e)
	case *syntax.Ident:
		return constant(e.Name)
	}

	return literalCollection(expr)
}

// literalCollection is the value of a list, a tuple or a dict of literals.
func literalCollection(expr syntax.Expr) (starlark.Value, bool) { //nolint:ireturn // the value is any of the Starlark types
	switch e := expr.(type) {
	case *syntax.ListExpr:
		items, ok := literalItems(e.List)

		return starlark.NewList(items), ok
	case *syntax.TupleExpr:
		items, ok := literalItems(e.List)

		return starlark.Tuple(items), ok
	case *syntax.DictExpr:
		return literalDict(e)
	}

	return nil, false
}

func literalOf(lit *syntax.Literal) (starlark.Value, bool) { //nolint:ireturn // the value is any of the Starlark types
	switch v := lit.Value.(type) {
	case string:
		return starlark.String(v), true
	case int64:
		return starlark.MakeInt64(v), true
	case *big.Int:
		return starlark.MakeBigInt(v), true
	}

	f, _ := lit.Value.(float64) //nolint:errcheck // the only other literal is a float
	return starlark.Float(f), true
}

func negated(e *syntax.UnaryExpr) (starlark.Value, bool) { //nolint:ireturn // the value is any of the Starlark types
	if e.Op != syntax.MINUS {
		return nil, false
	}

	switch inner, ok := literalValue(e.X); v := inner.(type) {
	case starlark.Int:
		return starlark.MakeInt(0).Sub(v), ok
	case starlark.Float:
		return -v, ok
	}

	return nil, false
}

func constant(name string) (starlark.Value, bool) { //nolint:ireturn // the value is any of the Starlark types
	switch name {
	case trueName:
		return starlark.True, true
	case falseName:
		return starlark.False, true
	case noneName:
		return starlark.None, true
	}

	return nil, false
}

func literalItems(exprs []syntax.Expr) ([]starlark.Value, bool) {
	items := make([]starlark.Value, 0, len(exprs))

	for _, expr := range exprs {
		item, ok := literalValue(expr)
		if !ok {
			return nil, false
		}

		items = append(items, item)
	}

	return items, true
}

func literalDict(e *syntax.DictExpr) (starlark.Value, bool) { //nolint:ireturn // the value is any of the Starlark types
	dict := starlark.NewDict(len(e.List))

	for _, entry := range e.List {
		pair := entry.(*syntax.DictEntry) //nolint:errcheck,forcetypeassert // the entries of a dict expression are dict entries

		key, keyOK := literalValue(pair.Key)
		val, valOK := literalValue(pair.Value)

		if !keyOK || !valOK || dict.SetKey(key, val) != nil {
			return nil, false
		}
	}

	return dict, true
}

// each checks the literal elements of a list literal against the closed set of its keyword. An element that is not a literal is left to the run.
func (g *graph) each(value syntax.Expr, param Param) {
	if param.Each == nil {
		return
	}

	items, _ := members(value) // the kind of the value is a list, so it is a list or a tuple

	for _, item := range items {
		if kindOf(item) == KindAny {
			continue
		}

		if text, _ := literal(item); kindOf(item) != KindText || !slices.Contains(param.Each, text) {
			g.report(placeOf(syntax.Start(item)), fmt.Sprintf("%s has the unknown kind %s. Use one of: %s.", param.Name, rawText(item), strings.Join(param.Each, ", ")))
		}
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
