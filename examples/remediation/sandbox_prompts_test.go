package remediation_test

import (
	"bytes"
	"io/fs"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"text/template"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.starlark.net/syntax"
)

// ghAsk matches a sentence that names a gh command or the forge tool. sentence cuts a text into sentences.
// prohibition matches one that starts with a prohibition; mergeNote matches one that says how the orchestrator merges.
var (
	ghAsk       = regexp.MustCompile(`\bgh (pr|issue|api|search|repo|label|run|release|auth|workflow)\b|(?i)\bwith the forge tool\b|\bwith gh\b`)
	sentence    = regexp.MustCompile(`[^.!?\n]+[.!?]?`)
	prohibition = regexp.MustCompile(`(?i)^\W*(never|do not)\b`)
	mergeNote   = regexp.MustCompile(`(?i)^\W*(the orchestrator merges|a merge is)\b`)
)

// forbids is true when the sentence forbids a gh command, or says how the orchestrator merges and names no gh command but `gh pr merge`.
func forbids(s string) bool {
	return prohibition.MatchString(s) || (mergeNote.MatchString(s) && !ghAsk.MatchString(strings.ReplaceAll(s, "gh pr merge", "")))
}

// sandboxedPrompts reads the workflow file with the Starlark parser and returns the sandbox expression of each agent call that sets the keyword sandbox, by its prompt.
func sandboxedPrompts(t *testing.T) map[string]syntax.Expr {
	t.Helper()

	file, err := (&syntax.FileOptions{}).Parse("workflow.star", nil, 0)
	require.NoError(t, err)

	prompts := map[string]syntax.Expr{}

	syntax.Walk(file, func(n syntax.Node) bool {
		if call, ok := n.(*syntax.CallExpr); ok { // the walk visits every node; only calls are agent steps
			if path, box := sandboxedPrompt(t, call); box != nil {
				require.NotContains(t, prompts, path, "each sandboxed call has its own prompt, so no call hides behind another")
				prompts[path] = box
			}
		}

		return true
	})

	return prompts
}

// keywordsOf returns the keyword arguments of a call by name.
func keywordsOf(call *syntax.CallExpr) map[string]syntax.Expr {
	keywords := map[string]syntax.Expr{}

	for _, arg := range call.Args {
		if bin, ok := arg.(*syntax.BinaryExpr); ok && bin.Op == syntax.EQ { // a keyword argument
			if name, ok := bin.X.(*syntax.Ident); ok { // the keyword is a name
				keywords[name.Name] = bin.Y
			}
		}
	}

	return keywords
}

// sandboxedPrompt returns the prompt and the sandbox expression of the call when it is an agent call with the keyword sandbox, and a nil expression otherwise.
func sandboxedPrompt(t *testing.T, call *syntax.CallExpr) (string, syntax.Expr) {
	t.Helper()

	if fn, ok := call.Fn.(*syntax.Ident); !ok || fn.Name != "agent" { // a call of a method or of another builtin
		return "", nil
	}

	keywords := keywordsOf(call)

	box, sandboxed := keywords["sandbox"]
	if !sandboxed {
		return "", nil
	}

	literal, ok := keywords["prompt"].(*syntax.Literal) // every sandboxed call of the example names its prompt as a string
	require.True(t, ok, "a sandboxed agent call names its prompt as a string")

	path, err := strconv.Unquote(literal.Raw)
	require.NoError(t, err)

	return path, box
}

// ghAsks renders the prompt and returns each sentence that asks the agent to use gh or the forge tool.
func ghAsks(t *testing.T, path string) []string {
	t.Helper()

	raw, err := fs.ReadFile(os.DirFS("."), path)
	require.NoError(t, err)

	tmpl, err := template.New(path).Funcs(skillFuncs).Option("missingkey=error").Parse(string(raw))
	require.NoError(t, err)

	var out bytes.Buffer

	require.NoError(t, tmpl.Execute(&out, promptData(facts())), path)

	var asks []string

	for _, s := range sentence.FindAllString(out.String(), -1) {
		if ghAsk.MatchString(s) && !forbids(s) {
			asks = append(asks, s)
		}
	}

	return asks
}

