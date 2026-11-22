package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/tobocop2/beehave/internal/flow"
	"github.com/tobocop2/beehave/internal/forge"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc"
	"github.com/tobocop2/beehave/internal/roundcut"
	"github.com/tobocop2/beehave/internal/roundintegration"
	"github.com/tobocop2/beehave/internal/roundsync"
	"github.com/tobocop2/beehave/internal/takeover"
)

// roundWrites runs the forge writes of a round as the writer role of the project: the forge of a project is built with the runner of its role, so each call
// carries the token of the writer and never the login of the owner. A forge that lacks a call that a write needs is an error.
type roundWrites struct{ pulls pulls }

// Sync merges the trunk into the sync branch of the round and opens the sync pull request.
func (r roundWrites) Sync(ctx context.Context, project string, req roundsync.Request) (roundsync.Result, error) {
	built, err := optionalSide[roundsync.Forge](r.pulls, project, "syncing a branch")
	if err != nil {
		return roundsync.Result{}, err
	}

	req.Label = r.label(project, req.Label)

	return roundsync.Check(ctx, built, req)
}

// label is the label of a pull request that a write opens: the one the call names, else the pr_label of the project.
func (r roundWrites) label(project, named string) string {
	if named != "" {
		return named
	}

	return r.pulls.projects[project].PRLabel
}

// Land merges the sync pull request pinned to the gated head.
func (r roundWrites) Land(ctx context.Context, project string, req roundsync.LandRequest) (roundsync.Result, error) {
	built, err := optionalSide[roundsync.Forge](r.pulls, project, "syncing a branch")
	if err != nil {
		return roundsync.Result{}, err
	}

	return roundsync.Land(ctx, built, req)
}

// Cut makes the next round branch and moves the open pull requests to it.
func (r roundWrites) Cut(ctx context.Context, project string, req roundcut.Request) (roundcut.Result, error) {
	built, err := r.pulls.forgeOf(project)
	if err != nil {
		return roundcut.Result{}, err
	}

	return roundcut.Run(ctx, built, req), nil
}

// Integrate finds or opens the integration pull request and writes its body.
func (r roundWrites) Integrate(ctx context.Context, project string, req roundintegration.Request) (roundintegration.Result, error) {
	built, err := optionalSide[roundintegration.Forge](r.pulls, project, "opening a pull request or writing its body")
	if err != nil {
		return roundintegration.Result{}, err
	}

	req.Label = r.label(project, req.Label)

	return roundintegration.Run(ctx, built, req)
}

// Place puts the card on the board and returns the column that the board shows for it.
func (r roundWrites) Place(ctx context.Context, project string, card model.Card) (string, error) {
	boarder, err := optionalSide[forge.Boarder](r.pulls, project, "keeping a project board")
	if err != nil {
		return "", err
	}

	if err := boarder.PlaceCard(ctx, card); err != nil {
		return "", err
	}

	return boarder.CardStatus(ctx, card)
}

// Take runs one step of the takeover. The run holds the claim of the item, so the claim read always passes. gh runs as the writer on the repository of the project.
func (r roundWrites) Take(ctx context.Context, project string, req flow.TakeRequest) (takeover.Result, error) {
	built, err := r.pulls.forgeOf(project)
	if err != nil {
		return takeover.Result{}, err
	}

	config := r.pulls.projects[project]

	runner, err := r.pulls.writers.runner(config.Project)
	if err != nil {
		return takeover.Result{}, err
	}

	settings := takeover.Context{
		Me: req.Me, Idle: req.Idle, Grace: req.Grace, Now: req.Now,
		Free: claimHeld,
		Gh:   inRepo{run: runner, repo: config.Repo},
	}

	return takeover.Run(ctx, built, req.Request, settings)
}

// claimHeld is the claim check of a takeover that runs in the engine. The engine writes only while its run holds the claim of the item (runWrites checks it before this call),
// so no other owner holds the claim and the check passes.
func claimHeld(context.Context) error { return nil }

var errNoOptionalWrite = errors.New("this forge cannot do it")

// optionalSide returns the forge of the project as the interface T, which a forge may lack. what names the missing ability in the error.
func optionalSide[T any](p pulls, project, what string) (T, error) { //nolint:ireturn // the optional sides of the forge are interfaces
	var none T

	built, err := p.forgeOf(project)
	if err != nil {
		return none, err
	}

	side, ok := built.Forge.(T) // the optional write sides of the forge are interfaces that only some forges hold
	if !ok {
		return none, fmt.Errorf("%w: %s", errNoOptionalWrite, what)
	}

	return side, nil
}

// inRepo runs gh on one repository: it adds -R repo to each call, so a call needs no git folder.
type inRepo struct {
	run  proc.Runner
	repo string
}

// Run runs gh with the arguments and the repository.
func (r inRepo) Run(ctx context.Context, args []string) model.CommandResult {
	return r.run.Run(ctx, append(append([]string{}, args...), "-R", r.repo))
}
