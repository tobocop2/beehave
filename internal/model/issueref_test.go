package model_test

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
)

func TestIssueRef_AnOwnIssueIsHashAndNumberAndAnotherRepositoryIsFullAndLowerCase(t *testing.T) {
	t.Parallel()

	assert.Equal(t, model.IssueRef("#12"), model.OwnIssue(12))
	assert.Equal(t, model.IssueRef("other-org/other-repo#5"), model.ElsewhereIssue("Other-Org/Other-Repo", 5))
}

func TestCompareIssues_OwnIssuesComeFirstByNumberThenOtherRepositoriesByNameAndNumber(t *testing.T) {
	t.Parallel()

	refs := []model.IssueRef{"b/c#1", "#10", "a/z#9", "#9", "a/z#10"}
	slices.SortFunc(refs, model.CompareIssues)
	assert.Equal(t, []model.IssueRef{"#9", "#10", "a/z#9", "a/z#10", "b/c#1"}, refs)
	assert.Zero(t, model.CompareIssues("a/z#9", "a/z#9"))
	assert.Negative(t, model.CompareIssues("#x", "#1"), "a text with no number counts as number 0")
}

func TestParseIssue_ReadsAnOwnIssueAndAFullReferenceAndNothingElse(t *testing.T) {
	t.Parallel()

	issue, ok := model.ParseIssue("#12")
	assert.True(t, ok)
	assert.Equal(t, model.IssueRef("#12"), issue)

	issue, ok = model.ParseIssue("Other-Org/Other-Repo#5")
	assert.True(t, ok)
	assert.Equal(t, model.IssueRef("other-org/other-repo#5"), issue)

	for _, text := range []string{"12", "#", "a/b#x", "#99999999999999999999", ""} {
		_, ok = model.ParseIssue(text)
		assert.False(t, ok, text)
	}
}

func TestIssueRef_ASetThatAnEarlierRunRecordedAsNumbersStillReadsAsOwnIssues(t *testing.T) {
	t.Parallel()

	var refs []model.IssueRef

	require.NoError(t, json.Unmarshal([]byte(`[3, "#4", "a/b#5"]`), &refs))
	assert.Equal(t, []model.IssueRef{"#3", "#4", "a/b#5"}, refs)
}

func TestIssueRef_AValueThatIsNeitherANumberNorTextIsAnError(t *testing.T) {
	t.Parallel()

	var refs []model.IssueRef

	require.Error(t, json.Unmarshal([]byte(`[true]`), &refs))
	require.Error(t, json.Unmarshal([]byte(`["a", {"x": 1}]`), &refs))
	require.Error(t, json.Unmarshal([]byte(`[]x`), &refs))
}
