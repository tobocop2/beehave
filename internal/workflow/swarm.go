package workflow

import (
	"fmt"
	"slices"
	"strings"

	"go.starlark.net/syntax"
)

const (
	spawnName      = "spawn"
	gatherName     = "gather"
	batchMergeName = "batch_merge"
	entryKeyword   = "entry"
	failKeyword    = "fail"
	nameKeyword    = "name"
	// secondArgument is the position of entry in a spawn call and of fail in a gather call.
	secondArgument = 1
)

// FailModes lists the values of the fail argument of a gather call.
func FailModes() []string { return []string{"return", "raise"} }

// entryRef is the literal entry of a spawn call and where it stands. The function it names is looked up in the workflow file after the scan.
type entryRef struct {
	at   Diagnostic
	name string
}

// swarm checks every spawn and gather call in a parsed file: a keyword that the call does not take, a fail value that is not listed,
// and a literal entry, which it keeps for the check against the functions of the workflow file.
func (g *graph) swarm(parsed *syntax.File) {
	syntax.Walk(parsed, func(n syntax.Node) bool {
		if call, ok := n.(*syntax.CallExpr); ok {
			g.swarmCall(call)
		}

		return true
	})
}

// swarmCall checks one call when it calls spawn or gather by name. The arguments are listed in the order of their positions, and a call may name only these.
func (g *graph) swarmCall(call *syntax.CallExpr) {
	fn, isName := call.Fn.(*syntax.Ident)
	if !isName {
		return
	}

	switch fn.Name {
	case spawnName:
		g.keywords(call, spawnName, []string{"items", "entry", nameKeyword})
		g.entry(call)
	case gatherName:
		g.keywords(call, gatherName, []string{"runs", "fail", nameKeyword})
		g.failMode(call)
	case batchMergeName:
		g.keywords(call, batchMergeName, []string{"prs", "base", "repo", "hold", "own", "forbid_text", "gate_run", nameKeyword})
		g.batchName(call)
	}
}

// batchName reports a batch_merge call with no `name` keyword. The name makes the step names of the call unique, and the run refuses the call without it.
func (g *graph) batchName(call *syntax.CallExpr) {
	if argument(call, nameKeyword, len(call.Args)) == nil {
		g.report(placeOf(call.Lparen), batchMergeName+" needs a `name` keyword. Add name = \"...\", one for each call of the run.")
	}
}

// keywords reports each keyword of the call that the builtin does not take, at the place of the keyword.
func (g *graph) keywords(call *syntax.CallExpr, builtin string, allowed []string) {
	for _, arg := range call.Args {
		pair, isPair := arg.(*syntax.BinaryExpr)
		if !isPair || pair.Op != syntax.EQ {
			continue
		}

		if key, isName := pair.X.(*syntax.Ident); isName && !slices.Contains(allowed, key.Name) {
			g.report(placeOf(key.NamePos), fmt.Sprintf("%s takes no argument %s. Use one of: %s.", builtin, key.Name, strings.Join(allowed, ", ")))
		}
	}
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
		if _, isPair := call.Args[index].(*syntax.BinaryExpr); !isPair {
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

// failMode reports a fail value that is a literal and is not listed.
func (g *graph) failMode(call *syntax.CallExpr) {
	value := argument(call, failKeyword, secondArgument)
	if value == nil {
		return
	}

	if mode, ok := literal(value); ok && !slices.Contains(FailModes(), mode) {
		g.report(placeOf(syntax.Start(value)), fmt.Sprintf("fail is %q. Use one of: %s.", mode, strings.Join(FailModes(), ", ")))
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
