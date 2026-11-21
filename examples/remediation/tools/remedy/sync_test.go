package remedy_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehave/examples/remediation/tools/remedy"
	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/forge"
	"github.com/tobocop2/beehave/internal/model"
)

const (
	roundBase = "integration/r1"
	roundTip  = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	syncTip   = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	syncRef   = "sync/integration/r1/11111111"
)

// syncWorld is a fake forge for sync. The trunk is at tip and the round base at roundTip. The sync branch is at syncTip once it exists.
// ahead answers every compare. merge is what a branch merge returns. down makes the first compare fail.
type syncWorld struct {
	*forgeWorld
	ahead int
	merge error
	down  error
	prs   []model.PullRequest
	view  model.PullRequest
	// created holds each pull request that the sync opened.
	created []model.NewPull
}

func (w *syncWorld) BranchRef(_ context.Context, branch string) (string, bool, error) {
	switch branch {
	case "main":
		return tip, true, nil
	case roundBase:
		return roundTip, true, nil
	case syncRef:
		return syncTip, true, nil
	default:
		return "", false, nil
	}
}

func (w *syncWorld) PRList(context.Context, int, ...string) ([]model.PullRequest, error) {
	return w.prs, nil
}

func (w *syncWorld) PRView(context.Context, int, ...string) (model.PullRequest, error) {
	return w.view, nil
}

func (w *syncWorld) CreatePR(_ context.Context, req model.NewPull) (int, error) {
	w.created = append(w.created, req)

	return 4, nil
}

func (*syncWorld) ClosePR(context.Context, int, string) error { return nil }

func (*syncWorld) MergePR(context.Context, int, string) error { return nil }

func (*syncWorld) SetDraft(context.Context, int, bool) error { return nil }

func (w *syncWorld) CommitsAhead(context.Context, string, string) (int, error) {
	return w.ahead, w.down
}

func (w *syncWorld) MergeBranch(context.Context, string, string) (string, error) {
	return syncTip, w.merge
}

func syncDeps(world remedy.Forge) remedy.Deps {
	return remedy.Deps{
		Git:   origin("git@github.com:owner/name.git", nothing),
		Dir:   ".",
		Forge: func(string) (remedy.Forge, error) { return world, nil },
	}
}

func newSyncWorld(ahead int) *syncWorld {
	open, base, head := "OPEN", roundBase, syncRef

	return &syncWorld{
		forgeWorld: &forgeWorld{state: "MERGED"},
		ahead:      ahead,
		view:       model.PullRequest{State: &open, BaseRefName: &base, HeadRefName: &head},
	}
}

func TestSync_CheckShowsTheStateOfTheMoveOnOneLine(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		world *syncWorld
		want  string
		code  int
	}{
		"current": {newSyncWorld(0), "state=current\n", remedy.ExitPass},
		"synced":  {newSyncWorld(1), "state=synced branch=" + syncRef + " head=" + syncTip + " base_tip=" + roundTip + " pr=4\n", remedy.ExitPass},
		"conflict": {
			func() *syncWorld {
				w := newSyncWorld(1)
				w.merge = &forge.Error{Message: "no", Err: forge.ErrMergeConflict}

				return w
			}(),
			"state=conflict branch=" + syncRef + " source=main base_tip=" + roundTip + "\n", remedy.ExitPass,
		},
	} {
		code, out, _ := run(t, syncDeps(test.world), "sync", "--base", roundBase)

		assert.Equal(t, test.code, code, name)
		assert.Equal(t, test.want, out, name)
	}
}

func TestSync_TheLabelFlagIsTheLabelOfTheSyncPullRequestNoFlagGivesTheDefaultAndAnEmptyOneAddsNone(t *testing.T) {
	t.Parallel()

	labeled := newSyncWorld(1)
	code, _, errOut := run(t, syncDeps(labeled), "sync", "--base", roundBase, "--label", "made-by-beehave")
	assert.Equal(t, remedy.ExitPass, code, errOut)
	assert.Equal(t, []model.NewPull{{
		Head: syncRef, Base: roundBase, Title: "Merge main into integration/r1",
		Body: "Brings the commits of main that integration/r1 lacks into the round.", Label: "made-by-beehave",
	}}, labeled.created)

	bare := newSyncWorld(1)
	code, _, errOut = run(t, syncDeps(bare), "sync", "--base", roundBase)
	assert.Equal(t, remedy.ExitPass, code, errOut)
	assert.Equal(t, config.DefaultPRLabel, bare.created[0].Label, "no flag gives the default of the project key")

	none := newSyncWorld(1)
	code, _, errOut = run(t, syncDeps(none), "sync", "--base", roundBase, "--label", "")
	assert.Equal(t, remedy.ExitPass, code, errOut)
	assert.Empty(t, none.created[0].Label)
}

