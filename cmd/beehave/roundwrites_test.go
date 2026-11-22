package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/exectest"
	"github.com/tobocop2/beehave/internal/flow"
	"github.com/tobocop2/beehave/internal/forge"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/roundcut"
	"github.com/tobocop2/beehave/internal/roundintegration"
	"github.com/tobocop2/beehave/internal/roundsync"
	"github.com/tobocop2/beehave/internal/takeover"
)

var errRoundForge = errors.New("the forge answered 502")

// roundForge is a forge that holds each optional write side of a round. Every read that the tests reach answers err when it is set. The methods it does not define panic.
type roundForge struct {
	forge.Forge

	err    error
	ahead  int
	status string
	placed []model.Card

	// created holds each pull request that a call opened.
	created []model.NewPull
}

func (f *roundForge) PRView(context.Context, int, ...string) (model.PullRequest, error) {
	return model.PullRequest{}, f.err
}

func (f *roundForge) PRList(context.Context, int, ...string) ([]model.PullRequest, error) {
	return nil, f.err
}

func (f *roundForge) CommitsAhead(context.Context, string, string) (int, error) {
	return f.ahead, f.err
}

func (f *roundForge) MergeBranch(context.Context, string, string) (string, error) { return "", f.err }

func (f *roundForge) ClosePR(context.Context, int, string) error { return f.err }

func (f *roundForge) CreatePR(_ context.Context, req model.NewPull) (int, error) {
	f.created = append(f.created, req)

	return 11, f.err
}

func (f *roundForge) BranchRef(context.Context, string) (string, bool, error) {
	return "tip", true, f.err
}

func (f *roundForge) SetBody(context.Context, int, string) error { return f.err }

func (f *roundForge) PlaceCard(_ context.Context, card model.Card) error {
	f.placed = append(f.placed, card)

	return f.err
}

func (f *roundForge) CardStatus(context.Context, model.Card) (string, error) { return f.status, nil }

// roundPulls returns the pulls of the project p, which has the forge built.
func roundPulls(built forge.Forge, label string) pulls {
	return pulls{
		projects: map[string]config.Project{"p": {Project: model.Project{Name: "p", PRLabel: label, Repo: "acme/app"}}},
		forge:    func(model.Project) (forge.Forge, error) { return built, nil },
	}
}

func TestRoundWrites_ASyncThatFindsNothingAheadIsCurrent(t *testing.T) {
	t.Parallel()

	writes := roundWrites{pulls: roundPulls(&roundForge{}, "")}

	result, err := writes.Sync(t.Context(), "p", roundsync.Request{Base: "round-i", Trunk: "main"})

	require.NoError(t, err)
	assert.Equal(t, roundsync.Current, result.State)
}

func TestRoundWrites_AFailedReadOfTheForgeEndsEachWriteWithItsError(t *testing.T) {
	t.Parallel()

	writes := roundWrites{pulls: roundPulls(&roundForge{err: errRoundForge}, "")}
	ctx := t.Context()

	_, err := writes.Sync(ctx, "p", roundsync.Request{Base: "round-i", Trunk: "main"})
	require.ErrorIs(t, err, errRoundForge)

	_, err = writes.Land(ctx, "p", roundsync.LandRequest{PR: 7, Head: "h", BaseTip: "t"})
	require.ErrorIs(t, err, errRoundForge)

	_, err = writes.Integrate(ctx, "p", roundintegration.Request{Base: "round-i", Into: "main"})
	require.ErrorIs(t, err, errRoundForge)

	_, err = writes.Place(ctx, "p", model.Card{Board: 3, Status: "Done", Number: 7, Pull: true})
	require.ErrorIs(t, err, errRoundForge)
}

