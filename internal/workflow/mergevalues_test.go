package workflow_test

import (
	"maps"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	"github.com/tobocop2/beehave/internal/workflow"
)

// dictOf makes a dict with text keys.
func dictOf(t *testing.T, entries map[string]starlark.Value) *starlark.Dict {
	t.Helper()

	dict := starlark.NewDict(len(entries))

	for key, value := range entries {
		require.NoError(t, dict.SetKey(starlark.String(key), value))
	}

	return dict
}

func pr(fields starlark.StringDict) starlark.Value {
	return starlarkstruct.FromStringDict(starlarkstruct.Default, fields)
}

func TestPullRequests_RefusesWhatTheRunRefuses(t *testing.T) {
	t.Parallel()

	text := func(s string) starlark.Value { return starlark.String(s) }
	cases := []struct {
		name  string
		value starlark.Value
		want  string
	}{
		{"not a list", starlark.String("x"), "`prs` must be a list"},
		{"empty", starlark.NewList(nil), "`prs` is empty"},
		{"not a struct", starlark.NewList([]starlark.Value{starlark.MakeInt(1)}), "prs[0]: the member has the type int, not struct"},
		{"no item", starlark.NewList([]starlark.Value{pr(starlark.StringDict{"head": text("h")})}), "has no `item`"},
		{"no head", starlark.NewList([]starlark.Value{pr(starlark.StringDict{"item": text("i")})}), "has no `head`"},
		{"empty head", starlark.NewList([]starlark.Value{pr(starlark.StringDict{"item": text("i"), "head": text("")})}), "`head` of a member of `prs` must be text that is not empty"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := workflow.PullRequests(tc.value)
			require.ErrorContains(t, err, tc.want)
		})
	}

	got, err := workflow.PullRequests(starlark.NewList([]starlark.Value{pr(starlark.StringDict{"item": text("i"), "head": text("h")})}))
	require.NoError(t, err)
	assert.Equal(t, []workflow.PullRequest{{Item: "i", Head: "h"}}, got)
}

func TestPullRequests_ApprovedIsABoolThatDefaultsToFalse(t *testing.T) {
	t.Parallel()

	member := func(extra starlark.StringDict) starlark.Value {
		fields := starlark.StringDict{"item": starlark.String("i"), "head": starlark.String("h")}
		maps.Copy(fields, extra)

		return starlark.NewList([]starlark.Value{pr(fields)})
	}

	cases := []struct {
		name         string
		extra        starlark.StringDict
		wantApproved bool
		wantErr      string
	}{
		{"absent is not approved", nil, false, ""},
		{"a bool true is approved", starlark.StringDict{"approved": starlark.True}, true, ""},
		{"a non-bool is refused", starlark.StringDict{"approved": starlark.String("yes")}, false, "`approved` of a member of `prs` must be a bool, not string. Use approved = True or approved = False"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := workflow.PullRequests(member(tc.extra))
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, []workflow.PullRequest{{Item: "i", Head: "h", Approved: tc.wantApproved}}, got)
		})
	}
}

func TestOwnerIdentity_RefusesWhatTheRunRefuses(t *testing.T) {
	t.Parallel()

	_, _, err := workflow.OwnerIdentity(starlark.String("x"))
	require.ErrorContains(t, err, "`own` must be a dict")

	bad := dictOf(t, map[string]starlark.Value{"logins": starlark.NewList([]starlark.Value{starlark.MakeInt(1)})})
	_, _, err = workflow.OwnerIdentity(bad)
	require.ErrorContains(t, err, "own.logins must hold strings only")

	logins, emails, err := workflow.OwnerIdentity(starlark.None)
	require.NoError(t, err)
	assert.Empty(t, logins)
	assert.Empty(t, emails)

	good := dictOf(t, map[string]starlark.Value{
		"logins": starlark.NewList([]starlark.Value{starlark.String("me")}),
		"emails": starlark.NewList([]starlark.Value{starlark.String("a@b")}),
	})
	logins, emails, err = workflow.OwnerIdentity(good)
	require.NoError(t, err)
	assert.Equal(t, []string{"me"}, logins)
	assert.Equal(t, []string{"a@b"}, emails)
}

func TestPullRequests_OwnDraftIsTheNumberOfTheDraftOfAMemberAndDefaultsToZero(t *testing.T) {
	t.Parallel()

	member := func(extra starlark.StringDict) starlark.Value {
		fields := starlark.StringDict{"item": starlark.String("i"), "head": starlark.String("h")}
		maps.Copy(fields, extra)

		return starlark.NewList([]starlark.Value{pr(fields)})
	}

	got, err := workflow.PullRequests(member(nil))
	require.NoError(t, err)
	assert.Equal(t, []workflow.PullRequest{{Item: "i", Head: "h"}}, got)

	got, err = workflow.PullRequests(member(starlark.StringDict{"own_draft": starlark.MakeInt(7)}))
	require.NoError(t, err)
	assert.Equal(t, []workflow.PullRequest{{Item: "i", Head: "h", OwnDraft: 7}}, got)

	_, err = workflow.PullRequests(member(starlark.StringDict{"own_draft": starlark.MakeInt(0)}))
	require.ErrorContains(t, err, "own_draft must be the number of a pull request")
}

func TestOwnDraft_ALeftOutKeywordIsZeroAndAWholeNumberOfOneOrMoreIsTheNumber(t *testing.T) {
	t.Parallel()

	got, err := workflow.OwnDraft(nil)
	require.NoError(t, err)
	assert.Zero(t, got)

	got, err = workflow.OwnDraft(starlark.MakeInt(7))
	require.NoError(t, err)
	assert.Equal(t, 7, got)

	for _, bad := range []starlark.Value{starlark.MakeInt(0), starlark.MakeInt(-5), starlark.String("7"), starlark.None, starlark.MakeInt64(1 << 62).Mul(starlark.MakeInt64(8))} {
		_, err = workflow.OwnDraft(bad)
		require.ErrorContains(t, err, "own_draft must be the number of a pull request", bad.String())
	}
}
