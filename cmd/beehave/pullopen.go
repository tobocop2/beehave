package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/tobocop2/beehave/internal/flow"
	"github.com/tobocop2/beehave/internal/forge"
	"github.com/tobocop2/beehave/internal/model"
)

var (
	errParentNoPull = errors.New("the parent item has no pull request")
	errBaseMoved    = errors.New("the stack link moved the base of the bottom pull request")
	errNoBase       = errors.New("the forge returned no base branch")
	errStillStacked = errors.New("the unstack left the pull request in its stack")
)

// Open finds the open pull request on the branch of the item, or opens a draft one into the base with the body of the call. It then links the pull request
// into the native stack of its parent. The find comes first, so a resume after a lost step record opens no second pull request.
// A pull request that is open is not written. A parent whose pull request is open sets the base to the branch of the parent; a parent that merged leaves the base as given and links nothing.
func (p pulls) Open(ctx context.Context, project, item string, req flow.OpenRequest) (flow.OpenResult, error) {
	built, opener, stacker, err := p.openerOf(ctx, project)
	if err != nil {
		return flow.OpenResult{}, err
	}

	parent, want, err := wantedPull(ctx, built, item, req)
	if err != nil {
		return flow.OpenResult{}, err
	}

	want.Label = p.projects[project].PRLabel
	number, created, err := openedPull(ctx, built, opener, item, want)
	if err != nil {
		return flow.OpenResult{}, err
	}

	stack, err := stackOf(ctx, built, stacker, parent, number, req.Base)

	return flow.OpenResult{Number: number, Created: created, Stack: stack}, err
}

// openerOf returns the forge of the project, its write side for opening pull requests, and its native stacks. A forge that cannot open pull requests is an error.
// A forge with no native stacks gives a nil Stacker: the pull request of a child is based on the branch of its parent and no stack is made.
func (p pulls) openerOf(ctx context.Context, project string) (opened, forge.Opener, forge.Stacker, error) { //nolint:ireturn // the optional write sides of the forge are interfaces by design
	built, err := p.forgeOf(project)
	if err != nil {
		return opened{}, nil, nil, err
	}

	opener, err := sideOf[forge.Opener](ctx, built.Forge, forge.AbilityOpener)
	if err != nil {
		return opened{}, nil, nil, err
	}

	if stacker, held := built.Forge.(forge.Stacker); held {
		return built, opener, stacker, nil
	}

	return built, opener, nil, nil
}

// wantedPull returns the number of the open pull request of the parent (0 for none) and the pull request to create for the item.
// An open parent becomes the base, so the dependent is based on the branch of its parent. The body of the new pull request is the body of the call.
func wantedPull(ctx context.Context, built forge.Forge, item string, req flow.OpenRequest) (parent int, want model.NewPull, err error) {
	parent, err = parentPull(ctx, built, req.Parent)
	if err != nil {
		return 0, model.NewPull{}, err
	}

	base := req.Base
	if parent != 0 {
		base = model.ItemBranch(req.Parent)
	}

	return parent, model.NewPull{Head: model.ItemBranch(item), Base: base, Title: req.Title, Body: req.Body}, nil
}

// parentPull returns the number of the open pull request of the parent item, 0 for no parent or one that merged, and an error for a parent with no pull request.
func parentPull(ctx context.Context, built forge.Forge, parent string) (int, error) {
	if parent == "" {
		return 0, nil
	}

	open, err := listed(ctx, built, parent, "open")
	if err != nil || open != 0 {
		return open, err
	}

	merged, err := listed(ctx, built, parent, "merged")
	if err != nil {
		return 0, err
	}

	if merged == 0 {
		return 0, fmt.Errorf("%w: %s", errParentNoPull, parent)
	}

	return 0, nil
}

// openedPull returns the open pull request on the branch of the item, and whether this call opened it. A new pull request gets the wanted body.
// An open pull request is not written: it keeps its base, its title and its body.
func openedPull(ctx context.Context, built forge.Forge, opener forge.Opener, item string, want model.NewPull) (number int, created bool, err error) {
	number, err = listed(ctx, built, item, "open")
	if err != nil || number != 0 {
		return number, false, err
	}

	number, err = opener.CreatePR(ctx, want)

	return number, err == nil, err
}

