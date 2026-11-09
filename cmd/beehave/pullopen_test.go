package main

import (
	"context"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/flow"
	"github.com/tobocop2/beehave/internal/forge"
	"github.com/tobocop2/beehave/internal/model"
)

// openForge answers the reads of pulls.Open and records each write. The methods it does not define panic.
type openForge struct {
	forge.Forge

	open     map[string]int // branch -> number of its open pull request
	merged   map[string]int
	def      string
	bodies   map[int]string
	stacks   map[int]string
	created  []model.NewPull
	linked   []string
	setBody  []string
	linkMake string // the stack id that a link gives both pull requests
	// bases is the base branch of each pull request; a pull request not in it targets round-1.
	bases     map[int]string
	linkBases []string // the base that each link was given
	unstacked []int
	// A link with no base moves the base of the parent to main, as the forge does. movesFirst makes the first link do it whatever its base, and stubborn makes every link do it.
	movesFirst bool
	stubborn   bool
	// fail makes the call of that key fail with errFake. A key is the method, then the argument that tells calls apart: "PRList:open:bb-4", "StackID:20", "StackID:20:linked" (after a link).
	fail map[string]bool
}

func (f *openForge) failed(key string) error {
	if f.fail[key] {
		return errFake
	}

	return nil
}

func (f *openForge) PRList(_ context.Context, _ int, args ...string) ([]model.PullRequest, error) {
	if err := f.failed("PRList:" + args[3] + ":" + args[1]); err != nil {
		return nil, err
	}

	pick := f.open
	if args[3] == "merged" {
		pick = f.merged
	}

	if n, ok := pick[args[1]]; ok {
		return []model.PullRequest{{Number: n}}, nil
	}

	return nil, nil
}

func (f *openForge) DefaultBranch(context.Context) (string, error) {
	return f.def, f.failed("DefaultBranch")
}

func (f *openForge) PRBody(_ context.Context, pr int) (string, error) {
	return f.bodies[pr], f.failed("PRBody")
}

func (f *openForge) SetBody(_ context.Context, pr int, body string) error {
	f.setBody = append(f.setBody, body)
	f.bodies[pr] = body

	return f.failed("SetBody")
}

func (f *openForge) StackID(_ context.Context, pr int) (string, error) {
	key := "StackID:" + strconv.Itoa(pr)
	if len(f.linked) > 0 {
		key += ":linked"
	}

	return f.stacks[pr], f.failed(key)
}

func (f *openForge) CreatePR(_ context.Context, req model.NewPull) (int, error) {
	if err := f.failed("CreatePR"); err != nil {
		return 0, err
	}

	f.created = append(f.created, req)
	f.open[req.Head] = 50 + len(f.created)
	f.bodies[f.open[req.Head]] = req.Body

	return f.open[req.Head], nil
}

func (f *openForge) LinkStack(_ context.Context, parent, child int, base string) error {
	f.linked = append(f.linked, strconv.Itoa(parent)+">"+strconv.Itoa(child))
	f.linkBases = append(f.linkBases, base)

	if len(f.linked) == 2 {
		if err := f.failed("LinkStack:2"); err != nil {
			return err
		}
	}
	f.stacks[parent], f.stacks[child] = f.linkMake, f.linkMake

	if base == "" || f.stubborn || (f.movesFirst && len(f.linked) == 1) {
		f.bases[parent] = "main"
	}

	return f.failed("LinkStack")
}

func (f *openForge) Unstack(_ context.Context, pr int) error {
	f.unstacked = append(f.unstacked, pr)
	f.stacks = map[int]string{}

	return f.failed("Unstack")
}

func (f *openForge) SetBase(_ context.Context, pr int, base string) error {
	f.bases[pr] = base

	return f.failed("SetBase")
}

func (f *openForge) PRView(_ context.Context, pr int, _ ...string) (model.PullRequest, error) {
	key := "PRView:" + strconv.Itoa(pr)
	if len(f.linked) > 0 {
		key += ":linked" + strconv.Itoa(len(f.linked))
	}

	base, ok := f.bases[pr]
	if !ok {
		base = "round-1"
	}

	return model.PullRequest{BaseRefName: &base}, f.failed(key)
}

func newOpenForge() *openForge {
	return &openForge{open: map[string]int{}, merged: map[string]int{}, def: "main", bodies: map[int]string{}, stacks: map[int]string{}, bases: map[int]string{}, linkMake: "S_1"}
}