func TestRoundWrites_ACutThatCannotReadThePullRequestIsRefusedWithLines(t *testing.T) {
	t.Parallel()

	writes := roundWrites{pulls: roundPulls(&roundForge{err: errRoundForge}, "")}

	result, err := writes.Cut(t.Context(), "p", roundcut.Request{Cut: roundcut.Cut{Roman: "ii", OldPR: 9, OldBase: "round-i", NewBase: "round-ii", Trunk: "main"}})

	require.NoError(t, err)
	assert.Equal(t, roundcut.Refused, result.Stop)
	assert.NotEmpty(t, result.Lines)
}

func TestRoundWrites_APlacedCardReturnsTheColumnThatTheBoardShows(t *testing.T) {
	t.Parallel()

	built := &roundForge{status: "In Progress"}
	card := model.Card{Board: 3, Status: "Done", Number: 7, Pull: true}

	shown, err := roundWrites{pulls: roundPulls(built, "")}.Place(t.Context(), "p", card)

	require.NoError(t, err)
	assert.Equal(t, "In Progress", shown)
	assert.Equal(t, []model.Card{card}, built.placed)
}

func TestRoundWrites_AForgeWithoutAWriteSideRefusesTheWriteThatNeedsIt(t *testing.T) {
	t.Parallel()

	writes := roundWrites{pulls: roundPulls(plainForge{}, "")}
	ctx := t.Context()

	_, err := writes.Sync(ctx, "p", roundsync.Request{})
	require.ErrorIs(t, err, errNoOptionalWrite)

	_, err = writes.Land(ctx, "p", roundsync.LandRequest{})
	require.ErrorIs(t, err, errNoOptionalWrite)

	_, err = writes.Integrate(ctx, "p", roundintegration.Request{})
	require.ErrorIs(t, err, errNoOptionalWrite)

	_, err = writes.Place(ctx, "p", model.Card{})
	require.ErrorIs(t, err, errNoOptionalWrite)
}

func TestRoundWrites_AProjectThatTheConfigLacksIsRefusedByEachWrite(t *testing.T) {
	t.Parallel()

	writes := roundWrites{pulls: roundPulls(&roundForge{}, "")}
	ctx := t.Context()

	_, err := writes.Sync(ctx, "missing", roundsync.Request{})
	require.ErrorIs(t, err, errUnknownProject)

	_, err = writes.Land(ctx, "missing", roundsync.LandRequest{})
	require.ErrorIs(t, err, errUnknownProject)

	_, err = writes.Cut(ctx, "missing", roundcut.Request{})
	require.ErrorIs(t, err, errUnknownProject)

	_, err = writes.Integrate(ctx, "missing", roundintegration.Request{})
	require.ErrorIs(t, err, errUnknownProject)

	_, err = writes.Place(ctx, "missing", model.Card{})
	require.ErrorIs(t, err, errUnknownProject)

	_, err = writes.Take(ctx, "missing", flow.TakeRequest{})
	require.ErrorIs(t, err, errUnknownProject)
}

func TestRoundWrites_ALabelThatTheCallNamesWinsOverTheLabelOfTheProject(t *testing.T) {
	t.Parallel()

	writes := roundWrites{pulls: roundPulls(&roundForge{}, "beehave")}

	assert.Equal(t, "beehave", writes.label("p", ""))
	assert.Equal(t, "round", writes.label("p", "round"))
}

func TestRoundWrites_TheSyncAndTheIntegrationPullRequestsCarryThePRLabelOfTheProject(t *testing.T) {
	t.Parallel()

	built := &roundForge{ahead: 1}
	writes := roundWrites{pulls: roundPulls(built, "made-by-beehave")}
	ctx := t.Context()

	synced, err := writes.Sync(ctx, "p", roundsync.Request{Base: "round-i", Trunk: "main"})
	require.NoError(t, err)
	assert.Equal(t, roundsync.Synced, synced.State)

	_, err = writes.Integrate(ctx, "p", roundintegration.Request{Base: "round-i", Into: "main"})
	require.NoError(t, err)

	require.Len(t, built.created, 2)
	assert.Equal(t, "made-by-beehave", built.created[0].Label, "the sync pull request")
	assert.Equal(t, "made-by-beehave", built.created[1].Label, "the integration pull request")
}

