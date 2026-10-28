package check_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/check"
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

			lines, err := check.Papers(t.Context(), diffRunner{changed}, "A..B", tc.globs)

			require.NoError(t, err)
			assert.Equal(t, tc.want, lines[0])
		})
	}
}

func TestPapers_PatternsLine(t *testing.T) {
	t.Parallel()

	changed := diffRunner{model.CommandResult{Stdout: "crates/a/src/a.rs\n"}}

	lines, err := check.Papers(t.Context(), changed, "A..B", globs)
	require.NoError(t, err)
	assert.Contains(t, lines[1], "A change to source is a fix lane")
	assert.Contains(t, lines[1], "Patterns: "+globs)

	lines, err = check.Papers(t.Context(), changed, "A..B", "")
	require.NoError(t, err)
	assert.Contains(t, lines[1], "Patterns: <every path except *.md and *.txt>")
}

func TestPapers_Refusals(t *testing.T) {
	t.Parallel()

	for _, bad := range []string{"HEAD", "..HEAD", "HEAD..", "--output=x..HEAD", "-x..HEAD"} {
		_, err := check.Papers(t.Context(), diffRunner{}, bad, globs)
		require.ErrorContains(t, err, "usage: obee check papers <base>..<head>", bad)
	}

	_, err := check.Papers(t.Context(), diffRunner{model.CommandResult{ExitCode: 128}}, "nosuchref..HEAD", globs)
	require.ErrorContains(t, err, "git cannot read the range nosuchref..HEAD")
}

func TestPapers_GlobsMatchAsFnmatchDoes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name, path, glob string
		source           bool
	}{
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
		{"unclosed_bracket_is_literal", "x[a", "x[a", true},
		{"unclosed_bracket_matches_nothing_else", "xa", "x[a", false},
		{"bang_then_close_unclosed", "[!", "[!", true},
		{"leading_close_is_a_member", "]", "[]a]", true},
		{"empty_bracket_is_literal", "[]", "[]", true},
		{"reversed_range_holds_nothing", "b", "[b-a]", false},
		{"negated_reversed_range_holds_all", "b", "[!b-a]", true},
		{"trailing_dash_is_literal", "-", "[a-]", true},
		{"leading_dash_is_literal", "-", "[-a]", true},
		{"dash_after_range_is_literal", "-", "[a-c-]", true},
		{"backslash_is_literal", `a\b`, `a\b`, true},
		{"case_sensitive", "CRATES/a/src/x", "crates/*/src/*", false},
		{"star_matches_empty", "a", "a*", true},
		{"star_backtracks", "abcabd", "*abd", true},
		{"unicode_one_rune", "é", "?", true},
		{"blank_globs_match_nothing", "src/a.rs", " \t ", false},
		{"any_of_several", "b.go", "a.go b.go", true},
		{"form_feed_separates", "b.go", "a.go\fb.go", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			lines, err := check.Papers(t.Context(), diffRunner{model.CommandResult{Stdout: tc.path + "\n"}}, "A..B", tc.glob)
			require.NoError(t, err)
			assert.Equal(t, tc.source, strings.HasPrefix(lines[0], "REFUSED"), lines[0])
		})
	}
}

func TestPapers_RangeShape(t *testing.T) {
	t.Parallel()

	for _, ok := range []string{"a..b", "a...b", "..a..b", "x...", "a..b..c"} {
		_, err := check.Papers(t.Context(), diffRunner{}, ok, globs)
		require.NoError(t, err, ok)
	}

	for _, bad := range []string{"", "...", "..x..", "a..", "..a", "a.b"} {
		_, err := check.Papers(t.Context(), diffRunner{}, bad, globs)
		require.Error(t, err, bad)
	}
}

func TestPapers_NoGlobsTreatsDocumentsAsPapers(t *testing.T) {
	t.Parallel()

	lines, err := check.Papers(t.Context(), diffRunner{model.CommandResult{Stdout: "docs/a.md\ndocs/a.txt\nsrc/a.rs\n"}}, "A..B", "")
	require.NoError(t, err)
	assert.Contains(t, lines[0], "changes 1 source path(s)")
	assert.Contains(t, lines[0], "src/a.rs")
}
