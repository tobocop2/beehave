package brief_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/brief"
)

const rulesFile = "rules.md"

// rulesWith puts body between a preflight section and a CI section, as a rules file lays it out.
func rulesWith(body string) string {
	return "## Maintainer preflight\n- keep-this-rule: run the preflight.\n\n" +
		body + "\n\n## CI checks and their base\n\n- Run: `poly fmt --check`\n- Base: the pull request base\n"
}

func lean(t *testing.T, kind brief.Kind, text string) string {
	t.Helper()

	out, err := brief.Lean(text, kind, rulesFile)
	require.NoError(t, err)

	return out
}

func TestLean_DropsAWhyBlockAndKeepsTheRuleBesideIt(t *testing.T) {
	t.Parallel()

	out := lean(t, brief.KindImpl, rulesWith("<!--why\nThe story: STORY-OF-THE-RULE.\nwhy-->"))

	assert.Contains(t, out, "keep-this-rule: run the preflight.")
	assert.NotContains(t, out, "STORY-OF-THE-RULE")
	assert.NotContains(t, out, "why-->")
}

func TestLean_DropsAOneLineWhyBlockAndKeepsTheTextAround(t *testing.T) {
	t.Parallel()

	out := lean(t, brief.KindImpl, rulesWith("BEFORE-ONE\n<!--why a one line story why-->\nAFTER-ONE"))

	assert.Contains(t, out, "BEFORE-ONE")
	assert.Contains(t, out, "AFTER-ONE")
	assert.NotContains(t, out, "one line story")
}

func TestLean_KeepsATaggedSectionForItsKindsOnly(t *testing.T) {
	t.Parallel()

	text := rulesWith("## Reviewer only\n<!-- for: rev -->\nREVIEWER-ONLY-SECTION\n\n" +
		"## Implementer only\n<!-- for: impl design -->\nIMPLEMENTER-ONLY-SECTION")

	impl := lean(t, brief.KindImpl, text)
	assert.Contains(t, impl, "IMPLEMENTER-ONLY-SECTION")
	assert.NotContains(t, impl, "REVIEWER-ONLY-SECTION")
	assert.NotContains(t, impl, "<!-- for:")

	rev := lean(t, brief.KindRev, text)
	assert.Contains(t, rev, "REVIEWER-ONLY-SECTION")
	assert.NotContains(t, rev, "IMPLEMENTER-ONLY-SECTION")
	assert.Contains(t, rev, "## CI checks and their base")
}

func TestLean_AcceptsEveryKnownKindInAForLine(t *testing.T) {
	t.Parallel()

	out := lean(t, brief.KindPapers, rulesWith("## Sec\n<!-- for: impl rev design papers -->\nBODY"))

	assert.Contains(t, out, "BODY")
}

func TestLean_RefusesAMalformedMarkerWithTheFileAndTheLine(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name, body, line, fragment string
	}{
		{"unterminated", "## Open\ntext\n<!--why\nstory with no end", "6", "has no why--> line"},
		{"nested", "<!--why\nouter\n<!--why\ninner\nwhy-->\nwhy-->", "6", "opens inside the block opened"},
		{"unknown-kind", "## Sec\n<!-- for: impl reviewer -->\nBODY", "5", `"reviewer"`},
		{"blank-before-for", "## Sec\n\n<!-- for: rev -->\nBODY", "6", "first line under its heading"},
		{"no-kind", "## Sec\n<!-- for: -->\nBODY", "5", "names no kind"},
		{"tail-in-block", "<!--why\nstory\nwhy--> TAIL\nNEXT", "6", "text follows why-->"},
		{"tail-on-one-line", "<!--why story why--> TAIL\nNEXT", "4", "text follows why-->"},
		{"for-no-space", "## Sec\n<!--for: rev -->\nBODY-TYPO", "5", "looks like a for: marker"},
		{"for-two-spaces", "## Sec\n<!--  for: rev -->\nBODY-TYPO", "5", "looks like a for: marker"},
		{"for-no-colon", "## Sec\n<!-- for rev -->\nBODY-TYPO", "5", "looks like a for: marker"},
		{"for-indented", "## Sec\n  <!-- for: rev -->\nBODY-TYPO", "5", "looks like a for: marker"},
		{"for-no-arrow", "## Sec\n<!-- for: rev\nBODY-TYPO", "5", "has no closing -->"},
		{"why-indented", "  <!--why\nSTORY-TYPO\nwhy-->\nLIVE", "4", "looks like a why marker"},
		{"why-spaced", "<!-- why\nSTORY-TYPO\nwhy-->\nLIVE", "4", "looks like a why marker"},
		{"why-tab", "\t<!--why\nSTORY-TYPO\nwhy-->\nLIVE", "4", "looks like a why marker"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			out, err := brief.Lean(rulesWith(tc.body), brief.KindImpl, rulesFile)

			require.Error(t, err)
			assert.Empty(t, out)
			assert.Contains(t, err.Error(), rulesFile+":"+tc.line+":")
			assert.Contains(t, err.Error(), tc.fragment)
		})
	}
}

