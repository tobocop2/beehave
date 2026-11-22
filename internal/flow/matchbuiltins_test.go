package flow

import (
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// matched runs one line of a flow and returns what the flow showed in a command, and the error of the run.
func matched(t *testing.T, line string) (string, error) {
	t.Helper()

	h := newHarness(t)

	_, err := walkText(t, h, bodyFlow(line+"\n"))

	for _, ran := range h.commands.ran() {
		if shown, ok := strings.CutPrefix(ran, "show "); ok {
			return strings.TrimSpace(shown), err
		}
	}

	return "", err
}

func TestMatchBuiltins_PathsOutsideGivesTheSortedPathsThatNoGlobMatches(t *testing.T) {
	t.Parallel()

	shown, err := matched(t, `command(run = ["show", ",".join(paths_outside(allow = ["{docs,src}/**", "*.md"], paths = ["src/b.go", "z.txt", "docs/a/b.md", "a.txt", "README.md"]))])`)

	require.NoError(t, err)
	assert.Equal(t, "a.txt,z.txt", shown, "the braces and the star of the dialect of the agent scope, and the outside paths in order")
}

func TestMatchBuiltins_PathsOutsideRefusesTheListsThatTheAgentScopeRefuses(t *testing.T) {
	t.Parallel()

	for name, c := range map[string]struct{ line, message string }{
		"a bad pattern":                             {`paths_outside(allow = ["["], paths = [])`, `allow has the pattern "[", which does not parse`},
		"an empty list":                             {`paths_outside(allow = [], paths = [])`, "allow is an empty list"},
		"no list in a variable":                     {"scope = None\npaths_outside(allow = scope, paths = [\"a\"])", "allow is None, which names no scope"},
		"an entry that is no text":                  {`paths_outside(allow = ["a"], paths = [1])`, "paths must hold strings only"},
		"a missing keyword":                         {`paths_outside(allow = ["a"])`, "paths_outside needs the `paths` keyword"},
		"a bad list in a variable":                  {"bad = [\"[\"]\npaths_outside(allow = bad, paths = [])", `allow has the pattern "[", which does not parse`},
		"a name list in a variable that is no text": {"names = [1]\npaths_outside(allow = [\"a\"], paths = names)", "paths must hold strings only"},
		"a splat that misses one":                   {`paths_outside(**{"allow": ["a"]})`, "paths_outside: missing argument for paths"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := matched(t, c.line)

			require.ErrorContains(t, err, c.message)
		})
	}
}

func TestMatchBuiltins_ClosingReferencesGivesTheIssuesThatATextClosesInTheGrammarOfTheMergeGuard(t *testing.T) {
	t.Parallel()

	shown, err := matched(t, `command(run = ["show", ",".join(closing_references(text = "add a\n\nCloses\n#12\nsee #4\nfixes owner/repo#3"))])`)

	require.NoError(t, err)
	assert.Equal(t, "#12,owner/repo#3", shown, "a number on the next line counts, as it does for the merge guard, and a plain number does not")

	_, err = matched(t, `closing_references()`)

	require.ErrorContains(t, err, "closing_references needs the `text` keyword")

	_, err = matched(t, `closing_references(**{})`)

	require.ErrorContains(t, err, "closing_references: missing argument for text")
}

func TestMatchBuiltins_RecordNoStep(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	engine := h.engine(t)
	_, err := engine.RunFile(testItem, testProject, writeFlow(t, bodyFlow("paths_outside(allow = [\"a\"], paths = [\"b\"])\nclosing_references(text = \"fixes #1\")\n")), modeOff())

	require.NoError(t, err)
	assert.False(t, slices.ContainsFunc(stepNames(t, engine), func(name string) bool {
		return strings.HasPrefix(name, "paths_outside") || strings.HasPrefix(name, "closing_references")
	}), "a matcher judges text and a replay computes the same answer")
}
