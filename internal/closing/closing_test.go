package closing_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehave/internal/closing"
)

func TestReferences_ListsEachClosingKeywordAndIgnoresAPlainNumber(t *testing.T) {
	t.Parallel()

	text := "Fixes #12 and resolves other/repo#3.\nSee #99.\nCloses https://github.com/Owner/Repo/issues/7"

	assert.Equal(t, []string{"#12", "other/repo#3", "owner/repo#7"}, closing.References(text))
	assert.Empty(t, closing.References("Part of #5, no keyword."))
}

func TestReferences_AKeywordAtTheEndOfALineClosesTheIssueOnTheNextLine(t *testing.T) {
	t.Parallel()

	text := "add a\n\nCloses\n#12\nfixes:\n\towner/repo#3\nsee #4"

	assert.Equal(t, []string{"#12", "owner/repo#3"}, closing.References(text), "GitHub reads a line break after the keyword as a space")
}

func TestReferences_AllNineKeywordsInAnyCaseAndAnOptionalColonCount(t *testing.T) {
	t.Parallel()

	text := "close #1 Closes #2 CLOSED #3 fix #4 Fixes: #5 fixed #6 resolve #7 RESOLVES #8 Resolved: #9 unfixed #10"

	assert.Equal(t, []string{"#1", "#2", "#3", "#4", "#5", "#6", "#7", "#8", "#9"}, closing.References(text))
}
