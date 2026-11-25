package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/tobocop2/beehave/internal/forge"
	"github.com/tobocop2/beehave/internal/model"
	storegithub "github.com/tobocop2/beehave/internal/store/github"
)

// errNoIssueReads is the refusal of a build that cannot read who wrote the issue of an item, so the engine never reads an issue as the owner's by default.
var errNoIssueReads = errors.New("this build cannot read who wrote an issue")

// Association reads who wrote an issue or a pull request, when the forge can. A forge that cannot is an error, so the engine never reads an item as the owner's by default.
func (o opened) Association(ctx context.Context, number int) (model.Association, error) {
	reads, ok := o.Forge.(forge.AssociationReads)
	if !ok {
		return "", fmt.Errorf("%w: who wrote an issue or a pull request", errNoOptionalRead)
	}

	return reads.Association(ctx, number)
}

// IssueAuthor reads who wrote the issue of the item when the tracker of the project keeps its items as issues on GitHub. It reads through the GitHub
// access of the tracker, so a project with no forge key reads it too. An item of another tracker has none.
func (p pulls) IssueAuthor(ctx context.Context, project, item string) (model.Authored, error) {
	found, err := p.projectOf(project)
	if err != nil {
		return model.Authored{}, err
	}

	number, err := issueNumber(found.Tracker, item)
	if err != nil || number == 0 {
		return model.Authored{}, err
	}

	if p.issues == nil {
		return model.Authored{}, errNoIssueReads
	}

	reads, err := p.issues(found.Project)
	if err != nil {
		return model.Authored{}, err
	}

	return authored(ctx, opened{reads}, number)
}

// issueNumber returns the number of the GitHub issue of the item, or 0 when the tracker keeps no GitHub issues or the item is not one.
func issueNumber(tracker model.TrackerName, item string) (int, error) {
	if tracker != storegithub.Tracker || model.ItemKind(item) != model.KindForge {
		return 0, nil
	}

	number, err := strconv.Atoi(item)
	if err != nil {
		return 0, fmt.Errorf("item %s is not the number of an issue: %w", item, err)
	}

	return number, nil
}

// SameItem reports whether two item ids name one item: the same text, or, when the tracker keeps GitHub issues, two numbers of one issue, as 0412 and 412.
// An id that is the number of no issue, such as one past the range of an issue, names only itself.
func (p pulls) SameItem(project, a, b string) (bool, error) {
	if a == b {
		return true, nil
	}

	found, err := p.projectOf(project)
	if err != nil {
		return false, err
	}

	numberA, errA := issueNumber(found.Tracker, a)
	numberB, errB := issueNumber(found.Tracker, b)

	return errA == nil && errB == nil && numberA != 0 && numberA == numberB, nil
}

// authored reads who wrote the issue or pull request with the number.
func authored(ctx context.Context, built opened, number int) (model.Authored, error) {
	association, err := built.Association(ctx, number)
	if err != nil {
		return model.Authored{}, err
	}

	return model.Authored{Number: number, Association: association}, nil
}
