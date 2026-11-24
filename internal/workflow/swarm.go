package workflow

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

const (
	agentName    = "agent"
	spawnName    = "spawn"
	gatherName   = "gather"
	cancelName   = "cancel"
	entryKeyword = "entry"
	failKeyword  = "fail"
	nameKeyword  = "name"
	// firstOfName is the name of the builtin that waits for the first of a run that ended, a message and a timer.
	firstOfName = "first_of"
	// topicsKeyword is the keyword of a first_of call that lists the topics to wait for.
	topicsKeyword = "topics"
	// timeoutKeyword is the keyword of a gather call that sets the deadline of the group.
	timeoutKeyword = "timeout"
	// CancelledField is the one field of the struct that cancel returns.
	CancelledField = "cancelled" //nolint:misspell // the field name is part of the workflow API
	// OnTimeoutKeyword is the keyword of a gather call that says what the deadline does with the runs that are still open.
	OnTimeoutKeyword = "on_timeout"
	// secondArgument is the position of entry in a spawn call and of fail in a gather call.
	secondArgument = 1
)

// OnTimeout is what a gather call does at its deadline with the runs that are still open.
type OnTimeout string

const (
	// OnTimeoutCancel cancels each open run, and its result reads ok False with the error "deadline".
	OnTimeoutCancel OnTimeout = "cancel"
	// OnTimeoutReturn leaves each open run running, and its result reads open True.
	OnTimeoutReturn OnTimeout = "return"
)

// FailModes lists the values of the fail argument of a gather call.
func FailModes() []string { return []string{"return", "raise"} }

// OnTimeoutModes lists the values of the on_timeout argument of a gather call.
func OnTimeoutModes() []string { return []string{string(OnTimeoutCancel), string(OnTimeoutReturn)} }

// GroupDeadline reads the timeout and on_timeout of a gather call. A call with no timeout has no deadline, and a timeout with no on_timeout cancels.
func GroupDeadline(timeout, onTimeout string) (time.Duration, OnTimeout, error) {
	if err := DeadlineMode(timeout != "", onTimeout != ""); err != nil {
		return 0, "", err
	}

	if timeout == "" {
		return 0, "", nil
	}

	wait, err := Deadline(timeoutKeyword, timeout)
	if err != nil {
		return 0, "", err
	}

	mode := cmp.Or(OnTimeout(onTimeout), OnTimeoutCancel)
	if !slices.Contains(OnTimeoutModes(), string(mode)) {
		return 0, "", fmt.Errorf("on_timeout is %q. Use \"cancel\" or \"return\"", onTimeout)
	}

	return wait, mode, nil
}

// DeadlineMode is the rule that ties on_timeout to timeout in a gather call: on_timeout needs a timeout.
func DeadlineMode(hasTimeout, hasMode bool) error {
	if hasMode && !hasTimeout {
		return errors.New("on_timeout needs a timeout. Add timeout = \"10m\" to the call, or leave on_timeout out")
	}

	return nil
}

// FirstKind is what ended a first_of call: a run that ended, a message of a topic, or the timer.
type FirstKind string

const (
	// FirstRun is the kind of a call that a run ended.
	FirstRun FirstKind = "run"
	// FirstMessage is the kind of a call that a message of a listed topic ended.
	FirstMessage FirstKind = "message"
	// FirstTimer is the kind of a call that its timeout ended.
	FirstTimer FirstKind = "timer"
)

// FirstOfSources is the rule that a first_of call waits for something: runs, topics or a timeout.
func FirstOfSources(hasRuns, hasTopics, hasTimeout bool) error {
	if hasRuns || hasTopics || hasTimeout {
		return nil
	}

	return errors.New("first_of needs something to wait for. Add runs, topics or timeout to the call")
}

// Topics reads the topics keyword of a first_of call: a list of topic names that a user may send on. None and an absent keyword are empty.
func Topics(v starlark.Value) ([]string, error) {
	topics, err := Words(topicsKeyword, v)
	if err != nil {
		return nil, err
	}

	for _, topic := range topics {
		if err := MessageName(topicKeyword, topic); err != nil {
			return nil, err
		}
	}

	return topics, nil
}

// entryRef is the literal entry of a spawn call and where it stands. The function it names is looked up in the workflow file after the scan.
type entryRef struct {
	at   Diagnostic
	name string
}

// swarm checks every call of a builtin in a parsed file against the signature of the builtin, and keeps the literal entry of each spawn call
// for the check against the functions of the workflow file. A name that the file defines or loads is not the builtin, so its calls are left alone.
func (g *graph) swarm(parsed *syntax.File) {
	shadowed := definedNames(parsed)

	syntax.Walk(parsed, func(n syntax.Node) bool {
		if call, ok := n.(*syntax.CallExpr); ok {
			g.builtinCall(call, shadowed)
		}

		return true
	})
}

