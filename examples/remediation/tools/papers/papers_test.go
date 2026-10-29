package papers_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/examples/remediation/tools/check"
	"github.com/tobocop2/obee/examples/remediation/tools/papers"
	"github.com/tobocop2/obee/internal/model"
)

const globs = "crates/*/src/* packages/*/src/*"

type diffRunner struct{ result model.CommandResult }

func (d diffRunner) Run(context.Context, []string) model.CommandResult { return d.result }

func TestPapers_Verdicts(t *testing.T) {
	t.Parallel()

	changed := model.CommandResult{Stdout: "reports/proof.md\ncrates/a/tests/t.rs\ncrates/a/src/deep/lib.rs\n"}

	cases := []struct {
		name, globs string
		want        string
	}{
		{"source_path_refused", globs, "REFUSED: A..B changes 1 source path(s). This lane is proof and papers only:\n         crates/a/src/deep/lib.rs"},
		{"no_globs_all_but_documents", "", "REFUSED: A..B changes 2 source path(s). This lane is proof and papers only:\n         crates/a/tests/t.rs\n         crates/a/src/deep/lib.rs"},
		{"blank_globs_match_nothing", " \t ", "OK: A..B changes no source path."},
		{"case_sensitive", "CRATES/*/src/*", "OK: A..B changes no source path."},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			lines, err := papers.SourcePaths(t.Context(), diffRunner{changed}, "A..B", tc.globs)

			require.NoError(t, err)
			assert.Equal(t, tc.want, lines[0])
		})
	}
}

func TestPapers_PatternsLine(t *testing.T) {
	t.Parallel()

	changed := diffRunner{model.CommandResult{Stdout: "crates/a/src/a.rs\n"}}

	lines, err := papers.SourcePaths(t.Context(), changed, "A..B", globs)
	require.NoError(t, err)
	assert.Contains(t, lines[1], "A change to source is a fix lane")
	assert.Contains(t, lines[1], "Patterns: "+globs)

	lines, err = papers.SourcePaths(t.Context(), changed, "A..B", "")
	require.NoError(t, err)
	assert.Contains(t, lines[1], "Patterns: <every path except *.md and *.txt>")
}

func TestPapers_Refusals(t *testing.T) {
	t.Parallel()

	for _, bad := range []string{"HEAD", "..HEAD", "HEAD..", "--output=x..HEAD", "-x..HEAD"} {
		_, err := papers.SourcePaths(t.Context(), diffRunner{}, bad, globs)
		require.ErrorContains(t, err, "usage: remedy papers <base>..<head>", bad)
	}

	_, err := papers.SourcePaths(t.Context(), diffRunner{model.CommandResult{ExitCode: 128}}, "nosuchref..HEAD", globs)
	require.ErrorContains(t, err, "git cannot read the range nosuchref..HEAD")
}

// globCase is one fixture of the source globs: a changed path, the globs and whether the path is source under them.
type globCase struct {
	name, path, glob string
	source           bool
}

var globCases = []globCase{
	{"star_crosses_slash", "crates/a/src/deep/lib.rs", "crates/*/src/*", true},
	{"literal_mismatch", "crates/a/tests/t.rs", "crates/*/src/*", false},
	{"question_mark_one_char", "a/b", "a?b", true},
	{"question_mark_needs_a_char", "ab", "a?b", false},
	{"set_member", "x.c", "x.[ch]", true},
	{"set_range", "x.d", "x.[a-e]", true},
	{"set_range_miss", "x.z", "x.[a-e]", false},
	{"bang_negates", "x.c", "x.[!c]", false},
	{"bang_negates_other", "x.d", "x.[!c]", true},
	{"caret_is_literal", "x.^", "x.[^c]", true},
	{"caret_does_not_negate", "x.d", "x.[^c]", false},
	{"leading_dash_is_literal", "-", "[-a]", true},
	{"backslash_escapes_the_next_character", "ab", `a\b`, true},
	{"backslash_pattern_does_not_match_the_backslash", `a\b`, `a\b`, false},
	{"braces_choose_between_patterns", "b.go", "{a,b}.go", true},
	{"braces_with_one_choice_match_it", "x.rs", "{x}.rs", true},
	{"braces_with_one_choice_are_not_literal", "{x}.rs", "{x}.rs", false},
	{"escaped_braces_are_literal", "{x}.rs", `\{x\}.rs`, true},
	{"escaped_star_is_a_star_only", "a*", `a\*`, true},
	{"escaped_star_does_not_cross", "ab", `a\*`, false},
	{"case_sensitive", "CRATES/a/src/x", "crates/*/src/*", false},
	{"star_matches_empty", "a", "a*", true},
	{"star_backtracks", "abcabd", "*abd", true},
	{"unicode_one_rune", "é", "?", true},
	{"blank_globs_match_nothing", "src/a.rs", " \t ", false},
	{"any_of_several", "b.go", "a.go b.go", true},
	{"form_feed_separates", "b.go", "a.go\fb.go", true},
}

func TestPapers_AMalformedPatternIsARefusalThatNamesIt(t *testing.T) {
	t.Parallel()

	for _, bad := range []string{"x[a", "[!", "[]", "[]a]", "[b-a]", "[!b-a]", "[a-]", "[a-c-]", "[]]", "[!]a]", `a\`, "{", "[!"} {
		_, err := papers.SourcePaths(t.Context(), diffRunner{model.CommandResult{Stdout: "src/a.rs\n"}}, "A..B", "src/* "+bad)

		var refusal *check.Refusal

		require.ErrorAs(t, err, &refusal, bad)
		require.ErrorContains(t, err, fmt.Sprintf("the pattern %q is not valid", bad), bad)
	}
}

func TestPapers_GlobsMatchTheCasesOfTheTable(t *testing.T) {
	t.Parallel()

	for _, tc := range globCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			lines, err := papers.SourcePaths(t.Context(), diffRunner{model.CommandResult{Stdout: tc.path + "\n"}}, "A..B", tc.glob)
			require.NoError(t, err)
			assert.Equal(t, tc.source, strings.HasPrefix(lines[0], "REFUSED"), lines[0])
		})
	}
}

func TestPapers_RangeShape(t *testing.T) {
	t.Parallel()

	for _, ok := range []string{"a..b", "a...b", "..a..b", "x...", "a..b..c"} {
		_, err := papers.SourcePaths(t.Context(), diffRunner{}, ok, globs)
		require.NoError(t, err, ok)
	}

	for _, bad := range []string{"", "...", "..x..", "a..", "..a", "a.b"} {
		_, err := papers.SourcePaths(t.Context(), diffRunner{}, bad, globs)
		require.Error(t, err, bad)
	}
}

func TestPapers_NoGlobsTreatsDocumentsAsPapers(t *testing.T) {
	t.Parallel()

	lines, err := papers.SourcePaths(t.Context(), diffRunner{model.CommandResult{Stdout: "docs/a.md\ndocs/a.txt\nsrc/a.rs\n"}}, "A..B", "")
	require.NoError(t, err)
	assert.Contains(t, lines[0], "changes 1 source path(s)")
	assert.Contains(t, lines[0], "src/a.rs")
}
