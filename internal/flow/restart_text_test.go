package flow

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.starlark.net/syntax"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/model"
)

// restartSteps is the order that starts a stopped item again: a resume replays the recorded stop, so the resume comes first, then the cancel, then the run.
func restartSteps(item string) string {
	return "run beehave resume " + item + ", then beehave cancel " + item + ", then beehave run " + item
}

func assertNamesTheRestartSteps(t *testing.T, err error) {
	t.Helper()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "REFUSE")
	assert.Contains(t, err.Error(), restartSteps(testItem), "the text names resume, then cancel, then run")
	assert.Contains(t, err.Error(), "A plain beehave resume replays this stop", "the text says why a plain resume is not the instruction")
}

func TestRestartText_ACapStopOfALocalQueueOrResearchExampleNamesResumeThenCancelThenRun(t *testing.T) {
	t.Parallel()

	for _, guard := range capGuards {
		if guard.example == "swarm" {
			continue
		}

		t.Run(guard.step, func(t *testing.T) {
			t.Parallel()

			rig, _, file := guardRig(t, guard)

			_, err := rig.h.engine(t).RunFile(testItem, testProject, file)

			assertNamesTheRestartSteps(t, err)
		})
	}
}

func TestRestartText_ACappedPlanOfTheRemediationExampleNamesResumeThenCancelThenRun(t *testing.T) {
	t.Parallel()

	rig, file := capRig(t, func(int) string { return "approve" }, func(r AgentRequest) bool { return r.Model == "opus" }, agent.OutcomeTurnCap)

	_, err := runPipeline(rig.h.engine(t), file)

	assertNamesTheRestartSteps(t, err)
}

func TestRestartText_ACappedImplementOfTheRemediationExampleNamesResumeThenCancelThenRun(t *testing.T) {
	t.Parallel()

	rig, file := capRig(t, func(int) string { return "approve" }, func(r AgentRequest) bool { return r.Model == "sonnet" && r.Resume == "" }, agent.OutcomeTurnCap)

	_, err := runPipeline(rig.h.engine(t), file)

	assertNamesTheRestartSteps(t, err)
}

func TestRestartText_ACappedReviewOfTheReviewLoopNamesResumeThenCancelThenRun(t *testing.T) {
	t.Parallel()

	rig, file := capRig(t, func(int) string { return "changes" }, nthCall(4), agent.OutcomeBudgetCap)

	_, err := runPipeline(rig.h.engine(t), file)

	assertNamesTheRestartSteps(t, err)
}

func TestRestartText_ACappedSettleOfTheRemediationExampleNamesResumeThenCancelThenRun(t *testing.T) {
	t.Parallel()

	settle := 2 + len(capModels(2)) + 1
	rig, file := capRig(t, func(int) string { return "changes" }, nthCall(settle+1), agent.OutcomeTurnCap) // the sweep is one more call than capModels lists
	rig.setRound("fix_rounds", oneFixRound)
	engine := rig.h.engine(t)
	done := pipelineInBackground(engine, file)

	waitOn(t, ownerAsks(t, rig.h))
	require.NoError(t, rig.h.client(t, testVersion).Answer(testItem, "park all"))
	outcome := receive(t, done)

	assertNamesTheRestartSteps(t, outcome.err)
}

// restartSentence is the sentence that ends a replayed stop text. ITEM stands for each item id.
const restartSentence = "A plain beehave resume replays this stop. To start the item again, run beehave resume ITEM, then beehave cancel ITEM, then beehave run ITEM."

// restartSites are the workflow files of the examples and of the library that the scan reads.
var restartSites = []string{"../../examples", "../workflow/lib"}

// stopText is the text that one fail() call of a workflow file builds.
type stopText struct {
	line int
	text string
	// waitTimeout is true when the call sits in the body of an `if <wait result>.timed_out` branch.
	waitTimeout bool
}

// renderText builds the string that an expression gives, from the syntax tree. A value that is not a string literal shows as ITEM, and a call of restart_steps shows as the shared sentence.
func renderText(expr syntax.Expr) string {
	switch node := expr.(type) {
	case *syntax.Literal:
		if text, ok := node.Value.(string); ok {
			return text
		}
	case *syntax.ParenExpr:
		return renderText(node.X)
	case *syntax.CallExpr:
		if name, ok := node.Fn.(*syntax.Ident); ok && name.Name == "restart_steps" {
			return restartSentence
		}
	case *syntax.BinaryExpr:
		if node.Op == syntax.PLUS {
			return renderText(node.X) + renderText(node.Y)
		}

		if node.Op == syntax.PERCENT {
			return formatText(renderText(node.X), node.Y)
		}
	}

	return "ITEM"
}

// formatText fills the verbs of a format string with the rendered operands of a `%` expression.
func formatText(format string, operands syntax.Expr) string {
	for {
		paren, ok := operands.(*syntax.ParenExpr)
		if !ok {
			break
		}

		operands = paren.X
	}

	list := []syntax.Expr{operands}
	if tuple, ok := operands.(*syntax.TupleExpr); ok {
		list = tuple.List
	}

	var out strings.Builder

	next := 0

	for i := 0; i < len(format); i++ {
		if format[i] != '%' || i+1 == len(format) {
			out.WriteByte(format[i])

			continue
		}

		i++

		if format[i] == '%' || next == len(list) {
			out.WriteByte('%')

			continue
		}

		out.WriteString(renderText(list[next]))

		next++
	}

	return out.String()
}

