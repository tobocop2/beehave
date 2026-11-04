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
	// AsValue is true when the run reads the keyword as a Starlark value and not into a Go text, number or flag. A None then reaches Rule,
	// and the run does not refuse it at unpack.
	AsValue bool
}

// StatusValues lists the values of the status argument of an item_status call.
func StatusValues() []string { return []string{"open", "closed", "hold"} }

// ChildrenValues lists the values of the children argument of a merge call.
func ChildrenValues() []string { return []string{"refuse", "retarget"} }

// ForbidKinds lists the values that an element of the forbid_commands argument takes: the names of the finding kinds.
// The check and the run both read it.
func ForbidKinds() []string { return model.FindingKindNames() }

// Protection is the rule that ties forbid_commands to protect_paths: a kind that needs paths takes at least one, paths need such a kind, and an
// empty path is always wrong. The run applies it to the values it read, and the check applies it to literals, so this function owns the rule.
func Protection(kinds []model.FindingKind, paths []string) error {
	if slices.Contains(paths, "") {
		return errors.New("protect_paths holds an empty path, which would name the folder of the step. Give a path")
	}

	for _, kind := range kinds {
		if kind.NeedsPaths() && len(paths) == 0 {
			return fmt.Errorf("forbid_commands lists %s, so protect_paths must list at least one directory", kind)
		}
	}

	if len(paths) > 0 && !slices.ContainsFunc(kinds, model.FindingKind.NeedsPaths) {
		return fmt.Errorf("protect_paths is set, but forbid_commands lists no kind that uses it. Add %q to forbid_commands", model.FindingProtected)
	}

	return nil
}

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
		text, _ := starlark.AsString(v) // the unpack check has refused a value that is not text
		_, err := read(name, text)

		return err
	}
}

// goDestination is a new Go value of the type that the run reads a keyword of the kind into, or nil for a kind that the run reads as a value.
func goDestination(kind Kind) any {
	switch kind {
	case KindText:
		return new(string)
	case KindInt:
		return new(int)
	case KindBool:
		return new(bool)
	case KindAny, KindList, KindDict:
	}

	return nil
}

// takes reports whether dest is the destination that the run must read the keyword into: the Go type of goDestination for a typed keyword, and a
// Starlark value for any other. The check refuses a literal by the same table, so the run cannot read a keyword into another type than the check assumes.
func (p Param) takes(dest any) bool {
	want := goDestination(p.Kind)
	if p.AsValue || want == nil {
		want = new(starlark.Value)
	}

	switch want.(type) {
	case *string:
		return is[*string](dest)
	case *int:
		return is[*int](dest)
	case *bool:
		return is[*bool](dest)
	}

	return is[*starlark.Value](dest)
}

// is reports whether v has the type T.
func is[T any](v any) bool {
	_, ok := v.(T)

	return ok
}

// unpackError is the error of the run for a literal that the Go destination of its keyword cannot take: a None, or a number beyond the range
// of the destination. It is nil for a keyword that the run reads as a value, and for a kind with no Go destination.
func unpackError(param Param, v starlark.Value) error {
	dest := goDestination(param.Kind)
	if param.AsValue || dest == nil {
		return nil
	}

	err := starlark.UnpackArgs("", nil, []starlark.Tuple{{starlark.String(param.Name), v}}, param.Name+"?", dest)
	if err == nil {
		return nil
	}

	// The step name goes in front when the check reports the error.
	return errors.New(strings.TrimPrefix(err.Error(), ": "))
}

// budgetKeyword is the keyword of the agent builtin that caps the dollars of one step.
const budgetKeyword = "max_budget_usd"

// budgetRule is the rule of a keyword that takes a cap in dollars: an int or a float of 0 or more.
func budgetRule(name string) func(starlark.Value) error {
	return func(v starlark.Value) error {
		_, err := Dollars(name, v)

		return err
	}
}

// Dollars reads a cap in dollars: an int or a float of 0 or more, finite.
func Dollars(name string, v starlark.Value) (float64, error) {
	dollars, ok := starlark.AsFloat(v)
	if !ok {
		return 0, fmt.Errorf("%s must be a number of dollars. Write %s = 2.5, or 0 for no cap", name, name)
	}

	return dollars, Budget(name, dollars)
}

func triesRule(v starlark.Value) error {
	tries, _ := starlark.AsInt32(v) //nolint:errcheck // the unpack check has refused a value that the destination cannot take

	return OutputTries(tries)
}

func ownRule(v starlark.Value) error {
	_, _, err := OwnerIdentity(v)

	return err
}