func TestSync_LandShowsTheStateAndRefusesAMovedBaseOrHead(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		head, baseTip string
		want          string
		code          int
	}{
		"landed":     {syncTip, roundTip, "state=landed", remedy.ExitPass},
		"stale":      {syncTip, syncTip, "state=stale", remedy.ExitRefused},
		"head-moved": {roundTip, roundTip, "state=head-moved", remedy.ExitRefused},
	} {
		code, out, _ := run(t, syncDeps(newSyncWorld(1)), "sync", "land", "--pr", "4", "--head", test.head, "--base-tip", test.baseTip)

		assert.Equal(t, test.code, code, name)
		assert.Contains(t, out, test.want, name)
	}
}

func TestSync_AFailureOfTheForgeIsARefusalWithTheError(t *testing.T) {
	t.Parallel()

	world := newSyncWorld(1)
	world.down = errDown

	code, out, errOut := run(t, syncDeps(world), "sync", "--base", roundBase)

	assert.Equal(t, remedy.ExitRefused, code)
	assert.Empty(t, out)
	assert.Contains(t, errOut, "remedy sync: down")
}

func TestSync_AForgeThatCannotMergeABranchIsARefusal(t *testing.T) {
	t.Parallel()

	code, _, errOut := run(t, syncDeps(&forgeWorld{}), "sync", "--base", roundBase)

	assert.Equal(t, remedy.ExitRefused, code)
	assert.Contains(t, errOut, "cannot sync a branch")
}

func TestSync_AnOriginOrForgeThatCannotBeOpenedIsARefusal(t *testing.T) {
	t.Parallel()

	failing := func([]string) model.CommandResult { return model.CommandResult{ExitCode: 1} }
	noForge := syncDeps(nil)
	noForge.Forge = func(string) (remedy.Forge, error) { return nil, errDown }

	for name, deps := range map[string]remedy.Deps{
		"git fails": {Git: git(failing)},
		"no forge":  noForge,
	} {
		code, _, errOut := run(t, deps, "sync", "--base", roundBase)

		assert.Equal(t, remedy.ExitRefused, code, name)
		assert.Contains(t, errOut, "remedy sync:", name)
	}
}

func TestSync_ABadArgumentShowsTheUsageAndIsARefusal(t *testing.T) {
	t.Parallel()

	for name, args := range map[string][]string{
		"no base":          {"sync"},
		"empty trunk":      {"sync", "--base", roundBase, "--trunk", ""},
		"extra argument":   {"sync", "--base", roundBase, "more"},
		"land without pr":  {"sync", "land", "--head", syncTip, "--base-tip", roundTip},
		"land without sha": {"sync", "land", "--pr", "4", "--base-tip", roundTip},
		"land without tip": {"sync", "land", "--pr", "4", "--head", syncTip},
	} {
		code, _, errOut := run(t, syncDeps(newSyncWorld(1)), args...)

		assert.Equal(t, remedy.ExitRefused, code, name)
		assert.Contains(t, errOut, "usage: remedy sync --base <branch> [--trunk <branch>] [--label <name>]\n       remedy sync land --pr <number> --head <sha> --base-tip <sha>\n", name)
	}
}

func TestSync_TheCommandTableNamesBothCallsOfTheSync(t *testing.T) {
	t.Parallel()

	_, out, errOut := run(t, syncDeps(newSyncWorld(1)))

	assert.Contains(t, out+errOut, "sync --base <branch> [--trunk <branch>] [--label <name>] | sync land --pr <number> --head <sha> --base-tip <sha>")
}

func TestSync_AFlagThatTheCallDoesNotKnowIsARefusal(t *testing.T) {
	t.Parallel()

	for name, args := range map[string][]string{
		"check with a land flag": {"sync", "--pr", "4"},
		"land with a check flag": {"sync", "land", "--base", roundBase},
	} {
		code, _, errOut := run(t, syncDeps(newSyncWorld(1)), args...)

		assert.Equal(t, remedy.ExitRefused, code, name)
		assert.Contains(t, errOut, "flag provided but not defined", name)
	}
}