func TestLean_RefusesAForLineBeforeAnyHeading(t *testing.T) {
	t.Parallel()

	_, err := brief.Lean("<!-- for: impl -->\n## Maintainer preflight\n- r\n", brief.KindImpl, rulesFile)

	require.Error(t, err)
	assert.Contains(t, err.Error(), rulesFile+":1:")
	assert.Contains(t, err.Error(), "before any heading")
}

func TestLean_SkipLastsAcrossSubheadingsAndFences(t *testing.T) {
	t.Parallel()

	text := rulesWith("## Rev only\n<!-- for: rev -->\nREV-TOP\n### Sub\nREV-SUB\n```\n# not a heading\n" +
		"REV-FENCED\n```\n#### Deeper\nREV-DEEPER\n## Next\nNEXT-SECTION")

	impl := lean(t, brief.KindImpl, text)
	for _, gone := range []string{"REV-TOP", "REV-SUB", "REV-FENCED", "REV-DEEPER", "### Sub", "not a heading"} {
		assert.NotContains(t, impl, gone)
	}
	assert.Contains(t, impl, "NEXT-SECTION")

	rev := lean(t, brief.KindRev, text)
	for _, kept := range []string{"REV-SUB", "REV-FENCED", "REV-DEEPER"} {
		assert.Contains(t, rev, kept)
	}
}

func TestLean_ATildeFenceHoldsHashLinesLikeABacktickFence(t *testing.T) {
	t.Parallel()

	plain := lean(t, brief.KindImpl, rulesWith("## Rev only\n<!-- for: rev -->\nREV-TOP\n~~~\n# not a heading\n~~~\n"+
		"REV-AFTER-FENCE\n## Next\nNEXT-SECTION"))
	assert.NotContains(t, plain, "REV-AFTER-FENCE")
	assert.NotContains(t, plain, "REV-TOP")
	assert.Contains(t, plain, "NEXT-SECTION")

	mixed := lean(t, brief.KindImpl, rulesWith("## Rev only\n<!-- for: rev -->\n~~~\n```\n# still fenced\n~~~\n"+
		"REV-AFTER-FENCE\n## Next\nNEXT-SECTION"))
	assert.NotContains(t, mixed, "REV-AFTER-FENCE")
	assert.Contains(t, mixed, "NEXT-SECTION")
}

func TestLean_ATypoedMarkerInsideAFenceIsText(t *testing.T) {
	t.Parallel()

	out := lean(t, brief.KindImpl, rulesWith("```\n<!--for: rev -->\n  <!--why\n```\nFENCED-OK"))

	assert.Contains(t, out, "FENCED-OK")
	assert.Contains(t, out, "<!--for: rev -->")
}

func TestLean_ReadsACRLFFileAndKeepsTheCarriageReturn(t *testing.T) {
	t.Parallel()

	text := "## Maintainer preflight\r\n- keep-crlf\r\n\r\n<!--why\r\nSTORY-CRLF\r\nwhy-->\r\n\r\n" +
		"## Rev only\r\n<!-- for: rev -->\r\nREV-CRLF\r\n\r\n## Both\r\n<!-- for: impl rev -->\r\nBOTH-CRLF\r\n"

	out := lean(t, brief.KindImpl, text)

	assert.Contains(t, out, "keep-crlf\r\n")
	assert.Contains(t, out, "BOTH-CRLF")
	assert.NotContains(t, out, "STORY-CRLF")
	assert.NotContains(t, out, "REV-CRLF")
}

func TestLean_ALoneCarriageReturnIsNotALineEnd(t *testing.T) {
	t.Parallel()

	out := lean(t, brief.KindImpl, "## A\r<!-- for: rev -->\rBODY\r")

	assert.Equal(t, "## A\r<!-- for: rev -->\rBODY\r\n", out)
}

func TestLean_EndsWithOneNewlineWhateverTheInputEndsWith(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "\n", lean(t, brief.KindImpl, ""))
	assert.Equal(t, "a\n", lean(t, brief.KindImpl, "a\n\n\n"))
	assert.Equal(t, "a\n\nb\n", lean(t, brief.KindImpl, "a\n\nb"))
}

func TestLean_AWhyBlockOpensInsideAFence(t *testing.T) {
	t.Parallel()

	out := lean(t, brief.KindImpl, "```\n<!--why\nSTORY\nwhy-->\n```\nAFTER")

	assert.NotContains(t, out, "STORY")
	assert.True(t, strings.HasSuffix(out, "AFTER\n"))
}

func TestParseKind_NamesTheClosedSet(t *testing.T) {
	t.Parallel()

	for _, kind := range brief.Kinds() {
		got, err := brief.ParseKind(string(kind))
		require.NoError(t, err)
		assert.Equal(t, kind, got)
	}

	_, err := brief.ParseKind("fix")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "impl, rev, design and papers")
}