// builtinCall checks one call when it calls a builtin by name. The arguments are listed in the order of their positions, and a call may name only these.
func (g *graph) builtinCall(call *syntax.CallExpr, shadowed []string) {
	fn, isName := call.Fn.(*syntax.Ident)
	if !isName || slices.Contains(shadowed, fn.Name) {
		return
	}

	params, signed := Signature(fn.Name)
	if !signed {
		return
	}

	g.signature(call, fn.Name, params)

	if fn.Name == spawnName {
		g.entry(call)
	}
}

// keywords reports each keyword of the call that the builtin does not take, at the place of the keyword.
func (g *graph) keywords(call *syntax.CallExpr, builtin string, allowed []string) {
	for _, arg := range call.Args {
		pair, isPair := arg.(*syntax.BinaryExpr)
		if !isPair || pair.Op != syntax.EQ {
			continue
		}

		key, isName := pair.X.(*syntax.Ident)
		if !isName || slices.Contains(allowed, key.Name) {
			continue
		}

		g.report(placeOf(key.NamePos), unknownKeyword(builtin, key.Name, allowed))
	}
}

// unknownKeyword is the sentence for a keyword that the builtin does not take. A keyword that is an option of another command says where it belongs.
func unknownKeyword(builtin, keyword string, allowed []string) string {
	switch {
	case builtin == sandboxName && keyword == skillsField:
		return "sandbox takes no argument skills. The prompt declares the skills of a step with the template function skill, such as {{ skill \"review\" }}."
	case builtin == spawnName && keyword == DebounceKeyword:
		return "spawn takes no argument debounce. debounce is an option of beehave run."
	case removedPRArgument(builtin, keyword) != "":
		return removedPRArgument(builtin, keyword)
	case builtin == preflightName && slices.Contains(ClaimKeywords(), keyword):
		return PreflightClaimRefusal(keyword)
	case len(allowed) == 0:
		return fmt.Sprintf("%s takes no argument, and the call gives %s.", builtin, keyword)
	}

	return fmt.Sprintf("%s takes no argument %s. Use one of: %s.", builtin, keyword, strings.Join(allowed, ", "))
}

// removedPRArgument is the sentence for an argument that pr_open or pr_comment no longer takes. It is empty for any other keyword.
func removedPRArgument(builtin, keyword string) string {
	switch {
	case (builtin == prOpenName || builtin == prCommentName) && keyword == "integration":
		return builtin + " takes no argument integration. Pass the closing keywords in forbid_text for a pull request that may not close an issue."
	case builtin == prOpenName && keyword == "issue":
		return "pr_open takes no argument issue. Write the closing line in body, such as Closes #12."
	}

	return ""
}

// argument returns the expression of a keyword or of the positional argument at index. It is nil when the call leaves the argument out.
func argument(call *syntax.CallExpr, keyword string, index int) syntax.Expr { //nolint:ireturn // the syntax tree of starlark-go holds expressions as an interface
	for _, arg := range call.Args {
		if pair, isPair := arg.(*syntax.BinaryExpr); isPair && pair.Op == syntax.EQ {
			if key, isName := pair.X.(*syntax.Ident); isName && key.Name == keyword {
				return pair.Y
			}
		}
	}

	if index < len(call.Args) {
		if pair, isPair := call.Args[index].(*syntax.BinaryExpr); !isPair || pair.Op != syntax.EQ {
			return call.Args[index]
		}
	}

	return nil
}

// literal returns the text of a string literal. ok is false for any other expression.
func literal(expr syntax.Expr) (text string, ok bool) {
	lit, isLit := expr.(*syntax.Literal)
	if !isLit {
		return "", false
	}

	text, ok = lit.Value.(string)

	return text, ok
}

// entry keeps the entry of a spawn call when it is a string literal. A computed entry is left to the run.
func (g *graph) entry(call *syntax.CallExpr) {
	value := argument(call, entryKeyword, secondArgument)
	if value == nil {
		return
	}

	if name, ok := literal(value); ok {
		g.entries = append(g.entries, entryRef{at: placeOf(syntax.Start(value)), name: name})
	}
}

// definedNames lists the names that a file binds at the top level with def or load: the functions that spawn can name.
func definedNames(parsed *syntax.File) []string {
	var names []string

	for _, stmt := range parsed.Stmts {
		switch s := stmt.(type) {
		case *syntax.DefStmt:
			names = append(names, s.Name.Name)
		case *syntax.LoadStmt:
			for _, bound := range s.To {
				names = append(names, bound.Name)
			}
		}
	}

	return names
}

// entriesDefined reports each literal entry that names no function of the workflow file. The run starts the entry in that file, so a name in a loaded file counts only when the file loads it.
func (g *graph) entriesDefined() {
	if g.unparsed {
		return
	}

	for _, ref := range g.entries {
		if !slices.Contains(g.mainNames, ref.name) {
			g.report(ref.at, fmt.Sprintf("entry is %q, and %s defines no function of that name. Add def %s(item):", ref.name, g.main, ref.name))
		}
	}
}