// stackOf links the pull request on top of the parent when they are not yet in one stack, then reads both ids and requires one non-empty id.
// The first link of a stack carries the base branch of the round. No parent means no stack and an empty id.
func stackOf(ctx context.Context, built forge.Forge, stacker forge.Stacker, parent, number int, base string) (string, error) {
	if parent == 0 || stacker == nil {
		return "", nil
	}

	if err := linkIfApart(ctx, built, stacker, parent, number, base); err != nil {
		return "", err
	}

	same, id, err := sharedStack(ctx, built, parent, number)
	if err != nil {
		return "", err
	}

	if !same {
		return "", fmt.Errorf("pull request %d and its parent %d are not in one stack after the link", number, parent)
	}

	return id, nil
}

// linkIfApart links the pull request on top of the parent unless both already show one stack id.
// A parent in no stack is the bottom of a new one, and that link passes the base branch of the round.
// Every link reads the base of the parent before and after, because a link can move it.
func linkIfApart(ctx context.Context, built forge.Forge, stacker forge.Stacker, parent, number int, base string) error {
	same, top, err := sharedStack(ctx, built, parent, number)
	if err != nil || same {
		return err
	}

	first := top == ""
	if !first {
		base = ""
	}

	return linkChecked(ctx, built, stacker, parent, number, base, first)
}

// linkChecked reads the base of the parent, links, and reads it again. A new stack whose bottom moved is repaired.
func linkChecked(ctx context.Context, built forge.Forge, stacker forge.Stacker, parent, number int, base string, first bool) error {
	before, err := baseOf(ctx, built, parent)
	if err != nil {
		return err
	}

	err = linkKeeping(ctx, built, stacker, parent, number, base, before)
	if !errors.Is(err, errBaseMoved) || !first {
		return err
	}

	return relinkBottom(ctx, built, stacker, parent, number, before)
}

// linkKeeping links with the base and returns an errBaseMoved error when the parent no longer targets the branch it targeted before.
func linkKeeping(ctx context.Context, built forge.Forge, stacker forge.Stacker, parent, number int, base, before string) error {
	if err := stacker.LinkStack(ctx, parent, number, base); err != nil {
		return err
	}

	return keptBase(ctx, built, parent, before)
}

// relinkBottom takes the new stack apart, sets the base of its bottom back and links once more with that base.
// A stack that stays after the unstack, or a second move of the base, is an error.
func relinkBottom(ctx context.Context, built forge.Forge, stacker forge.Stacker, parent, number int, before string) error {
	if err := unstack(ctx, built, stacker, parent); err != nil {
		return err
	}

	if err := built.SetBase(ctx, parent, before); err != nil {
		return err
	}

	return linkKeeping(ctx, built, stacker, parent, number, before, before)
}

// unstack removes the stack of the pull request and reads it again, because gh leaves a pull request that is queued for merge in its stack.
func unstack(ctx context.Context, built forge.Forge, stacker forge.Stacker, pr int) error {
	if err := stacker.Unstack(ctx, pr); err != nil {
		return err
	}

	left, err := built.StackID(ctx, pr)
	if err != nil {
		return err
	}

	if left != "" {
		return fmt.Errorf("%w: pull request %d is still in stack %s", errStillStacked, pr, left)
	}

	return nil
}

// keptBase is nil when the pull request still targets the branch it targeted before, and an errBaseMoved error when it does not.
func keptBase(ctx context.Context, built forge.Forge, pr int, before string) error {
	now, err := baseOf(ctx, built, pr)
	if err != nil {
		return err
	}

	if now != before {
		return fmt.Errorf("%w: pull request %d targets %s, not %s", errBaseMoved, pr, now, before)
	}

	return nil
}

// baseOf returns the base branch of a pull request. A read with no base is an error.
func baseOf(ctx context.Context, built forge.Forge, pr int) (string, error) {
	view, err := built.PRView(ctx, pr, "baseRefName")
	if err != nil {
		return "", err
	}

	if view.BaseRefName == nil || *view.BaseRefName == "" {
		return "", fmt.Errorf("%w: pull request %d", errNoBase, pr)
	}

	return *view.BaseRefName, nil
}

// sharedStack tells whether both pull requests show the same non-empty stack id.
func sharedStack(ctx context.Context, built forge.Forge, parent, number int) (same bool, id string, err error) {
	top, err := built.StackID(ctx, parent)
	if err != nil {
		return false, "", err
	}

	mine, err := built.StackID(ctx, number)
	if err != nil {
		return false, "", err
	}

	return top != "" && top == mine, top, nil
}