// stopTextsOf lists the fail() calls of a workflow file whose text names a resume. It reads the syntax tree, so a comment never counts and a text that is split over lines counts whole.
func stopTextsOf(t *testing.T, name, source string) []stopText {
	t.Helper()

	parsed, err := model.StarlarkFileOptions().Parse(name, source, 0)
	require.NoError(t, err)

	var (
		found []stopText
		stack []syntax.Node
	)

	syntax.Walk(parsed, func(node syntax.Node) bool {
		if node == nil {
			stack = stack[:len(stack)-1]

			return true
		}

		stack = append(stack, node)

		call, isCall := node.(*syntax.CallExpr)
		if !isCall {
			return true
		}

		if fn, ok := call.Fn.(*syntax.Ident); !ok || fn.Name != "fail" || len(call.Args) == 0 {
			return true
		}

		text := renderText(call.Args[0])
		if strings.Contains(strings.ToLower(text), "beehave resume") {
			found = append(found, stopText{line: int(call.Lparen.Line), text: text, waitTimeout: inTimedOutBranch(stack)})
		}

		return true
	})

	return found
}

// inTimedOutBranch says whether the innermost node of the stack is in the body of an `if <wait result>.timed_out` branch. A resume does not replay that stop: the wait asks again (the test research-desk-recover shows it).
func inTimedOutBranch(stack []syntax.Node) bool {
	for _, node := range stack {
		branch, ok := node.(*syntax.IfStmt)
		if !ok {
			continue
		}

		if dot, isDot := branch.Cond.(*syntax.DotExpr); isDot && dot.Name.Name == "timed_out" {
			return true
		}
	}

	return false
}

func TestRestartText_AStopTextInASourceFileIsReadFromTheSyntaxTreeNotFromItsLines(t *testing.T) {
	t.Parallel()

	comment := "# REFUSE texts say: run beehave resume %s, then run again.\ndef main(item):\n    pass\n"
	assert.Empty(t, stopTextsOf(t, "comment.star", comment), "a comment is not a stop text")

	split := "def main(item):\n    fail(\"REFUSE: the step stopped. \" +\n         \"Run beehave resume %s.\" % item.id)\n"
	stops := stopTextsOf(t, "split.star", split)
	require.Len(t, stops, 1, "a text that is split over two lines is one stop text")
	assert.Equal(t, "REFUSE: the step stopped. Run beehave resume ITEM.", stops[0].text)
	assert.NotContains(t, stops[0].text, restartSentence)

	whole := "def main(item):\n    fail((\"REFUSE: x. \" +\n         \"A plain beehave resume replays this stop. To start the item again, run beehave resume %s, \" +\n         \"then beehave cancel %s, then beehave run %s.\") % (item.id, item.id, item.id))\n"
	stops = stopTextsOf(t, "whole.star", whole)
	require.Len(t, stops, 1)
	assert.Contains(t, stops[0].text, restartSentence, "the sentence is found across the split")

	helper := "def main(item):\n    fail(\"REFUSE: x. %s\" % restart_steps(item))\n"
	stops = stopTextsOf(t, "helper.star", helper)
	require.Len(t, stops, 1)
	assert.Contains(t, stops[0].text, restartSentence, "a call of the shared helper gives the sentence")
}

func TestRestartText_OnlyAStopRaisedOnTheTimedOutFieldOfAWaitResultIsLeftAlone(t *testing.T) {
	t.Parallel()

	waited := "def main(item):\n    ruling = wait(\"q\", name = \"n\", timeout = \"1s\")\n    if ruling.timed_out:\n        fail(\"REFUSE: no answer. Run beehave resume.\")\n    fail(\"REFUSE: other. Run beehave resume.\")\n"
	stops := stopTextsOf(t, "wait.star", waited)

	require.Len(t, stops, 2)
	assert.True(t, stops[0].waitTimeout, "the stop in the timed_out branch is the wait timeout")
	assert.False(t, stops[1].waitTimeout, "a stop outside the branch is not")
}

func TestRestartText_TheLibraryHelperReturnsTheSharedSentence(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile("../workflow/lib/review_fix.star")
	require.NoError(t, err)

	parsed, err := model.StarlarkFileOptions().Parse("review_fix.star", body, 0)
	require.NoError(t, err)

	rendered := ""

	for _, stmt := range parsed.Stmts {
		def, ok := stmt.(*syntax.DefStmt)
		if !ok || def.Name.Name != "restart_steps" {
			continue
		}

		for _, inner := range def.Body {
			if returned, isReturn := inner.(*syntax.ReturnStmt); isReturn {
				rendered = renderText(returned.Result)
			}
		}
	}

	assert.Equal(t, restartSentence, rendered)
}

func TestRestartText_EveryStopTextInTheExamplesAndTheLibraryThatNamesAResumeHoldsTheSharedSentence(t *testing.T) {
	t.Parallel()

	var timeouts []string

	scanned, stops := 0, 0

	for _, root := range restartSites {
		require.NoError(t, filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			require.NoError(t, err)

			if entry.IsDir() || !strings.HasSuffix(path, ".star") {
				return nil
			}

			body, readErr := os.ReadFile(path) //nolint:gosec // the test reads the example files of the repository
			require.NoError(t, readErr)

			scanned++

			for _, stop := range stopTextsOf(t, path, string(body)) {
				if stop.waitTimeout {
					timeouts = append(timeouts, path)

					continue
				}

				stops++

				assert.Contains(t, stop.text, restartSentence, "%s:%d: the stop text names resume, then cancel, then run", path, stop.line)
			}

			return nil
		}))
	}

	assert.Greater(t, scanned, 10, "the scan reads the workflow files")
	assert.Greater(t, stops, 8, "the scan finds the stop texts")
	assert.Equal(t, []string{"../../examples/ticket-pipeline/partition.star"}, timeouts, "one wait timeout stop is left alone, in the ticket pipeline")
}
