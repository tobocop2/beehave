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

	"github.com/tobocop2/beehave/internal/merge"
	"github.com/tobocop2/beehave/internal/model"
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
	// Members is true when Rule reads each member of the list as a struct. The check then builds each struct(...) member from its literal fields and
	// leaves a field that is not a literal to the run.
	Members bool
}

// StatusValues lists the values of the status argument of an item_status call.
func StatusValues() []string { return []string{"open", "closed", "hold"} }

// ofKeyword is the keyword of item_read that names the item to read.
const ofKeyword = "of"

// ItemTargets lists the values of the of argument of an item_read call: the item of the run, or its parent.
func ItemTargets() []string { return []string{"self", "parent"} }

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

// forbidText is the forbid_text keyword of a step: a list of RE2 patterns that the text of the step may not match.
func forbidText() Param { return Param{Name: "forbid_text", Kind: KindList, Rule: forbidRule} }

// forbidRule is the rule of forbid_text. The patterns must be text and RE2, the same test that the merge applies when it runs.
func forbidRule(v starlark.Value) error {
	patterns, err := Words("forbid_text", v)
	if err != nil {
		return err
	}

	_, err = merge.Compile(patterns)

	return err
}

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

// skillRule is the rule of the skill keyword of agent: the name of a skill folder. An empty text names no skill.
func skillRule(v starlark.Value) error {
	name, _ := starlark.AsString(v) // the unpack check has refused a value that is not text
	if name == "" {
		return nil
	}

	return model.CheckSkill(name)
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

func contextRule(v starlark.Value) error {
	percent, _ := starlark.AsInt32(v) //nolint:errcheck // the unpack check has refused a value that the destination cannot take

	return ContextPercent(percent)
}

func ownDraftRule(v starlark.Value) error {
	_, err := OwnDraft(v)

	return err
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

// dataRule is the rule of the data keyword of agent.
func dataRule(v starlark.Value) error {
	_, err := DataRecord(v)

	return err
}

// pathsOutsideRule is the rule of the allow keyword of paths_outside.
func pathsOutsideRule(v starlark.Value) error {
	_, err := PathsOutsideAllow(v)

	return err
}

// allowRule is the rule of allow_paths of agent, a list of globs of the agent scope.
func allowRule(v starlark.Value) error {
	_, _, err := AllowList("allow_paths", v)

	return err
}

// The builtins that match text and record no step.
const (
	pathsOutsideName      = "paths_outside"
	closingReferencesName = "closing_references"
	allowWord             = "allow"
)

func one(name string, values []string) Param {
	return Param{Name: name, Kind: KindText, Values: values}
}

// signatures holds the keywords of each builtin that takes keywords, in the order of their positions. The run reads its keywords from here,
// so the check and the run cannot disagree. struct takes any keyword and json is a module, so neither is listed.
func signatures() map[string][]Param {
	all := stepSignatures()
	maps.Copy(all, valueSignatures())
	maps.Copy(all, swarmSignatures())
	maps.Copy(all, eventSignatures())
	maps.Copy(all, itemSignatures())
	maps.Copy(all, readSignatures())
	maps.Copy(all, inboxSignatures())
	maps.Copy(all, streamSignatures())
	maps.Copy(all, roundSignatures())

	return all
}

// agentParams holds the keywords of the agent builtin.
func agentParams() []Param {
	return []Param{
		req("prompt", KindText), req("model", KindText), opt("runner", KindText), ruled("inputs", KindDict, inputsRule), words("outputs"),
		opt("pin", KindText), opt("resume", KindText),
		{Name: "forbid_commands", Kind: KindList, Each: ForbidKinds()},
		ruled("allow_paths", KindList, allowRule), words("protect_paths"), ruled("output_tries", KindInt, triesRule),
		opt("edits", KindBool), opt(LabelKeyword, KindText), ruled("timeout", KindText, durationRule("timeout")),
		ruled("idle_timeout", KindText, durationRule("idle_timeout")), ruled(budgetKeyword, KindAny, budgetRule(budgetKeyword)), ruled("max_context_percent", KindInt, contextRule), opt(sandboxKeyword, KindAny), opt(nameKeyword, KindText),
		ruled(dataKeyword, KindDict, dataRule), ruled("skill", KindText, skillRule),
	}
}

// LabelKeyword is the keyword of the agent builtin that names the call in the ledger and the status line. It is free text and never picks behavior.
const LabelKeyword = "label"

// dataKeyword is the keyword of the agent builtin whose dict the engine writes to the data file that the prompt names as data_file.
const dataKeyword = "data"

// commandParams holds the keywords of the command builtin: the program and where it runs, its stops and caps, and its retry policy.
func commandParams() []Param {
	return []Param{
		runParam(),
		opt("host", KindText), opt("stdin", KindText), ruled(envName, KindDict, envRule),
		ruled("timeout", KindText, durationRule("timeout")), ruled("stop_on_rc", KindList, stopRule), processCap(), slotClass(), opt(sandboxKeyword, KindAny), opt(nameKeyword, KindText),
		ruled(retriesKeyword, KindInt, retriesRule), ruled(backoffKeyword, KindText, backoffRule), one(failureKeyword, FailureModes()),
	}
}

// writeParams holds the keywords of the write builtin: the keywords of command that make sense for a program on this machine, and its read-back and undo.
func writeParams() []Param {
	return []Param{
		runParam(), words("read_back"), words("undo"), opt("stdin", KindText), ruled(envName, KindDict, envRule),
		ruled("timeout", KindText, durationRule("timeout")), opt(nameKeyword, KindText),
		ruled(retriesKeyword, KindInt, retriesRule), ruled(backoffKeyword, KindText, backoffRule),
	}
}

// runParam is the required keyword run of a builtin that starts a program.
func runParam() Param {
	return Param{Name: runWord, Kind: KindList, Required: true, Rule: wordsRule(runWord)}
}

// runWord is the keyword that names the program of a call.
const runWord = "run"

// treeKeyword is the name of the tree field in the result of gate and merge.
const treeKeyword = "tree"

// processCap is the keyword max_new_processes of command. The run reads it as a value, so a None reaches the check of the hostWithCap pair.
func processCap() Param { return Param{Name: "max_new_processes", Kind: KindInt, AsValue: true} }

// slotClass is the keyword slots of command. The run reads it as a value, so an absent keyword differs from an empty text.
func slotClass() Param { return Param{Name: slotsKeyword, Kind: KindText, AsValue: true} }

// valueSignatures holds the builtins that return text or a struct of facts and start nothing.
func valueSignatures() map[string][]Param {
	return map[string][]Param{
		envName:   {{Name: nameKeyword, Kind: KindText, Required: true, Rule: envReadRule}, {Name: "default", Kind: KindText, AsValue: true}},
		"now":     {},
		factsName: {},
		// patched records a marker of DBOS and no step of its own. Its name is the name of the patch.
		patchedName: {{Name: nameKeyword, Kind: KindText, Required: true, Rule: patchNameRule, Why: "The name is the name of the patch, such as \"plan-first\"."}},
		// The two matchers judge text and run no step. They read the dialect of the agent scope and the grammar of the merge guard.
		pathsOutsideName:      {{Name: "allow", Kind: KindList, Required: true, Rule: pathsOutsideRule}, {Name: "paths", Kind: KindList, Required: true, Rule: wordsRule("paths")}},
		closingReferencesName: {req("text", KindText)},
	}
}

// stepSignatures holds the builtins that run something.
func stepSignatures() map[string][]Param {
	label := opt(nameKeyword, KindText)

	return map[string][]Param{
		agentName:   agentParams(),
		sandboxName: {ruled("network", KindList, networkRule)},
		commandName: commandParams(),
		writeName:   writeParams(),
		gateName: {
			words(runWord), opt("host", KindText), ruled("timeout", KindText, durationRule("timeout")), opt("head", KindText), opt("base", KindText), opt("pin_base", KindText), label,
		},
		waitName:         waitParams(),
		sleepName:        {{Name: "duration", Kind: KindText, Required: true, Rule: deadlineRule("duration"), Why: "The duration is how long the run sleeps, such as \"90s\" or \"10m\"."}, label},
		earlierCallsName: {req("call", KindText), label},
		hostTeardownName: {opt("host", KindText), label},
		hostUpName:       {opt("host", KindText), label},
	}
}

// swarmSignatures holds the builtins that start, wait for and land child runs.
func swarmSignatures() map[string][]Param {
	label := opt(nameKeyword, KindText)

	return map[string][]Param{
		mergeName: {
			{Name: "prs", Kind: KindList, Required: true, Rule: prsRule, Members: true},
			filled("base"), filled("repo"), opt("hold", KindText), ruled("own", KindDict, ownRule),
			forbidText(), words("gate_run"), opt("integration", KindInt), opt("hand_off", KindBool),
			{Name: nameKeyword, Kind: KindText, Required: true, Why: "The name makes the step names of the call unique, so use one for each call of the run."},
		},
		gatherName: {
			req("runs", KindAny), one(failKeyword, FailModes()),
			ruled(timeoutKeyword, KindText, deadlineRule(timeoutKeyword)), one(OnTimeoutKeyword, OnTimeoutModes()), label,
		},
		firstOfName: {opt("runs", KindAny), ruled(topicsKeyword, KindList, topicsRule), ruled(timeoutKeyword, KindText, deadlineRule(timeoutKeyword)), label},
		cancelName:  {req("run", KindAny), label},
		spawnName:   {{Name: "items", Kind: KindList, Required: true, Rule: wordsRule("items")}, opt(entryKeyword, KindText), opt("base", KindText), label, ruled(retriesKeyword, KindInt, retriesRule), one(OnOpenKeyword, OnOpenModes())},
		queueName:   {label, opt("hold", KindText), opt("label", KindText)},
	}
}

// readSignatures holds the builtins that read the tracker, the forge or the item folder.
func readSignatures() map[string][]Param {
	return map[string][]Param{
		itemReadName:       {one(ofKeyword, ItemTargets()), opt(nameKeyword, KindText)},
		prStateName:        {req("pr", KindInt), opt(nameKeyword, KindText)},
		prHistoryName:      {opt(nameKeyword, KindText)},
		prReadName:         {opt("id", KindText), opt(nameKeyword, KindText)},
		issueAssigneesName: {opt("repo", KindText), req("number", KindInt), opt(nameKeyword, KindText)},
		itemStopName:       {filled("step"), req("code", KindInt), filled("reason"), opt("id", KindText), opt(nameKeyword, KindText)},
	}
}

// itemSignatures holds the builtins that write the tracker or merge.
func itemSignatures() map[string][]Param {
	label := opt(nameKeyword, KindText)

	return map[string][]Param{
		itemCommentName: {filled("id"), filled("text"), filled("key"), forbidText(), label},
		itemCreateName: {
			filled("title"), filled("key"), opt("body", KindText), opt("parent", KindText), opt("assignee", KindText), opt("severity", KindText), opt("operator", KindBool), forbidText(), opt("shared", KindBool), label,
		},
		itemStatusName: {filled("id"), {Name: statusField, Kind: KindText, Required: true, Values: StatusValues()}, label},
		prCommentName:  {filled("text"), filled("key"), forbidText(), opt("integration", KindBool), label},
		prOpenName:     {filled("title"), filled("base"), opt("body", KindText), opt("issue", KindInt), opt("parent", KindText), forbidText(), opt("integration", KindBool), label},
		preflightName:  {opt("head", KindText), opt("base", KindText), opt("hold", KindText), ruled("own", KindDict, ownRule), opt("id", KindText), {Name: "own_draft", Kind: KindInt, AsValue: true, Rule: ownDraftRule}, label},
		pushName:       {filled("branch"), filled("head"), opt("expect", KindText), label},
	}
}

// TakeSteps lists the steps of pr_take: the notice that asks the author for an answer, and the take after the grace period.
func TakeSteps() []string { return []string{"apply", "take"} }

// roundSignatures holds the builtins that write the forge for a round.
func roundSignatures() map[string][]Param {
	label := opt(nameKeyword, KindText)

	return map[string][]Param{
		roundSyncName:        {filled("base"), filled("trunk"), label},
		roundLandName:        {req("pr", KindInt), filled("head"), filled("base_tip"), label},
		roundCutName:         {filled("old"), filled("new"), req("pr", KindInt), filled("trunk"), label},
		roundIntegrationName: {filled("base"), filled("into"), opt("pr", KindInt), label},
		boardCardName:        {req("board", KindInt), filled("status"), opt("owner", KindText), opt("pr", KindInt), opt("issue", KindInt), label},
		prTakeName:           {req("pr", KindInt), filled("me"), filled("idle"), filled("grace"), {Name: "step", Kind: KindText, Required: true, Values: TakeSteps()}, label},
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
		g.commandPairs(call, params)
	case writeName:
		g.retrying(call, builtin, params)
	case waitName:
		g.asking(call, params)
	case mergeName:
		g.handingOff(call, params)
	case gatherName:
		g.deadlineMode(call, params)
	case firstOfName:
		g.reportRule(call, FirstOfSources(paramArg(call, params, "runs") != nil, paramArg(call, params, topicsKeyword) != nil, paramArg(call, params, timeoutKeyword) != nil))
	}
}

// commandPairs checks the rules of a command call that tie two keywords together.
func (g *graph) commandPairs(call *syntax.CallExpr, params []Param) {
	g.hostWithCap(call, params)
	g.hostWithSlots(call, params)
	g.sandboxPairs(call, params)
	g.retrying(call, commandName, params)
}

// sandboxPairs checks the sandbox keyword of a command call against host, stdin and max_new_processes with the rule of the run. A value that is not a literal is left to the run.
func (g *graph) sandboxPairs(call *syntax.CallExpr, params []Param) {
	box := paramArg(call, params, sandboxKeyword)
	if box == nil || isNone(box) {
		return
	}

	host, _ := hostText(paramArg(call, params, "host"))

	var stdin string
	if arg := paramArg(call, params, "stdin"); arg != nil {
		stdin, _ = literal(arg)
	}

	processes := 0
	if _, isLiteral := capLiteral(paramArg(call, params, "max_new_processes")); isLiteral {
		processes = 1
	}

	if err := SandboxWith(host, stdin, processes); err != nil {
		g.report(placeOf(syntax.Start(box)), commandName+": "+err.Error()+".")
	}
}

// isNone reports whether expr is the name None.
func isNone(expr syntax.Expr) bool {
	name, ok := expr.(*syntax.Ident)

	return ok && name.Name == noneName
}

// deadlineMode refuses a gather call that names on_timeout and no timeout, as the run does.
func (g *graph) deadlineMode(call *syntax.CallExpr, params []Param) {
	mode := paramArg(call, params, OnTimeoutKeyword)

	if err := DeadlineMode(paramArg(call, params, timeoutKeyword) != nil, mode != nil); err != nil {
		g.report(placeOf(syntax.Start(mode)), gatherName+": "+err.Error()+".")
	}
}

// handingOff refuses a keyword of a landing on a merge call whose hand_off is the literal True, as the run does. A flag that only the run knows is left to the run.
func (g *graph) handingOff(call *syntax.CallExpr, params []Param) {
	flag, isName := paramArg(call, params, "hand_off").(*syntax.Ident)
	if !isName || flag.Name != trueName {
		return
	}

	for _, keyword := range HandOffLanding() {
		if paramArg(call, params, keyword) != nil {
			g.reportRule(call, HandOffTakes(keyword))

			return
		}
	}
}

// paramArg returns the expression of the keyword of a call, found by the place of the keyword in the signature. It is nil when the call leaves it out.
func paramArg(call *syntax.CallExpr, params []Param, keyword string) syntax.Expr { //nolint:ireturn // the syntax tree of starlark-go holds expressions as an interface
	return argument(call, keyword, slices.IndexFunc(params, func(p Param) bool { return p.Name == keyword }))
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

// hostWithSlots checks the slots of a command call with the rule of the run. A value that is not a text literal, or a host that is neither absent nor a
// text literal, is left to the run, and so is a value of the wrong kind, which the kind check reports.
func (g *graph) hostWithSlots(call *syntax.CallExpr, params []Param) {
	class := paramArg(call, params, slotsKeyword)
	if class == nil {
		return
	}

	if kind := kindOf(class); kind != KindText && kind != KindAny {
		return
	}

	value, isLiteral := literalValue(class)
	host, known := hostText(paramArg(call, params, "host"))

	if !isLiteral || !known {
		return
	}

	if _, err := SlotClass(value, host); err != nil {
		g.report(placeOf(syntax.Start(class)), commandName+": "+err.Error()+".")
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
	if param.Rule == nil || (param.Name == envName && g.agentKey(builtin, value)) {
		return
	}

	read := literalPart
	if param.Members {
		read = memberList
	}

	v, ok := read(value)
	if !ok {
		return
	}

	if err := param.Rule(v); err != nil {
		g.report(placeOf(syntax.Start(value)), builtin+": "+err.Error()+".")
	}
}

// agentKey reports the env dict that names the variable of the SSH agent as a literal key, whatever its value. It returns true when it reported.
func (g *graph) agentKey(builtin string, value syntax.Expr) bool {
	for paren, isParen := value.(*syntax.ParenExpr); isParen; paren, isParen = value.(*syntax.ParenExpr) {
		value = paren.X
	}

	dict, isDict := value.(*syntax.DictExpr)
	if !isDict {
		return false
	}

	for _, entry := range dict.List {
		pair := entry.(*syntax.DictEntry) //nolint:errcheck,forcetypeassert // the entries of a dict expression are dict entries

		if name, ok := literal(pair.Key); ok && name == model.SSHAgentVar {
			g.report(placeOf(syntax.Start(value)), builtin+": "+agentSocketError(name).Error()+".")

			return true
		}
	}

	return false
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
