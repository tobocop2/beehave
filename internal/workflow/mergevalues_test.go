package workflow_test

import (
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

func step(t *testing.T, name, status string) starlark.Value {
	t.Helper()

	return dictOf(t, map[string]starlark.Value{"name": starlark.String(name), "status": starlark.String(status)})
}

func TestGateSteps_RefusesWhatTheRunRefuses(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		value starlark.Value
		want  string
	}{
		{"absent", nil, "names no gate steps"},
		{"a value the encoder refuses", starlark.NewBuiltin("f", nil), "must be the steps of a gate"},
		{"empty", starlark.NewList(nil), "`steps` is empty"},
		{"not a step", starlark.NewList([]starlark.Value{starlark.MakeInt(1)}), "must be the steps of a gate"},
		{"failed step", starlark.NewList([]starlark.Value{step(t, "a", "fail")}), `the gate step "a" is fail`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			require.ErrorContains(t, workflow.GateSteps(tc.value), tc.want)
		})
	}

	require.NoError(t, workflow.GateSteps(starlark.NewList([]starlark.Value{step(t, "a", "pass")})))
}

func TestRequiredSteps_ARequiredNameWithNoResultRefusesAndAnEmptyRequirementAsksForNothing(t *testing.T) {
	t.Parallel()

	both := starlark.NewList([]starlark.Value{step(t, "a", "pass"), step(t, "b", "pass")})

	require.NoError(t, workflow.RequiredSteps(nil, []string{"a"}))
	require.NoError(t, workflow.RequiredSteps(both, nil))
	require.NoError(t, workflow.RequiredSteps(both, []string{"a", "b"}))
	require.ErrorContains(t, workflow.RequiredSteps(both, []string{"a", "c"}), `has the step "c"`)
	require.ErrorContains(t, workflow.RequiredSteps(starlark.NewList([]starlark.Value{starlark.MakeInt(1)}), []string{"a"}), `has the step "a"`)
	require.ErrorContains(t, workflow.RequiredSteps(starlark.NewBuiltin("f", nil), []string{"a"}), "must be the steps of a gate")
}

func TestGatedTree_ReadsTheTreeTheRunReads(t *testing.T) {
	t.Parallel()

	tree, err := workflow.GatedTree(nil)
	require.NoError(t, err)
	assert.Empty(t, tree)

	tree, err = workflow.GatedTree(starlark.String("abc"))
	require.NoError(t, err)
	assert.Equal(t, "abc", tree)

	_, err = workflow.GatedTree(starlark.String(""))
	require.ErrorContains(t, err, "`tree` is empty")

	_, err = workflow.GatedTree(starlark.MakeInt(1))
	require.ErrorContains(t, err, "`tree` must be text, not int")
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
		{"not a struct", starlark.NewList([]starlark.Value{starlark.MakeInt(1)}), "`prs` holds a int"},
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