// ghAs puts a gh on the PATH that prints the GH_TOKEN of its environment, and returns the folder of the program.
func ghAs(t *testing.T) string {
	t.Helper()

	folder := t.TempDir()
	require.NoError(t, exectest.Write(filepath.Join(folder, "gh"), "#!/bin/sh\nprintf '%s' \"${GH_TOKEN:-unset}\"\n"))
	t.Setenv("PATH", folder)
	t.Setenv("GH_TOKEN", "owner-token")

	return folder
}

func TestRoundWrites_TheGhOfATakeRunsWithTheTokenOfTheLoadedWriterAndNeverTheTokenOfTheOwner(t *testing.T) { //nolint:paralleltest // ghAs sets the environment of the process
	folder := ghAs(t)

	pull := roundPulls(&roundForge{}, "")
	project := pull.projects["p"]
	project.Credentials = map[model.Role]model.Credential{model.RoleWriter: {}}
	pull.projects["p"] = project
	pull.writers = &writers{byProject: map[string]writerRole{"p": {token: writerToken}}, env: map[string]string{"PATH": folder}}

	settings, err := roundWrites{pulls: pull}.takeSettings("p", flow.TakeRequest{})

	require.NoError(t, err)
	assert.Equal(t, writerToken, settings.Gh.Run(t.Context(), []string{"pr", "view"}).Stdout)
}

func TestRoundWrites_TheGhOfATakeInAProjectWithNoCredentialsRunsAsTheLoginOfTheOwner(t *testing.T) { //nolint:paralleltest // ghAs sets the environment of the process
	ghAs(t)

	settings, err := roundWrites{pulls: roundPulls(&roundForge{}, "")}.takeSettings("p", flow.TakeRequest{})

	require.NoError(t, err)
	assert.Equal(t, "owner-token", settings.Gh.Run(t.Context(), []string{"pr", "view"}).Stdout)
}

func TestRoundWrites_ATakeReadsThePullRequestThroughTheForgeOfTheProject(t *testing.T) {
	t.Parallel()

	writes := roundWrites{pulls: roundPulls(&roundForge{err: errRoundForge}, "")}

	_, err := writes.Take(t.Context(), "p", flow.TakeRequest{Request: takeover.Request{PR: 7, Mode: takeover.ModeApply}, Me: "owner", Idle: time.Hour, Grace: time.Hour})

	require.ErrorIs(t, err, errRoundForge)
}

func TestRoundWrites_ATakeInAProjectWhoseWriterIsNotLoadedFailsBeforeAnyRead(t *testing.T) {
	t.Parallel()

	pull := roundPulls(&roundForge{}, "")
	project := pull.projects["p"]
	project.Credentials = map[model.Role]model.Credential{model.RoleWriter: {}}
	pull.projects["p"] = project

	_, err := roundWrites{pulls: pull}.Take(t.Context(), "p", flow.TakeRequest{})

	require.ErrorIs(t, err, errCredentialsNotLoaded)
}

func TestClaimHeld_PassesBecauseTheEngineWritesOnlyUnderTheClaimOfTheRun(t *testing.T) {
	t.Parallel()

	require.NoError(t, claimHeld(t.Context()))
}

func TestInRepo_AddsTheRepositoryToEachCallOfGhWithoutChangingTheArguments(t *testing.T) {
	t.Parallel()

	spy := &argSpy{}
	args := []string{"pr", "view", "7"}

	inRepo{run: spy, repo: "acme/app"}.Run(t.Context(), args)

	assert.Equal(t, []string{"pr", "view", "7", "-R", "acme/app"}, spy.args)
	assert.Equal(t, []string{"pr", "view", "7"}, args, "the arguments of the caller stay as they were")
}

// argSpy keeps the arguments of the last call.
type argSpy struct{ args []string }

func (s *argSpy) Run(_ context.Context, args []string) model.CommandResult {
	s.args = args

	return model.CommandResult{}
}
