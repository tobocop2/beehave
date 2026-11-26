package ghcontract_test

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/ghcontract"
)

func words(text string) []string { return strings.Fields(text) }

func TestShapes_ReadsEveryLineOfTheTable(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile("gh-2.101.0.txt")
	require.NoError(t, err)

	lines := 0

	for _, line := range strings.Split(string(data), "\n") {
		if line != "" && !strings.HasPrefix(line, "#") {
			lines++
		}
	}

	shapes := ghcontract.Shapes()
	assert.Len(t, shapes, lines)

	for _, shape := range shapes {
		assert.NotEmpty(t, shape.Words, "a line of the table names no command")
		assert.NotEmpty(t, shape.Flags, "%v has no flag", shape.Words)
	}
}

func TestCheck_RefusesAnUnknownJSONFieldOfAList(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
	}{
		{"pr_list", words("pr list -R o/n --state open --json number,authorAssociation")},
		{"pr_view", words("pr view 7 --repo o/n --json authorAssociation")},
		{"issue_list", words("issue list -R o/n --json number,authorAssociation")},
		{"issue_view", words("issue view 7 -R o/n --json authorAssociation")},
		{"inline_value", words("pr list --json=number,authorAssociation")},
		{"empty_field", words("pr list --json number,")},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			stderr, ok := ghcontract.Check(tc.args)

			assert.False(t, ok)
			assert.Contains(t, stderr, "Unknown JSON field: ")
			assert.Contains(t, stderr, "Available fields:\n  ")
		})
	}

	stderr, _ := ghcontract.Check(words("pr list --json authorAssociation"))
	assert.True(t, strings.HasPrefix(stderr, `Unknown JSON field: "authorAssociation"`), stderr)

	stderr, ok := ghcontract.Check(words("search prs --json authorAssociation"))
	assert.True(t, ok, "search prs has the field authorAssociation: %s", stderr)
}

// today lists the shapes that the code sends at the base, each with the field list or the flags that the code gives it.
var today = []string{
	"pr list -R o/n --limit 5 --state open --json number,headRefName,baseRefName,labels,reviewDecision,author,isDraft,title --base main",
	"pr list --head b --state all --json number",
	"pr list --state all --search x -L 3 --json number,title,body",
	"pr list --state all --json number,title,body,state,headRefName,baseRefName,mergedAt,closedAt",
	"pr list --state open --json number,author,title,files",
	"pr list --state open --base b --json number,baseRefName",
	"pr list --state open --author me --json number,isDraft",
	"pr list --state merged --base b --json number,title,state,headRefName",
	"pr list --state all --head h --base b --json number,state,body",
	"pr view 7 -R o/n --json number,state,headRefOid,baseRefName,author,assignees,isDraft,labels,body,reviews,reviewRequests",
	"pr view 7 --json number,headRefName,headRefOid,author",
	"pr view 7 --json state,isDraft,headRefName,mergeable,files,commits,body,author,labels,mergedAt",
	"pr view 7 --json number,title,author,headRefOid,isDraft,updatedAt,reviewRequests",
	"pr view 7 --json number,state,isDraft,headRefOid,author,assignees,updatedAt,commits,comments,reviews",
	"pr view 7 --json body --jq .body",
	"pr checks 7 --json name,bucket,workflow,link",
	"issue list --state all --json number,title,labels,assignees --limit 100",
	"issue list --state all --search x --json number,body",
	"issue view 5 --json number,title,body,labels",
	"issue view 5 --json state,labels",
	"search prs --involves me --json number --limit 3",
	"search issues x --json number",
	"repo view o/n --json defaultBranchRef --jq .defaultBranchRef.name",
	"label list --search x --json name --limit 100",
	"auth status --active --hostname github.com --json hosts",
	"pr create --draft --head h --base b --title t --body b --label l",
	"pr comment 7 --body x",
	"pr close 7 --comment x",
	"pr ready 7 --undo",
	"pr edit 7 --base b",
	"pr edit 7 --body -text",
	"pr merge 7 --merge --match-head-commit abc",
	"pr merge 7 --auto --merge --match-head-commit abc",
	"pr merge 7 --disable-auto",
	"issue create --title t --body b --parent 3 --assignee a --label l",
	"issue edit 5 --add-label hold --remove-label hold",
	"issue close 5",
	"issue reopen 5",
	"issue comment 5 --body x",
	"label create name",
	"stack link --base main u1 u2",
	"stack unstack 3",
	"api --paginate --slurp repos/o/n/pulls/7/reviews",
	"api repos/o/n/statuses/abc -X POST -f state=success -f context=c --jq .id",
	"api graphql -F p=7 -f query=q -F owner=o",
	"run view --job 12 --log-failed -R o/n",
	"project field-list 3 --owner o --format json --limit 100",
	"project item-edit --id i --project-id p --field-id f --single-select-option-id o",
	"project item-add 3 --owner o --url u --format json",
	"project item-list 3 --owner o --format json --limit 100",
	"project view 3 --owner o --format json",
	"auth token --hostname github.com",
}

func TestCheck_AcceptsTheFieldsOfEveryShape(t *testing.T) {
	t.Parallel()

	for _, call := range today {
		stderr, ok := ghcontract.Check(words(call))

		assert.True(t, ok, "gh %s: %s", call, stderr)
	}

	for _, shape := range ghcontract.Shapes() {
		if len(shape.Fields) == 0 {
			continue
		}

		stderr, ok := ghcontract.Check(slices.Concat(shape.Words, []string{"--json", strings.Join(shape.Fields, ",")}))

		assert.True(t, ok, "every field of %v: %s", shape.Words, stderr)
	}
}

func TestCheck_RefusesAnUnknownFlag(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want string
	}{
		{"long_flag", words("pr list --nope"), "unknown flag: --nope"},
		{"long_flag_with_value", words("pr view 7 --nope=1 --json number"), "unknown flag: --nope"},
		{"short_flag", words("pr list -Z"), "unknown shorthand flag: 'Z' in -Z"},
		{"flag_of_another_command", words("pr list --draft --fill"), "unknown flag: --fill"},
		{"json_with_no_value", words("pr list --state open --json"), "flag needs an argument: --json"},
		{"state_with_no_value", words("pr list --state"), "flag needs an argument: --state"},
		{"flag_after_a_value_flag", words("pr list --state open --nope"), "unknown flag: --nope"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			stderr, ok := ghcontract.Check(tc.args)

			assert.False(t, ok)
			assert.Equal(t, tc.want, stderr)
		})
	}
}

func TestCheck_TakesTheNextArgumentAsTheValueOfAFlag(t *testing.T) {
	t.Parallel()

	for _, call := range [][]string{
		{"pr", "edit", "7", "--body", "--nope is text here"},
		{"pr", "edit", "7", "--body=--nope"},
		{"pr", "list", "-", "x", "--draft"},
		{"api", "x", "-f", "-q=1"},
	} {
		stderr, ok := ghcontract.Check(call)

		assert.True(t, ok, "%v: %s", call, stderr)
	}
}

func TestCheck_RefusesAShapeOutsideTheTable(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		words("pr nope --json number"),
		words("gist list"),
		words("pr"),
		{},
	} {
		stderr, ok := ghcontract.Check(args)

		assert.False(t, ok, "%v", args)
		assert.Contains(t, stderr, "is not in the contract table")
	}

	stderr, _ := ghcontract.Check(words("gist list --limit 3"))
	assert.Equal(t, "gh gist list is not in the contract table of the pinned gh: add its fields and flags to gh-2.101.0.txt", stderr)
}