func prsRule(v starlark.Value) error {
	_, err := PullRequests(v)

	return err
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
	maps.Copy(all, valueSignatures())
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
		ruled("idle_timeout", KindText, durationRule("idle_timeout")), ruled(budgetKeyword, KindAny, budgetRule(budgetKeyword)), opt(sandboxKeyword, KindAny), opt(nameKeyword, KindText),
		ruled(RoundsKeyword, KindAny, roundsRule),
	}
}

func roundsRule(v starlark.Value) error {
	_, err := Rounds(v)

	return err
}

// writeParams holds the keywords of the write builtin: the keywords of command that make sense for a program on this machine, and its read-back and undo.
func writeParams() []Param {
	return []Param{
		runParam(), words("read_back"), words("undo"), opt("stdin", KindText), ruled("env", KindDict, envRule),
		ruled("timeout", KindText, durationRule("timeout")), opt(nameKeyword, KindText),
	}
}

// runParam is the required keyword run of a builtin that starts a program.
func runParam() Param {
	return Param{Name: runWord, Kind: KindList, Required: true, Rule: wordsRule(runWord)}
}

// runWord is the keyword that names the program of a call.
const runWord = "run"

// treeKeyword is the keyword of merge that carries the tree that the gate recorded, and the name of that field in a result.
const treeKeyword = "tree"

// processCap is the keyword max_new_processes of command. The run reads it as a value, so a None reaches the check of the hostWithCap pair.
func processCap() Param { return Param{Name: "max_new_processes", Kind: KindInt, AsValue: true} }

// valueSignatures holds the builtins that return text or a struct of facts and start nothing.
func valueSignatures() map[string][]Param {
	return map[string][]Param{
		"env":     {req(nameKeyword, KindText), {Name: "default", Kind: KindText, AsValue: true}},
		"now":     {},
		factsName: {},
	}
}

// stepSignatures holds the builtins that run something.
func stepSignatures() map[string][]Param {
	label := opt(nameKeyword, KindText)

	return map[string][]Param{
		agentName:   agentParams(),
		sandboxName: {ruled("network", KindList, networkRule), ruled("allow_write", KindList, writableRule)},
		commandName: {
			runParam(),
			opt("host", KindText), opt("stdin", KindText), ruled("env", KindDict, envRule),
			ruled("timeout", KindText, durationRule("timeout")), ruled("stop_on_rc", KindList, stopRule), processCap(), label,
		},
		writeName: writeParams(),
		gateName: {
			words(runWord), opt("host", KindText), ruled("timeout", KindText, durationRule("timeout")), opt("head", KindText), label,
		},
		waitName:         waitParams(),
		earlierCallsName: {req("call", KindText), label},
		hostTeardownName: {opt("host", KindText), label},
		hostUpName:       {opt("host", KindText), label},
	}
}