func openOver(f forge.Forge) pulls {
	return pulls{projects: map[string]config.Project{"p": {}}, forge: func(model.Project) (forge.Forge, error) { return f, nil }}
}

func TestPullsOpen_OpensOneDraftAndAResumeFindsIt(t *testing.T) {
	t.Parallel()

	f := newOpenForge()
	req := flow.OpenRequest{Title: "Fix 5", Body: "text", Issue: 5, Base: "round-1"}

	first, err := openOver(f).Open(t.Context(), "p", "bb-5", req)
	require.NoError(t, err)
	assert.True(t, first.Created)

	again, err := openOver(f).Open(t.Context(), "p", "bb-5", req)
	require.NoError(t, err)
	assert.False(t, again.Created)
	assert.Equal(t, first.Number, again.Number)
	require.Len(t, f.created, 1, "a resume opens no second pull request")
	assert.Equal(t, model.NewPull{Head: "bb-5", Base: "round-1", Title: "Fix 5", Body: "text\n\nAddresses #5"}, f.created[0])
	assert.Empty(t, f.setBody, "the body already holds the text, so nothing is written again")
}

func TestPullsOpen_TheClosingKeywordFollowsTheBase(t *testing.T) {
	t.Parallel()

	for base, want := range map[string]string{"main": "text\n\nCloses #5", "round-1": "text\n\nAddresses #5"} {
		f := newOpenForge()
		_, err := openOver(f).Open(t.Context(), "p", "bb-5", flow.OpenRequest{Title: "t", Body: "text", Issue: 5, Base: base})
		require.NoError(t, err)
		require.Len(t, f.created, 1)
		assert.Equal(t, want, f.created[0].Body, base)
	}

	f := newOpenForge()
	_, err := openOver(f).Open(t.Context(), "p", "bb-5", flow.OpenRequest{Title: "t", Body: "text", Base: "main"})
	require.NoError(t, err)
	assert.Equal(t, "text", f.created[0].Body, "no issue, no keyword")
}

func TestPullsOpen_AResumeKeepsTheBodyAPersonWroteAndAddsOnlyTheClosingLine(t *testing.T) {
	t.Parallel()

	f := newOpenForge()
	f.open["bb-5"] = 9
	f.bodies[9] = "what a person wrote\n\nsee also #3"

	got, err := openOver(f).Open(t.Context(), "p", "bb-5", flow.OpenRequest{Title: "t", Body: "body of the workflow", Issue: 5, Base: "round-1"})
	require.NoError(t, err)
	assert.False(t, got.Created)
	assert.Equal(t, []string{"what a person wrote\n\nsee also #3\n\nAddresses #5"}, f.setBody, "the workflow body is for a new pull request only")
}

func TestPullsOpen_AWrongKeywordIsCorrectedInPlaceAndEveryOtherLineStays(t *testing.T) {
	t.Parallel()

	f := newOpenForge()
	f.open["bb-5"] = 9
	f.bodies[9] = "intro\nCloses #5\nCloses #7\noutro"

	_, err := openOver(f).Open(t.Context(), "p", "bb-5", flow.OpenRequest{Title: "t", Issue: 5, Base: "round-1"})
	require.NoError(t, err)
	assert.Equal(t, []string{"intro\nAddresses #5\nCloses #7\noutro"}, f.setBody)

	// A body with the right line, and a call with no issue, write nothing.
	_, err = openOver(f).Open(t.Context(), "p", "bb-5", flow.OpenRequest{Title: "t", Issue: 5, Base: "round-1"})
	require.NoError(t, err)

	_, err = openOver(f).Open(t.Context(), "p", "bb-5", flow.OpenRequest{Title: "t", Base: "round-1"})
	require.NoError(t, err)
	assert.Len(t, f.setBody, 1)
}

func TestPullsOpen_AResumeAfterACrashBetweenTheCreateAndTheLinkLinksTheStack(t *testing.T) {
	t.Parallel()

	f := newOpenForge()
	f.open["bb-4"] = 20
	f.open["bb-5"] = 9
	f.bodies[9] = "Addresses #5"
	f.stacks[20], f.stacks[9] = "S_0", ""

	got, err := openOver(f).Open(t.Context(), "p", "bb-5", flow.OpenRequest{Title: "t", Issue: 5, Base: "round-1", Parent: "bb-4"})
	require.NoError(t, err)
	assert.False(t, got.Created)
	assert.Equal(t, 9, got.Number)
	assert.Equal(t, []string{"20>9"}, f.linked)
	assert.Equal(t, "S_1", got.Stack)
}

