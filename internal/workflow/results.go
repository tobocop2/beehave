package workflow

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"go.starlark.net/syntax"

	"github.com/tobocop2/beehave/internal/model"
)

// The names of the builtins that the signature table, the table of result fields and the list of builtins all use.
const (
	commandName = "command"
	envName     = "env"
	factsName   = "facts"
	gateName    = "gate"

	earlierCallsName = "earlier_calls"
	patchedName      = "patched"
	writeName        = "write"

	hostTeardownName     = "host_teardown"
	hostUpName           = "host_up"
	reasonField          = "reason"
	stateField           = "state"
	headField            = "head"
	waitName             = "wait"
	sleepName            = "sleep"
	mergeName            = "merge"
	preflightName        = "preflight"
	queueName            = "queue"
	itemCommentName      = "item_comment"
	itemCreateName       = "item_create"
	itemReadName         = "item_read"
	itemStatusName       = "item_status"
	prCommentName        = "pr_comment"
	prOpenName           = "pr_open"
	prStateName          = "pr_state"
	pushName             = "push"
	prHistoryName        = "pr_history"
	prReadName           = "pr_read"
	issueAssigneesName   = "issue_assignees"
	itemStopName         = "item_stop"
	prTakeName           = "pr_take"
	boardCardName        = "board_card"
	roundSyncName        = "round_sync"
	roundLandName        = "round_land"
	roundCutName         = "round_cut"
	roundIntegrationName = "integration_pull"
	revertField          = "revert"
	numberField          = "number"
	statusField          = "status"
	attemptsField        = "attempts"
)

// resultFields holds the fields of the struct that each builtin returns. The run builds the fields of each result in one function per builtin,
// and a test of the run compares the fields of each function with this table, so a field added to one and not to the other fails a test.
// A step that the run recorded before a change still builds its struct from the recorded fields, so the run does not enforce the table.
// env and now return text, struct takes any field and json is a module, so none of them is listed. The fields of an agent that its outputs declare
// come in addition to the fields listed here.
func resultFields() map[string][]string {
	fields := map[string][]string{
		agentName:        {"text", "session", "transcript", "head_sha", "violations", "stop", "cost_usd", "cost_estimated", "cost_unknown", "rewrote", "committed", "skills", "commit_subject", "commit_body"},
		mergeName:        {"merged", "rejected", "deferred", "tip", treeKeyword, "gates", "findings", "note", "unproven", "moved", "host_loss", "host_losses"},
		commandName:      {"stdout", "stderr", "rc", attemptsField, "error"},
		earlierCallsName: {"count"},
		hostTeardownName: {"down", reasonField},
		hostUpName:       {"started", "host"},
		preflightName:    {"ok", "stop", "code", reasonField},
		queueName:        {"rows", "free", "pulls"},
		sleepName:        {"slept"},
		waitName:         {"answer", "answers", "timed_out"},
		writeName:        {"stdout", "stderr", "rc", "key", "skipped", attemptsField},
	}
	for _, group := range []map[string][]string{swarmResultFields(), gateResultFields(), trackerResultFields(), factsResultFields(), inboxResultFields(), streamResultFields(), roundResultFields()} {
		maps.Copy(fields, group)
	}

	return fields
}

// swarmResultFields holds the fields of the builtins that start child runs, wait for them and cancel them.
func swarmResultFields() map[string][]string {
	return map[string][]string{spawnName: {"runs"}, gatherName: {"results"}, cancelName: {CancelledField}, firstOfName: {"kind", "run", "outcome", "runs", "message"}}
}

// gateResultFields holds the fields of the gate builtin.
func gateResultFields() map[string][]string {
	return map[string][]string{
		gateName: {"passed", headField, treeKeyword, "report", "steps", "required", "checks", "new_tests", revertField, "host_loss", "host_losses"},
	}
}

// factsResultFields holds the fields of the struct that facts returns.
func factsResultFields() map[string][]string {
	return map[string][]string{factsName: {"repo", "source", "forge", "tracker", "base_branch", "gate", "gate_steps", "build_host", "vars", "runners"}}
}

// trackerResultFields holds the fields of the builtins that write the tracker, the pull request or its branch, and of pr_state and pr_history, which read pull requests.
func trackerResultFields() map[string][]string {
	return map[string][]string{
		itemCommentName:    {"id"},
		itemCreateName:     {"id", "created"},
		itemReadName:       {"found", "id", "fields", "comments", "parent"},
		itemStatusName:     {"id", statusField},
		prCommentName:      {numberField},
		prOpenName:         {numberField, "created", "stack"},
		prStateName:        {stateField},
		pushName:           {headField},
		prHistoryName:      {"pulls"},
		prReadName:         {"found", numberField, stateField, "draft", "labels", "author", "assignees", "reviewers", "links"},
		itemStopName:       {"recorded"},
		issueAssigneesName: {"assignees"},
	}
}

