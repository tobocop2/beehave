package recount_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehave/examples/remediation/tools/recount"
)

const body = `Intro.

## Pull requests merged into this branch (2 pull requests)

| PR | Change | Note |
|---|---|---|
| [#10](https://x/pull/10) | first | note |
| [#11](https://x/pull/11) | second | note |

## Closes

- Closes #5
| Closes #6 | proof |
`

func TestCount_CountsTheRowsOfTheTableAndTheClosingRows(t *testing.T) {
	t.Parallel()

	assert.Equal(t, recount.Tally{Rows: 2, Issues: 2}, recount.Count(body))
}

func TestCheck_AMatchingHeadingAndTitlePass(t *testing.T) {
	t.Parallel()

	assert.Empty(t, recount.Check("Remediation round IV: 2 reviewed pull requests, 2 issues", body))
}

func TestCheck_AHeadingThatDisagreesWithTheRowsIsAFinding(t *testing.T) {
	t.Parallel()

	stale := "## Pull requests merged into this branch (1 pull request)\n| #10 | a |\n| #11 | b |\n"

	found := recount.Check("A title that a person wrote", stale)

	assert.Equal(t, []string{"heading says 1 but the section has 2 rows"}, found)
}

func TestCheck_ATitleInTheFormOfTheToolMustCountTheRowsAndTheIssues(t *testing.T) {
	t.Parallel()

	found := recount.Check("Remediation round IV: 1 reviewed pull request, 0 issues", body)

	assert.Equal(t, []string{"title says 1 reviewed pull request, 0 issues but the body has 2, 2"}, found)
}

func TestCheck_ATitleThatAPersonWroteIsLeftAlone(t *testing.T) {
	t.Parallel()

	assert.Empty(t, recount.Check("Round four", body))
}

func TestCheck_ATitleInTheStartOfTheFormButNotTheWholeFormIsAFinding(t *testing.T) {
	t.Parallel()

	found := recount.Check("Remediation round IV: two reviewed", body)

	assert.Len(t, found, 1)
	assert.Contains(t, found[0], "title is not in the form")
}

func TestCheck_AHeadingWithNoFigureOrNoHeadingHasNothingToCompare(t *testing.T) {
	t.Parallel()

	assert.Empty(t, recount.Check("t", "## Merged pull requests\n| #1 | a |\n"))
	assert.Empty(t, recount.Check("t", "no heading\n| #1 | a |\n"))
}

func TestCheck_ANumberTooBigForAnIntMatchesNoCount(t *testing.T) {
	t.Parallel()

	found := recount.Check("t", "## Merged pull requests (99999999999999999999 pull requests)\n| #1 | a |\n")

	assert.Equal(t, []string{"heading says -1 but the section has 1 rows"}, found)
}

func TestCheck_ATitleWithManyPullRequestsUsesThePlural(t *testing.T) {
	t.Parallel()

	found := recount.Check("Remediation round IV: 3 reviewed pull requests, 0 issues", body)

	assert.Equal(t, []string{"title says 3 reviewed pull requests, 0 issues but the body has 2, 2"}, found)
}

func TestCount_ASecondTableOutsideTheHeadingSectionIsNotCounted(t *testing.T) {
	t.Parallel()

	other := body + "\n## Other\n\n| [#99](https://x/pull/99) | stray | note |\n"

	assert.Equal(t, 2, recount.Count(other).Rows)
	assert.Empty(t, recount.Check("Remediation round r: 2 reviewed pull requests, 2 issues", other))
}

func TestChanged_OneMergeAddsExactlyOneRowAndNeverRemovesAClosingRow(t *testing.T) {
	t.Parallel()

	now := recount.Tally{Rows: 4, Issues: 2}

	assert.Equal(t, []string{"the row count is 4 but one merge adds one row to the 2 before"}, recount.Changed(now, recount.Tally{Rows: 2, Issues: 2}, true, false), "two rows more, or a row swapped for two")
	assert.Equal(t, []string{"the row count is 4 but one merge adds one row to the 5 before"}, recount.Changed(now, recount.Tally{Rows: 5, Issues: 2}, true, false), "fewer rows than before")
	assert.Equal(t, []string{"the issue count is 2 but it was 3 before: a closing row was removed"}, recount.Changed(now, recount.Tally{Rows: 3, Issues: 3}, false, true))
	assert.Empty(t, recount.Changed(now, recount.Tally{Rows: 3, Issues: 1}, true, true))
}

func TestChanged_ACountThatDidNotMoveIsAFinding(t *testing.T) {
	t.Parallel()

	now := recount.Count(body)

	assert.Empty(t, recount.Changed(now, recount.Tally{Rows: 1, Issues: 2}, true, false))
	assert.Equal(t, []string{"the row count is still 2: the write did not land"}, recount.Changed(now, recount.Tally{Rows: 2, Issues: 2}, true, false))
	assert.Equal(t, []string{"the issue count is still 2: the write did not land"}, recount.Changed(now, recount.Tally{Rows: 1, Issues: 2}, true, true))
	assert.Empty(t, recount.Changed(now, recount.Tally{Rows: 2, Issues: 2}, false, false), "a count that is not asked about is not compared")
}
