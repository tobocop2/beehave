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
	case "RunFile":
		return []runCall{{where: name + ": RunFile", plain: 3, args: len(call.Args)}}
	case "JoinRun":
		return []runCall{{where: name + ": JoinRun", plain: 4, args: len(call.Args)}}
	}

	return nil
}

// The start of a run refuses an empty sandbox mode, so this scan only names a production caller that leaves the option out before a run finds it.
func TestRunFileAndJoinRun_EveryProductionCallerPassesTheRunOption(t *testing.T) {
	t.Parallel()

	calls := append(runCallsIn(t, "../cli"), runCallsIn(t, "../../cmd/beehest")...)

	var missing []string

	runs := 0

	for _, call := range calls {
		if call.args <= call.plain {
			missing = append(missing, call.where)

			continue
		}

		runs++
	}

	assert.Equal(t, 2, runs, "the run command calls RunFile once and JoinRun once")
	assert.Empty(t, missing, "a caller that leaves the option out starts no run: the start refuses an empty mode")
}