// roundResultFields holds the fields of the builtins that write the forge for a round: the sync of the trunk, the landing of a sync, the cut of the next round,
// the integration pull request, a project card and the take of a pull request.
func roundResultFields() map[string][]string {
	sync := []string{stateField, "branch", "source", numberField, headField, "base_tip", reasonField}

	return map[string][]string{
		roundSyncName:        sync,
		roundLandName:        sync,
		roundCutName:         {"lines"},
		roundIntegrationName: {numberField, stateField},
		boardCardName:        {statusField},
		prTakeName:           {"code", "lines"},
	}
}

// ResultFields returns the fields that a builtin always returns, and false for a builtin whose result has no fixed fields.
func ResultFields(builtin string) ([]string, bool) {
	fields, ok := resultFields()[builtin]

	return slices.Clone(fields), ok
}

// Results lists the builtins that have result fields.
func Results() []string {
	names := make([]string, 0, len(resultFields()))
	for name := range resultFields() {
		names = append(names, name)
	}

	slices.Sort(names)

	return names
}

// bound is what the check knows about the names that one function binds: how many times each is bound, and the builtin call that
// a name is bound to when it is bound by `name = builtin(...)`.
type bound struct {
	count map[string]int
	calls map[string]*syntax.CallExpr
	item  string
	// shadowed are the names that the file binds at the top level.
	shadowed []string
}

// itemFunctions lists the functions of the workflow file that the run calls with an item: main, finish and each literal entry of a spawn call.
func (g *graph) itemFunctions() []string {
	names := []string{"main", "finish"}
	for _, ref := range g.entries {
		names = append(names, ref.name)
	}

	return names
}

// markItem keeps the name of the first parameter of a function in itemFunctions as the item, when the function binds it once.
func (h *bound) markItem(def *syntax.DefStmt, itemFunctions []string) {
	if len(def.Params) == 0 || !slices.Contains(itemFunctions, def.Name.Name) {
		return
	}

	if id, ok := def.Params[0].(*syntax.Ident); ok && h.count[id.Name] == 1 {
		h.item = id.Name
	}
}

// results checks each `name.field` of a top-level function against the fields of the builtin that the name holds. A name counts only when the
// function binds it once, as `name = builtin(...)`, so a name that is reassigned, a parameter, a loop variable or a value from another call is left to the run.
// A name that a nested function binds again counts as bound twice.
//
// The first parameter of each function in itemFunctions holds the item. It counts under the same rule, and its fields are the item fields of the tracker.
func (g *graph) results(parsed *syntax.File, itemFunctions []string) {
	shadowed := definedNames(parsed)

	for _, stmt := range parsed.Stmts {
		def, ok := stmt.(*syntax.DefStmt)
		if !ok {
			continue
		}

		held := holds(def, shadowed)
		held.markItem(def, itemFunctions)
		g.fields(def, held)
	}
}

// fields checks each `name.field` of a function.
func (g *graph) fields(def *syntax.DefStmt, held bound) {
	syntax.Walk(def, func(n syntax.Node) bool {
		if dot, isDot := n.(*syntax.DotExpr); isDot {
			g.field(dot, held)
		}

		return true
	})
}

// callee is the name of the function that a call names, or the empty text when the call names something else.
func callee(call *syntax.CallExpr) string {
	if fn, ok := call.Fn.(*syntax.Ident); ok {
		return fn.Name
	}

	return ""
}

// holds finds the builtin call that each name of the function holds. A builtin whose name the function or the file binds is not the builtin.
func holds(def *syntax.DefStmt, shadowed []string) bound {
	held := bound{count: map[string]int{}, calls: map[string]*syntax.CallExpr{}, shadowed: shadowed}

	for _, param := range def.Params {
		held.names(param)
	}

	for _, stmt := range def.Body {
		syntax.Walk(stmt, func(n syntax.Node) bool {
			held.bind(n)

			return true
		})
	}

	held.prune(shadowed)

	return held
}

// prune forgets a name that is bound more than once, and a call of a name that the function or the file binds.
func (h bound) prune(shadowed []string) {
	for name, call := range h.calls {
		if h.count[name] != 1 || h.count[callee(call)] > 0 || slices.Contains(shadowed, callee(call)) {
			delete(h.calls, name)
		}
	}
}

// names counts each identifier of an expression as a binding: an assignment target, a loop variable, or a parameter of a def or a lambda.
func (h bound) names(expr syntax.Expr) {
	syntax.Walk(expr, func(n syntax.Node) bool {
		if id, ok := n.(*syntax.Ident); ok {
			h.count[id.Name]++
		}

		return true
	})
}

