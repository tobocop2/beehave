// Package workflowtest reads the builtin calls of a workflow file for tests that check an example against its tools.
package workflowtest

import (
	"slices"

	"go.starlark.net/syntax"

	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/workflow"
)

// Call is one call of a builtin in a workflow file, with the argument expressions by keyword.
type Call struct {
	Builtin string
	Args    map[string]syntax.Expr
}

// Calls lists the calls of the builtins in the text of a workflow file, in the order of the text. It reads the syntax only and runs nothing.
func Calls(name string, text []byte) ([]Call, error) {
	parsed, err := model.StarlarkFileOptions().Parse(name, text, 0)
	if err != nil {
		return nil, err
	}

	var calls []Call

	syntax.Walk(parsed, func(n syntax.Node) bool {
		if call, ok := n.(*syntax.CallExpr); ok {
			calls = appendBuiltin(calls, call)
		}

		return true
	})

	return calls, nil
}

// Constant returns the string literals of the top-level assignment `name = ...` of a workflow file. A name that the file does not assign has none.
func Constant(file string, text []byte, name string) ([]string, error) {
	parsed, err := model.StarlarkFileOptions().Parse(file, text, 0)
	if err != nil {
		return nil, err
	}

	var out []string

	for _, stmt := range parsed.Stmts {
		if assign, ok := stmt.(*syntax.AssignStmt); ok && assign.Op == syntax.EQ {
			if target, isName := assign.LHS.(*syntax.Ident); isName && target.Name == name {
				out = append(out, Literals(assign.RHS)...)
			}
		}
	}

	return out, nil
}

// appendBuiltin adds the call when it calls a builtin by its name.
func appendBuiltin(calls []Call, call *syntax.CallExpr) []Call {
	fn, ok := call.Fn.(*syntax.Ident)
	if !ok || !slices.Contains(workflow.Builtins(), fn.Name) {
		return calls
	}

	found := Call{Builtin: fn.Name, Args: map[string]syntax.Expr{}}

	for _, arg := range call.Args {
		if pair, isPair := arg.(*syntax.BinaryExpr); isPair && pair.Op == syntax.EQ {
			if key, isName := pair.X.(*syntax.Ident); isName {
				found.Args[key.Name] = pair.Y
			}
		}
	}

	return append(calls, found)
}

// Literals returns every string literal inside an expression, in the order of the text. A nil expression has none.
func Literals(expr syntax.Expr) []string {
	var out []string

	if expr == nil {
		return out
	}

	syntax.Walk(expr, func(n syntax.Node) bool {
		if lit, ok := n.(*syntax.Literal); ok {
			if text, isText := lit.Value.(string); isText {
				out = append(out, text)
			}
		}

		return true
	})

	return out
}