func TestPullsOpen_AStackedChildSaysAddressesEvenWhenTheGivenBaseIsTheDefaultBranch(t *testing.T) {
	t.Parallel()

	f := newOpenForge()
	f.open["bb-4"] = 20

	_, err := openOver(f).Open(t.Context(), "p", "bb-5", flow.OpenRequest{Title: "t", Issue: 5, Base: "main", Parent: "bb-4"})
	require.NoError(t, err)
	assert.Equal(t, "bb-4", f.created[0].Base)
	assert.Equal(t, "Addresses #5", f.created[0].Body)
}

func TestPullsOpen_ADependentGoesIntoTheStackOfItsParent(t *testing.T) {
	t.Parallel()

	f := newOpenForge()
	f.open["bb-4"] = 20

	got, err := openOver(f).Open(t.Context(), "p", "bb-5", flow.OpenRequest{Title: "t", Issue: 5, Base: "round-1", Parent: "bb-4"})
	require.NoError(t, err)
	assert.Equal(t, "S_1", got.Stack)
	assert.Equal(t, []string{"20>51"}, f.linked)
	assert.Equal(t, []string{"round-1"}, f.linkBases, "the first link of a stack carries the base of the round")
	assert.NotEqual(t, "main", f.bases[20], "the link leaves the bottom on the branch of the round")
	assert.Equal(t, "bb-4", f.created[0].Base, "the dependent is based on the branch of its parent")

	// A resume finds both pull requests in one stack and links nothing.
	again, err := openOver(f).Open(t.Context(), "p", "bb-5", flow.OpenRequest{Title: "t", Issue: 5, Base: "round-1", Parent: "bb-4"})
	require.NoError(t, err)
	assert.Equal(t, "S_1", again.Stack)
	assert.Len(t, f.linked, 1)
}

func TestPullsOpen_ALinkThatMovesTheBaseIsUnstackedRestoredAndLinkedAgain(t *testing.T) {
	t.Parallel()

	f := newOpenForge()
	f.open["bb-4"] = 20
	f.movesFirst = true

	got, err := openOver(f).Open(t.Context(), "p", "bb-5", flow.OpenRequest{Title: "t", Base: "round-1", Parent: "bb-4"})
	require.NoError(t, err)
	assert.Equal(t, "S_1", got.Stack)
	assert.Equal(t, []int{20}, f.unstacked)
	assert.Equal(t, []string{"20>51", "20>51"}, f.linked)
	assert.Equal(t, []string{"round-1", "round-1"}, f.linkBases, "the second link carries the base too")
	assert.Equal(t, "round-1", f.bases[20], "the base is back on the branch of the round")
}

func TestPullsOpen_ALinkThatMovesTheBaseTwiceIsAnError(t *testing.T) {
	t.Parallel()

	f := newOpenForge()
	f.open["bb-4"] = 20
	f.stubborn = true

	_, err := openOver(f).Open(t.Context(), "p", "bb-5", flow.OpenRequest{Title: "t", Base: "round-1", Parent: "bb-4"})
	require.ErrorIs(t, err, errBaseMoved)
	assert.Len(t, f.linked, 2, "the link is repaired once and never a third time")
}

func TestPullsOpen_AParentInAStackLinksWithNoBase(t *testing.T) {
	t.Parallel()

	f := newOpenForge()
	f.open["bb-4"] = 20
	f.stacks[20] = "S_1"
	f.bases[20] = "bb-3"

	got, err := openOver(f).Open(t.Context(), "p", "bb-5", flow.OpenRequest{Title: "t", Base: "round-1", Parent: "bb-4"})
	require.NoError(t, err)
	assert.Equal(t, "S_1", got.Stack)
	assert.Equal(t, []string{""}, f.linkBases, "only the first link of a stack names the base")
}

func TestPullsOpen_AParentWithNoBaseInTheAnswerIsAnError(t *testing.T) {
	t.Parallel()

	f := newOpenForge()
	f.open["bb-4"] = 20
	f.bases[20] = ""

	_, err := openOver(f).Open(t.Context(), "p", "bb-5", flow.OpenRequest{Title: "t", Base: "round-1", Parent: "bb-4"})
	require.ErrorIs(t, err, errNoBase)
}