// bind counts the names that a node binds, and keeps the call of `name = builtin(...)`.
func (h bound) bind(n syntax.Node) {
	switch s := n.(type) {
	case *syntax.AssignStmt:
		h.names(s.LHS)
		h.assigned(s)
	case *syntax.ForStmt:
		h.names(s.Vars)
	case *syntax.ForClause:
		h.names(s.Vars)
	case *syntax.DefStmt:
		h.count[s.Name.Name]++

		for _, param := range s.Params {
			h.names(param)
		}
	case *syntax.LambdaExpr:
		for _, param := range s.Params {
			h.names(param)
		}
	}
}

// assigned keeps the call of `name = builtin(...)`.
func (h bound) assigned(s *syntax.AssignStmt) {
	id, isName := s.LHS.(*syntax.Ident)

	call, isCall := s.RHS.(*syntax.CallExpr)
	if !isName || !isCall || s.Op != syntax.EQ {
		return
	}

	if _, builtin := resultFields()[callee(call)]; builtin {
		h.calls[id.Name] = call
	}
}

// field reports `name.field` when name holds the result of a builtin and the result has no such field. It reports `builtin(...).field` the same way.
func (g *graph) field(dot *syntax.DotExpr, held bound) {
	if call, direct := dot.X.(*syntax.CallExpr); direct {
		g.directField(dot, call, held)

		return
	}

	id, ok := dot.X.(*syntax.Ident)
	if !ok {
		return
	}

	if id.Name == held.item {
		g.itemField(dot)

		return
	}

	if call, found := held.calls[id.Name]; found {
		g.resultField(dot, call)
	}
}

// directField reports `builtin(...).field` when the call names a builtin that the function and the file do not bind.
func (g *graph) directField(dot *syntax.DotExpr, call *syntax.CallExpr, held bound) {
	name := callee(call)
	if _, builtin := resultFields()[name]; builtin && held.count[name] == 0 && !slices.Contains(held.shadowed, name) {
		g.resultField(dot, call)
	}
}

// resultField reports `name.field` when the result of the call has no such field.
func (g *graph) resultField(dot *syntax.DotExpr, call *syntax.CallExpr) {
	fields, known := callFields(call)
	if !known || slices.Contains(fields, dot.Name.Name) {
		return
	}

	g.report(placeOf(dot.Name.NamePos), fmt.Sprintf("%s is not a field of the result of %s. It has: %s.", dot.Name.Name, callee(call), strings.Join(fields, ", ")))
}

// itemField reports `item.name` when the item has no such field.
func (g *graph) itemField(dot *syntax.DotExpr) {
	if fields := model.ItemFields(); !slices.Contains(fields, dot.Name.Name) {
		g.report(placeOf(dot.Name.NamePos), fmt.Sprintf("%s is not a field of the item. It has: %s.", dot.Name.Name, strings.Join(fields, ", ")))
	}
}

// callFields lists the fields of the result of one call. An agent has the fields of its literal outputs as well. It is false for an agent whose
// outputs the check cannot read: a value that is not a list of text literals, or a call that passes *args or **kwargs. The check then leaves
// the fields of that agent to the run.
func callFields(call *syntax.CallExpr) ([]string, bool) {
	fields, _ := ResultFields(callee(call))
	if callee(call) != agentName {
		return fields, true
	}

	declared, known := declaredOutputs(call)

	return append(fields, declared...), known
}

// members returns the members of a list or a tuple, with the parentheses of a tuple removed. It is false for any other expression.
func members(value syntax.Expr) ([]syntax.Expr, bool) {
	for paren, ok := value.(*syntax.ParenExpr); ok; paren, ok = value.(*syntax.ParenExpr) {
		value = paren.X
	}

	switch list := value.(type) {
	case *syntax.ListExpr:
		return list.List, true
	case *syntax.TupleExpr:
		return list.List, true
	}

	return nil, false
}

// declaredOutputs lists the literal names in the outputs of an agent call. It is false when the call passes *args or **kwargs, or when
// outputs is anything but a list or a tuple of text literals.
func declaredOutputs(call *syntax.CallExpr) ([]string, bool) {
	if splat(call) {
		return nil, false
	}

	params, _ := Signature(agentName)
	value := argument(call, "outputs", slices.IndexFunc(params, func(p Param) bool { return p.Name == "outputs" }))

	if value == nil {
		return nil, true
	}

	items, ok := members(value)
	if !ok {
		return nil, false
	}

	return texts(items)
}

// texts reads each expression as a text literal. It is false when one is not.
func texts(items []syntax.Expr) ([]string, bool) {
	var names []string

	for _, item := range items {
		text, isText := literal(item)
		if !isText {
			return nil, false
		}

		names = append(names, text)
	}

	return names, true
}
