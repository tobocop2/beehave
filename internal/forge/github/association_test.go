package github_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/forge"
	"github.com/tobocop2/beehave/internal/model"
)

// issueOrPull is the answer of the association query for one issue or pull request.
func issueOrPull(t *testing.T, node any) string {
	t.Helper()

	return toJSON(t, fields{"data": fields{"repository": fields{"issueOrPullRequest": node}}})
}

func TestAssociation_ReadsTheAuthorOfAnIssueOrAPullRequestInOneQuery(t *testing.T) {
	t.Parallel()

	f, run := fixed(t, issueOrPull(t, fields{"authorAssociation": "CONTRIBUTOR"}))

	got, err := f.Association(context.Background(), 77)

	require.NoError(t, err)
	assert.Equal(t, model.AssociationContributor, got)
	require.Len(t, run.Calls, 1)
	assert.Equal(t, []string{"-f", "o=owner", "-f", "n=name", "-F", "p=77"}, run.Calls[0][2:8])
	assert.Contains(t, run.Calls[0][9], "issueOrPullRequest(number:$p){... on Issue{authorAssociation} ... on PullRequest{authorAssociation}}")
}

func TestAssociation_RefusesAnAnswerThatNamesNoKnownAuthor(t *testing.T) {
	t.Parallel()

	const base = "cannot read who wrote issue or pull request #77"

	for name, test := range map[string]struct {
		stdout string
		want   string
	}{
		"no such number":     {issueOrPull(t, nil), base + ": no such issue or pull request"},
		"no repository":      {`{"data":{"repository":null}}`, base + ": no such issue or pull request"},
		"no data":            {`{"data":null}`, base + ": no such issue or pull request"},
		"not json":           {"not json", base + ": cannot read the answer"},
		"an unknown value":   {issueOrPull(t, fields{"authorAssociation": "OWNER_ISH"}), base + ": unknown author association \"OWNER_ISH\""},
		"an empty value":     {issueOrPull(t, fields{}), base + ": unknown author association \"\""},
		"a value not a text": {issueOrPull(t, fields{"authorAssociation": 3}), base + ": cannot read the answer"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			f, _ := fixed(t, test.stdout)

			_, err := f.Association(context.Background(), 77)

			require.Error(t, err)
			assert.True(t, strings.HasPrefix(err.Error(), test.want), err.Error())
		})
	}

	f, _ := failing(t, "")

	_, err := f.Association(context.Background(), 77)
	require.EqualError(t, err, base)
}

func TestAssociation_IsTheOptionalReadOfTheForge(t *testing.T) {
	t.Parallel()

	f, _ := fixed(t, "")

	var built any = f

	_, ok := built.(forge.AssociationReads)
	assert.True(t, ok)
}