func TestPullsOpen_AStackThatDoesNotShowOneIdIsAnError(t *testing.T) {
	t.Parallel()

	f := newOpenForge()
	f.open["bb-4"] = 20
	f.linkMake = ""

	_, err := openOver(f).Open(t.Context(), "p", "bb-5", flow.OpenRequest{Title: "t", Base: "round-1", Parent: "bb-4"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not in one stack")
}

func TestPullsOpen_AParentThatMergedOrHasNoPullRequest(t *testing.T) {
	t.Parallel()

	f := newOpenForge()
	f.merged["bb-4"] = 20

	got, err := openOver(f).Open(t.Context(), "p", "bb-5", flow.OpenRequest{Title: "t", Base: "round-1", Parent: "bb-4"})
	require.NoError(t, err)
	assert.Empty(t, got.Stack)
	assert.Empty(t, f.linked)
	assert.Equal(t, "round-1", f.created[0].Base, "a merged parent leaves the base as given")

	_, err = openOver(newOpenForge()).Open(t.Context(), "p", "bb-5", flow.OpenRequest{Title: "t", Base: "round-1", Parent: "bb-9"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bb-9")
}

func TestPullsOpen_AFailedCallOfTheForgeIsAnErrorAndNeverAGuess(t *testing.T) {
	t.Parallel()

	tests := []struct {
		key   string
		setup func(f *openForge)
	}{
		{"PRList:open:bb-4", func(*openForge) {}},
		{"PRList:merged:bb-4", func(*openForge) {}},
		{"DefaultBranch", func(f *openForge) { f.open["bb-4"] = 20 }},
		{"PRList:open:bb-5", func(f *openForge) { f.open["bb-4"] = 20 }},
		{"CreatePR", func(f *openForge) { f.open["bb-4"] = 20 }},
		{"PRBody", func(f *openForge) { f.open["bb-4"], f.open["bb-5"] = 20, 9 }},
		{"SetBody", func(f *openForge) { f.open["bb-4"], f.open["bb-5"] = 20, 9; f.bodies[9] = "old" }},
		{"StackID:20", func(f *openForge) { f.open["bb-4"] = 20 }},
		{"StackID:51", func(f *openForge) { f.open["bb-4"] = 20 }},
		{"LinkStack", func(f *openForge) { f.open["bb-4"] = 20 }},
		{"StackID:20:linked", func(f *openForge) { f.open["bb-4"] = 20 }},
		{"PRView:20", func(f *openForge) { f.open["bb-4"] = 20 }},
		{"PRView:20:linked1", func(f *openForge) { f.open["bb-4"] = 20 }},
		{"PRView:20:linked2", func(f *openForge) { f.open["bb-4"] = 20; f.movesFirst = true }},
		{"LinkStack:2", func(f *openForge) { f.open["bb-4"] = 20; f.movesFirst = true }},
		{"Unstack", func(f *openForge) { f.open["bb-4"] = 20; f.movesFirst = true }},
		{"SetBase", func(f *openForge) { f.open["bb-4"] = 20; f.movesFirst = true }},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			t.Parallel()

			f := newOpenForge()
			f.fail = map[string]bool{tt.key: true}
			tt.setup(f)

			_, err := openOver(f).Open(t.Context(), "p", "bb-5", flow.OpenRequest{Title: "t", Body: "x", Issue: 5, Base: "round-1", Parent: "bb-4"})
			require.ErrorIs(t, err, errFake)
		})
	}
}

func TestPullsOpen_AProjectThatTheConfigLacksIsRefused(t *testing.T) {
	t.Parallel()

	_, err := openOver(newOpenForge()).Open(t.Context(), "nope", "bb-5", flow.OpenRequest{Title: "t", Base: "main"})
	require.ErrorIs(t, err, errUnknownProject)
}

type plainForge struct{ forge.Forge }

func (plainForge) PRList(context.Context, int, ...string) ([]model.PullRequest, error) {
	return nil, nil
}

func TestPullsOpen_AForgeThatCannotOpenIsAnError(t *testing.T) {
	t.Parallel()

	_, err := openOver(plainForge{}).Open(t.Context(), "p", "bb-5", flow.OpenRequest{Title: "t", Base: "main"})
	require.ErrorIs(t, err, errNoOptionalRead)
}
