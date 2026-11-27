package workflow

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	"github.com/tobocop2/beehest/internal/model"
)

// memberWith is the prs list of one member that has the required fields and the extra fields.
func memberWith(extra map[string]starlark.Value) starlark.Value {
	fields := []starlark.Tuple{{starlark.String("item"), starlark.String("i")}, {starlark.String("head"), starlark.String("h")}, {starlark.String("closes"), starlark.NewList(nil)}}
	for name, value := range extra {
		fields = append(fields, starlark.Tuple{starlark.String(name), value})
	}

	return starlark.NewList([]starlark.Value{starlarkstruct.FromKeywords(starlarkstruct.Default, fields)})
}

func issueTexts(values ...string) starlark.Value {
	list := make([]starlark.Value, len(values))
	for i, value := range values {
		list[i] = starlark.String(value)
	}

	return starlark.NewList(list)
}

func TestPullRequests_KeptAndClosingAreListsOfIssuesAsTextAndLeftOutMeansNone(t *testing.T) {
	t.Parallel()

	got, err := PullRequests(memberWith(nil))
	require.NoError(t, err)
	assert.Nil(t, got[0].Kept)
	assert.Nil(t, got[0].Closing, "a member that leaves closing out pins no set")

	got, err = PullRequests(memberWith(map[string]starlark.Value{"kept": issueTexts("#13", "Other-Org/Other-Repo#5"), "closing": issueTexts("#12", "#13")}))
	require.NoError(t, err)
	assert.Equal(t, []model.IssueRef{"#13", "other-org/other-repo#5"}, got[0].Kept, "the repository of another issue is in lower case")
	assert.Equal(t, []model.IssueRef{"#12", "#13"}, got[0].Closing)

	got, err = PullRequests(memberWith(map[string]starlark.Value{"closing": issueTexts()}))
	require.NoError(t, err)
	assert.NotNil(t, got[0].Closing, "an empty closing list pins a set of no issue")
	assert.Empty(t, got[0].Closing)
}

func TestPullRequests_RefusesAKeptOrClosingFieldThatIsNotAListOfIssuesAsText(t *testing.T) {
	t.Parallel()

	for field, values := range map[string][]starlark.Value{
		"kept":    {starlark.String("#13"), starlark.MakeInt(13), starlark.NewList([]starlark.Value{starlark.MakeInt(13)}), issueTexts("13"), issueTexts("#x"), issueTexts("")},
		"closing": {starlark.String("#13"), starlark.NewList([]starlark.Value{starlark.True}), issueTexts("owner/repo"), issueTexts("#99999999999999999999")},
	} {
		for _, value := range values {
			_, err := PullRequests(memberWith(map[string]starlark.Value{field: value}))

			require.ErrorContains(t, err, "`"+field+"` of a member of `prs` ", "%s = %s", field, value)
		}
	}
}

func TestPullRequests_SkipsAComputedKeptFieldAndAComputedIssueOfClosing(t *testing.T) {
	t.Parallel()

	got, err := PullRequests(memberWith(map[string]starlark.Value{"kept": computed{}, "closing": starlark.NewList([]starlark.Value{computed{}, starlark.String("#12")})}))

	require.NoError(t, err)
	assert.Nil(t, got[0].Kept, "a value that only the run knows is left to the run")
	assert.Equal(t, []model.IssueRef{"#12"}, got[0].Closing)
}