func TestPrompts_NoSandboxedStepAsksTheAgentToRunGh(t *testing.T) {
	t.Parallel()

	prompts := sandboxedPrompts(t)
	require.GreaterOrEqual(t, len(prompts), 6, "the parse finds the sandboxed steps of the example")

	for path := range prompts {
		assert.Empty(t, ghAsks(t, path), "%s runs in the sandbox, where gh cannot start and no credential reaches the agent", path)
	}
}

func TestPrompts_ThePlanAndTheQueueAuditRunInASandboxWithNoNetwork(t *testing.T) {
	t.Parallel()

	prompts := sandboxedPrompts(t)

	for _, path := range []string{"prompts/plan.md", "prompts/queue.md"} {
		call, ok := prompts[path].(*syntax.CallExpr)
		require.True(t, ok, "%s runs in a sandbox that the workflow builds with a call", path)

		fn, ok := call.Fn.(*syntax.Ident)
		require.True(t, ok)
		assert.Equal(t, "sandbox", fn.Name, path)
		assert.NotContains(t, keywordsOf(call), "network", "%s reads its forge data from the data file, so its shell reaches no host", path)
	}
}

func TestPrompts_NoSandboxOfTheExamplesReachesAHostBecauseTheEngineDoesEveryForgeCall(t *testing.T) {
	t.Parallel()

	// The least count of sandbox calls in each file, so a walk that finds none fails.
	files := map[string]int{"workflow.star": 6, "../queue/workflow.star": 1, "../local/workflow.star": 5}

	for path, least := range files {
		t.Run(path, func(t *testing.T) {
			t.Parallel()

			built, listed := sandboxHosts(t, path)

			require.GreaterOrEqual(t, built, least, "the walk finds the sandboxes of the example")
			assert.Empty(t, listed, "the engine pushes, opens and fetches for every step, so no sandbox lists a host")
		})
	}
}

// sandboxHosts returns the count of sandbox calls in the workflow file at path, and the position of each call that lists hosts.
func sandboxHosts(t *testing.T, path string) (built int, listed []string) {
	t.Helper()

	file, err := (&syntax.FileOptions{}).Parse(path, nil, 0)
	require.NoError(t, err)

	syntax.Walk(file, func(n syntax.Node) bool {
		call, ok := n.(*syntax.CallExpr)
		if !ok {
			return true
		}

		if fn, named := call.Fn.(*syntax.Ident); named && fn.Name == "sandbox" {
			built++

			// The only parameter of sandbox is the network list, so any argument, by keyword, by position or in a splat, lists hosts.
			if len(call.Args) > 0 {
				listed = append(listed, call.Lparen.String())
			}
		}

		return true
	})

	return built, listed
}

func TestPrompts_TheGhCheckFindsAnAskAndPassesAProhibition(t *testing.T) {
	t.Parallel()

	assert.True(t, ghAsk.MatchString("Read the pull requests with `gh pr list --state all`."))
	assert.True(t, ghAsk.MatchString("Read the open issues with the forge tool."))
	assert.True(t, forbids("**Never run `gh pr merge --auto`."))
	assert.False(t, forbids("Read the pull requests with `gh pr list`."))
	assert.True(t, forbids(" The orchestrator merges a pinned head with `gh pr merge`."))
	assert.True(t, forbids(" A merge is `gh pr merge` on a pinned head."))
	assert.False(t, forbids(" The orchestrator asks you to run `gh pr list`."), "a sentence about the orchestrator that asks for a read is an ask")
	assert.False(t, forbids(" The orchestrator merges with `gh pr merge` after you run `gh pr list`."), "a merge note that also asks for a read is an ask")
	assert.Equal(t, []string{"One.", " Two"}, sentence.FindAllString("One. Two", -1))
}
