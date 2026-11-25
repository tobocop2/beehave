package main

import (
	"context"
	"fmt"
	"strconv"

	"github.com/tobocop2/beehave/internal/forge"
	"github.com/tobocop2/beehave/internal/model"
	storegithub "github.com/tobocop2/beehave/internal/store/github"
)

// Association reads who wrote an issue or a pull request, when the forge can. A forge that cannot is an error, so the engine never reads an item as the owner's by default.
func (o opened) Association(ctx context.Context, number int) (model.Association, error) {
	reads, ok := o.Forge.(forge.AssociationReads)
	if !ok {
		return "", fmt.Errorf("%w: who wrote an issue or a pull request", errNoOptionalRead)
	}

	return reads.Association(ctx, number)
}

// Origin reads who wrote the item: its issue, when the tracker of the project keeps its items as issues on the forge, and its pull request, found as ReadPull finds it.
// An item of another tracker reads no issue, and an item with no pull request reads no author of one.
func (p pulls) Origin(ctx context.Context, project, item string) (model.Origin, error) {
	found, err := p.projectOf(project)
	if err != nil {
		return model.Origin{}, err
	}

	reads, err := p.forge(found.Project)
	if err != nil {
		return model.Origin{}, err
	}

	issue, err := issueOf(ctx, opened{reads}, found.Tracker, item)
	if err != nil {
		return model.Origin{}, err
	}

	pull, err := pullAuthor(ctx, opened{reads}, item)

	return model.Origin{Issue: issue, Pull: pull}, err
}

// pullAuthor reads who wrote the pull request of the item, found as ReadPull finds it. An item with no pull request has none.
func pullAuthor(ctx context.Context, built opened, item string) (model.Authored, error) {
	number, err := anyState(ctx, built, item)
	if err != nil || number == 0 {
		return model.Authored{}, err
	}

	return authored(ctx, built, number)
}

// issueOf reads who wrote the issue of the item when the tracker keeps its items as issues on the forge. Any other item has none.
func issueOf(ctx context.Context, built opened, tracker model.TrackerName, item string) (model.Authored, error) {
	if tracker != storegithub.Tracker || model.ItemKind(item) != model.KindForge {
		return model.Authored{}, nil
	}

	number, err := strconv.Atoi(item)
	if err != nil {
		return model.Authored{}, fmt.Errorf("item %s is not the number of an issue: %w", item, err)
	}

	return authored(ctx, built, number)
}

// authored reads who wrote the issue or pull request with the number.
func authored(ctx context.Context, built opened, number int) (model.Authored, error) {
	association, err := built.Association(ctx, number)
	if err != nil {
		return model.Authored{}, err
	}

	return model.Authored{Number: number, Association: association}, nil
}
