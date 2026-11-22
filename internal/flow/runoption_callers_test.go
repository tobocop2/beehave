package flow

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runCall is one call of RunFile or JoinRun in a production file, with the count of its arguments before the options.
type runCall struct {
	where string
	plain int
	args  int
	// empty is true for a RunSandbox call that passes a mode written as the empty text.
	empty bool
}

// emptyMode is true when the one argument of a RunSandbox call is the empty text, written as "" or as model.SandboxMode("").
func emptyMode(args []ast.Expr) bool {
	if len(args) != 1 {
		return false
	}

	if conversion, ok := args[0].(*ast.CallExpr); ok && len(conversion.Args) == 1 {
		return emptyMode(conversion.Args)
	}

	literal, ok := args[0].(*ast.BasicLit)

	return ok && literal.Value == `""`
}

// runCallsIn lists the calls of RunFile and JoinRun in the production files of a folder. RunFile takes (item, project, file), and JoinRun takes a context first.
func runCallsIn(t *testing.T, dir string) []runCall {
	t.Helper()

	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	require.NoError(t, err)

	var calls []runCall

	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}

		parsed, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		require.NoError(t, err)

		ast.Inspect(parsed, func(node ast.Node) bool {
			if call, ok := node.(*ast.CallExpr); ok {
				calls = append(calls, runCallOf(name, call)...)
			}

			return true
		})
	}

	return calls
}

// runCallOf returns the call as a runCall when it calls RunFile or JoinRun, and nothing otherwise.
func runCallOf(name string, call *ast.CallExpr) []runCall {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return nil
	}

	switch selector.Sel.Name {
	case "RunSandbox":
		return []runCall{{where: name + ": RunSandbox", plain: 0, args: len(call.Args), empty: emptyMode(call.Args)}}
	case "RunFile":
		return []runCall{{where: name + ": RunFile", plain: 3, args: len(call.Args)}}
	case "JoinRun":
		return []runCall{{where: name + ": JoinRun", plain: 4, args: len(call.Args)}}
	}

	return nil
}

// The sandbox mode of a run comes from the option of the caller. A caller that leaves the option out records no mode, and the run reads as off.
func TestRunFileAndJoinRun_EveryProductionCallerPassesTheRunOption(t *testing.T) {
	t.Parallel()

	calls := append(runCallsIn(t, "../cli"), runCallsIn(t, "../../cmd/beehave")...)

	var missing, empty []string

	runs := 0

	for _, call := range calls {
		switch {
		case call.empty:
			empty = append(empty, call.where)
		case call.plain == 0:
			// A RunSandbox call is not a run: only its value counts.
		case call.args <= call.plain:
			missing = append(missing, call.where)
		default:
			runs++
		}
	}

	assert.Equal(t, 2, runs, "the run command calls RunFile once and JoinRun once")
	assert.Empty(t, missing, "a caller that leaves the option out starts a run that has no mode")
	assert.Empty(t, empty, "a caller that passes an empty mode starts a run that has no mode")
}