// swarmSignatures holds the builtins that start, wait for and land child runs.
func swarmSignatures() map[string][]Param {
	label := opt(nameKeyword, KindText)

	return map[string][]Param{
		batchMergeName: {
			{Name: "prs", Kind: KindList, Required: true, Rule: prsRule},
			filled("base"), filled("repo"), opt("hold", KindText), ruled("own", KindDict, ownRule),
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
		itemAssignName:  {filled("id"), filled("assignee"), label},
		itemCommentName: {filled("id"), filled("text"), filled("key"), label},
		itemCreateName: {
			filled("title"), filled("key"), opt("body", KindText), opt("parent", KindText), opt("assignee", KindText), opt("severity", KindText), opt("operator", KindBool), label,
		},
		itemStatusName: {filled("id"), {Name: statusField, Kind: KindText, Required: true, Values: StatusValues()}, label},
		boardSyncName:  {req("board", KindInt), filled(statusField), opt("owner", KindText), opt("issue", KindInt), label},
		prAssignName:   {filled("assignee"), opt("linked", KindBool), label},
		prCommentName:  {filled("text"), filled("key"), label},
		prOpenName:     {filled("title"), filled("base"), opt("body", KindText), opt("issue", KindInt), opt("parent", KindText), label},
		preflightName:  {opt("head", KindText), opt("base", KindText), opt("hold", KindText), ruled("own", KindDict, ownRule), opt("id", KindText), label},
		mergeName: {
			req("head", KindText), opt("repo", KindText), opt("base", KindText), opt("hold", KindText), ruled("own", KindDict, ownRule), opt("approved", KindBool),
			{Name: treeKeyword, Kind: KindText, AsValue: true},
			opt("steps", KindList), words("forbid_text"), one("children", ChildrenValues()), opt("merge", KindBool), label,
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
	return slices.Sorted(maps.Keys(signatures()))
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
		if !found || !param.takes(dest) {
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
	case mergeName:
		g.proof(call, params)
	case waitName:
		g.asking(call, params)
	}
}

// proof checks the tree and steps arguments of a merge call with the rules of the run. The run reads them only when the call sets merge to True,
// so the check does the same. An empty list of steps is reported by mergeNeedsSteps, and a value of the wrong kind by the kind check.
func (g *graph) proof(call *syntax.CallExpr, params []Param) {
	if merges, isIdent := paramArg(call, params, "merge").(*syntax.Ident); !isIdent || merges.Name != trueName {
		return
	}

	if tree := paramArg(call, params, treeKeyword); tree != nil && g.proofFits(tree, KindText) {
		_, err := GatedTree(g.literal(tree))
		g.proofError(tree, err)
	}

	if steps := paramArg(call, params, "steps"); steps != nil && !emptyList(steps) && g.proofFits(steps, KindList) {
		g.proofError(steps, GateSteps(g.literal(steps)))
	}
}

// paramArg returns the expression of the keyword of a call, found by the place of the keyword in the signature. It is nil when the call leaves it out.
func paramArg(call *syntax.CallExpr, params []Param, keyword string) syntax.Expr { //nolint:ireturn // the syntax tree of starlark-go holds expressions as an interface
	return argument(call, keyword, slices.IndexFunc(params, func(p Param) bool { return p.Name == keyword }))
}

// proofFits reports whether an argument is a literal of the kind, or None.
func (g *graph) proofFits(arg syntax.Expr, kind Kind) bool {
	value, ok := literalValue(arg)

	return ok && (value == starlark.None || kindOf(arg) == kind)
}

// literal returns the value of an argument that proofFits accepted.
func (g *graph) literal(arg syntax.Expr) starlark.Value { //nolint:ireturn // the value is any of the Starlark types
	value, _ := literalValue(arg)

	return value
}

// proofError reports an error of the run for an argument of a merge call.
func (g *graph) proofError(arg syntax.Expr, err error) {
	if err != nil {
		g.report(placeOf(syntax.Start(arg)), mergeName+": "+err.Error()+".")
	}
}

// hostWithCap checks max_new_processes of a command call with the rule of the run and the real pair of values the run passes it: the value and
// the host. It reports one error for the argument. A value that is not a literal, or a host that is neither absent nor a text literal, is left
// to the run, and so is a value of the wrong kind, which the kind check reports.
func (g *graph) hostWithCap(call *syntax.CallExpr, params []Param) {
	limit := paramArg(call, params, "max_new_processes")
	value, isLiteral := capLiteral(limit)
	host, known := hostText(paramArg(call, params, "host"))

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
	pathsArg := paramArg(call, params, "protect_paths")
	paths, pathsKnown := textList(pathsArg)

	if hasEmptyText(pathsArg) {
		g.reportRule(call, Protection(nil, []string{""}))

		return
	}

	kinds, kindsKnown := textList(paramArg(call, params, "forbid_commands"))
	if pathsKnown && kindsKnown {
		g.pair(call, kinds, paths)
	}
}

// pair reports a list of kinds and a list of paths that do not go together. A list with an unknown kind is left to the check of each kind.
func (g *graph) pair(call *syntax.CallExpr, kinds, paths []string) {
	if slices.ContainsFunc(kinds, func(kind string) bool { return !slices.Contains(ForbidKinds(), kind) }) {
		return
	}

	found := make([]model.FindingKind, len(kinds))
	for i, kind := range kinds {
		found[i] = model.FindingKind(kind)
	}

	g.reportRule(call, Protection(found, paths))
}

// reportRule reports the error of a rule that the run owns, at the call, with a full stop. It reports nothing for a nil error.
func (g *graph) reportRule(call *syntax.CallExpr, err error) {
	if err != nil {
		g.report(placeOf(call.Lparen), err.Error()+".")
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

	merges := paramArg(call, params, "merge")
	if ident, isIdent := merges.(*syntax.Ident); merges == nil || (isIdent && ident.Name == falseName) {
		return
	}

	steps := paramArg(call, params, "steps")
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

	if !g.kindFits(value, got, param) || !g.unpacks(builtin, value, param) {
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
	if v, ok := literalValue(value); ok && v == starlark.None && g.unpacks(builtin, value, param) {
		g.rule(builtin, value, param)
	}
}

// unpacks reports whether the Go destination of the keyword takes the literal, as the run reads it, and reports the error of the run when it does not.
// A value that is not a literal is left to the run.
func (g *graph) unpacks(builtin string, value syntax.Expr, param Param) bool {
	v, ok := literalValue(value)
	if !ok {
		return true
	}

	err := unpackError(param, v)
	if err != nil {
		g.report(placeOf(syntax.Start(value)), builtin+": "+err.Error()+".")
	}

	return err == nil
}

// closedSet reports a literal that is not one of the values of its keyword.
func (g *graph) closedSet(value syntax.Expr, got Kind, param Param) {
	if text, _ := literal(value); got != KindText || !slices.Contains(param.Values, text) {
		g.report(placeOf(syntax.Start(value)), fmt.Sprintf("%s is %s. Use one of: %s.", param.Name, rawText(value), strings.Join(param.Values, ", ")))
	}
}

// rule applies the rule of the run to a value that is a literal, and reports the error of the run with the name of the step. A list, tuple or dict
// with computed members is checked on its literal members, because each rule of a collection judges its members one by one. A value that is
// not a literal, and a collection with no literal member, is left to the run.
func (g *graph) rule(builtin string, value syntax.Expr, param Param) {
	if param.Rule == nil {
		return
	}

	v, ok := literalPart(value)
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
	if !onlyLiterals(expr) {
		return nil, false
	}

	value, err := starlark.EvalExprOptions(model.StarlarkFileOptions(), &starlark.Thread{}, expr, nil)

	return value, err == nil
}

// onlyLiterals is true when every node of the expression is a literal, a minus sign, True, False, None, a paren, or a list, tuple or dict.
// The expression then names no variable and calls no function, so evaluating it runs no code.
func onlyLiterals(expr syntax.Expr) bool {
	only := true

	syntax.Walk(expr, func(node syntax.Node) bool {
		switch n := node.(type) {
		case nil, *syntax.Literal, *syntax.ParenExpr, *syntax.ListExpr, *syntax.TupleExpr, *syntax.DictExpr, *syntax.DictEntry:
		case *syntax.UnaryExpr:
			only = only && n.Op == syntax.MINUS
		case *syntax.Ident:
			only = only && (n.Name == trueName || n.Name == falseName || n.Name == noneName)
		default:
			only = false
		}

		return only
	})

	return only
}

// literalPart is the value of an expression made of literals, or for a list, tuple or dict with computed members, the same collection with only
// its literal members: a dict entry stays when its key and its value are literal, at any depth. ok is false when nothing literal is left.
func literalPart(expr syntax.Expr) (starlark.Value, bool) { //nolint:ireturn // the value is any of the Starlark types
	if v, ok := literalValue(expr); ok {
		return v, true
	}

	if paren, isParen := expr.(*syntax.ParenExpr); isParen {
		return literalPart(paren.X)
	}

	return collectionPart(expr)
}

// collectionPart is literalPart for a list, a tuple or a dict expression.
func collectionPart(expr syntax.Expr) (starlark.Value, bool) { //nolint:ireturn // the value is any of the Starlark types
	switch e := expr.(type) {
	case *syntax.ListExpr:
		items := literalParts(e.List)

		return starlark.NewList(items), len(items) > 0
	case *syntax.TupleExpr:
		items := literalParts(e.List)

		return starlark.Tuple(items), len(items) > 0
	case *syntax.DictExpr:
		dict := literalEntries(e)

		return dict, dict.Len() > 0
	}

	return nil, false
}

// literalParts keeps the members that literalPart reads.
func literalParts(exprs []syntax.Expr) []starlark.Value {
	var items []starlark.Value

	for _, expr := range exprs {
		if item, ok := literalPart(expr); ok {
			items = append(items, item)
		}
	}

	return items
}

// literalEntries keeps the entries of a dict expression whose key is a literal and whose value has a literal part.
func literalEntries(e *syntax.DictExpr) *starlark.Dict {
	dict := starlark.NewDict(len(e.List))

	for _, entry := range e.List {
		pair := entry.(*syntax.DictEntry) //nolint:errcheck,forcetypeassert // the entries of a dict expression are dict entries

		key, keyOK := literalValue(pair.Key)
		val, valOK := literalPart(pair.Value)

		if keyOK && valOK {
			_ = dict.SetKey(key, val) //nolint:errcheck // an entry with an unhashable key is left to the run
		}
	}

	return dict
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
