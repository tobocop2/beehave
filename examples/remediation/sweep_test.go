package remediation_test

import (
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.starlark.net/syntax"

	"github.com/tobocop2/beehest/internal/workflow"
)

// exprText writes the source form of the expressions that the workflow uses for a keyword value: names, fields, indexes, calls and text.
func exprText(expr syntax.Expr) string {
	switch e := expr.(type) {
	case *syntax.Ident:
		return e.Name
	case *syntax.Literal:
		if text, ok := e.Value.(string); ok {
			return strconv.Quote(text)
		}

		return e.Raw
	case *syntax.DotExpr:
		return exprText(e.X) + "." + e.Name.Name
	case *syntax.IndexExpr:
		return exprText(e.X) + "[" + exprText(e.Y) + "]"
	case *syntax.CallExpr:
		args := make([]string, len(e.Args))
		for i, arg := range e.Args {
			args[i] = exprText(arg)
		}

		return exprText(e.Fn) + "(" + strings.Join(args, ", ") + ")"
	case *syntax.BinaryExpr:
		return exprText(e.X) + " " + e.Op.String() + " " + exprText(e.Y)
	}

	return "?"
}

// agentCalls returns the keywords of each agent call of the workflow file, in the order of the text.
func agentCalls(t *testing.T) []map[string]syntax.Expr {
	t.Helper()

	raw, err := os.ReadFile("workflow.star")
	require.NoError(t, err)

	file, err := (&syntax.FileOptions{}).Parse("workflow.star", raw, 0)
	require.NoError(t, err)

	var calls []map[string]syntax.Expr

	syntax.Walk(file, func(n syntax.Node) bool {
		if call, ok := n.(*syntax.CallExpr); ok {
			if fn, isName := call.Fn.(*syntax.Ident); isName && fn.Name == "agent" {
				calls = append(calls, keywordsOf(call))
			}
		}

		return true
	})

	return calls
}

// namedCall returns the keywords of the agent call whose name keyword is the text, and the position of the call among the agent calls.
func namedCall(t *testing.T, name string) (map[string]syntax.Expr, int) {
	t.Helper()

	for i, call := range agentCalls(t) {
		if got, ok := call["name"]; ok && exprText(got) == strconv.Quote(name) {
			return call, i
		}
	}

	require.Failf(t, "no agent call", "the workflow has no agent call named %s", name)

	return nil, 0
}

func TestExample_TheReviewSweepIsAStepThatLoadsTheReviewSkill(t *testing.T) {
	t.Parallel()

	implement, at := namedCall(t, "implement")
	sweep, sweepAt := namedCall(t, "implement-sweep")

	assert.Equal(t, at+1, sweepAt, "the sweep is the agent call right after implement")
	assert.Equal(t, `project.vars["review_skill"]`, exprText(sweep["skill"]), "the sweep starts with the review skill that the project names")
	assert.Equal(t, `"prompts/sweep.md"`, exprText(sweep["prompt"]))
	assert.Equal(t, "implement.session", exprText(sweep["resume"]), "the sweep continues the session of the implement step")
	assert.Equal(t, exprText(implement["runner"]), exprText(sweep["runner"]), "a session belongs to one runner")
	assert.Equal(t, exprText(implement["sandbox"]), exprText(sweep["sandbox"]), "the sweep has the sandbox of the implement step")
	assert.NotContains(t, sweep, "edits", "the sweep may edit: it commits each fix")
	assert.Empty(t, workflow.RequiredSkills("workflow.star"), "no check of the workflow rests on an agent calling a skill")
	assert.Empty(t, workflow.RequiredSkillVars("workflow.star"))
}
