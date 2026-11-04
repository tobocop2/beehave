package main

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"github.com/tobocop2/obee/internal/flow"
	"github.com/tobocop2/obee/internal/forge"
	"github.com/tobocop2/obee/internal/model"
)

var errParentNoPull = errors.New("the parent item has no pull request")

// Open finds the open pull request on the branch of the item, or opens a draft one into the base. It then makes the body right and links the pull request
// into the native stack of its parent. The find comes first, so a resume after a lost step record opens no second pull request.
// A parent whose pull request is open sets the base to the branch of the parent; a parent that merged leaves the base as given and links nothing.
func (p pulls) Open(ctx context.Context, project, item string, req flow.OpenRequest) (flow.OpenResult, error) {
	built, opener, err := p.openerOf(project)
	if err != nil {
		return flow.OpenResult{}, err
	}

	parent, want, line, err := wantedPull(ctx, built, item, req)
	if err != nil {
		return flow.OpenResult{}, err
	}

	number, created, err := openedPull(ctx, built, opener, want, req.Issue, line)
	if err != nil {
		return flow.OpenResult{}, err
	}

	stack, err := stackOf(ctx, built, opener, parent, number)

	return flow.OpenResult{Number: number, Created: created, Stack: stack}, err
}

// openerOf returns the forge of the project and its write side for opening pull requests. A forge that has none is an error.
func (p pulls) openerOf(project string) (opened, forge.Opener, error) { //nolint:ireturn // the optional write side of the forge is an interface by design
	built, err := p.forgeOf(project)
	if err != nil {
		return opened{}, nil, err
	}

	opener, ok := built.Forge.(forge.Opener)
	if !ok {
		return opened{}, nil, fmt.Errorf("%w: opening a pull request", errNoOptionalRead)
	}

	return built, opener, nil
}

// wantedPull returns the number of the open pull request of the parent (0 for none), the pull request to create for the item, and the closing line.
// An open parent becomes the base, so the dependent is based on the branch of its parent. The body of the new pull request is the body of the call plus the closing line.
func wantedPull(ctx context.Context, built forge.Forge, item string, req flow.OpenRequest) (parent int, want model.NewPull, line string, err error) {
	parent, err = parentPull(ctx, built, req.Parent)
	if err != nil {
		return 0, model.NewPull{}, "", err
	}

	base := req.Base
	if parent != 0 {
		base = req.Parent
	}

	line, err = closingLine(ctx, built, req.Issue, base)
	if err != nil {
		return 0, model.NewPull{}, "", err
	}

	return parent, model.NewPull{Head: item, Base: base, Title: req.Title, Body: joinLine(req.Body, line)}, line, nil
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

// closingLine returns the closing line for the issue. A pull request into the default branch says Closes, one into any other branch says Addresses,
// so a round branch never closes an issue before the integration pull request lands. No issue means no line.
func closingLine(ctx context.Context, built forge.Forge, issue int, base string) (string, error) {
	if issue == 0 {
		return "", nil
	}

	def, err := built.DefaultBranch(ctx)
	if err != nil {
		return "", fmt.Errorf("read the default branch: %w", err)
	}

	keyword := "Addresses"
	if base == def {
		keyword = "Closes"
	}

	return fmt.Sprintf("%s #%d", keyword, issue), nil
}

// joinLine puts the line after the text, one blank line between them. An empty line leaves the text as it is.
func joinLine(text, line string) string {
	switch {
	case line == "":
		return text
	case text == "":
		return line
	}

	return text + "\n\n" + line
}

// openedPull returns the open pull request on the branch, and whether this call opened it. A new pull request gets the wanted body.
// An open pull request keeps its base, its title and its body; only its closing line is set.
func openedPull(ctx context.Context, built forge.Forge, opener forge.Opener, want model.NewPull, issue int, line string) (number int, created bool, err error) {
	number, err = listed(ctx, built, want.Head, "open")
	if err != nil {
		return 0, false, err
	}

	if number == 0 {
		number, err = opener.CreatePR(ctx, want)

		return number, err == nil, err
	}

	return number, false, syncLine(ctx, built, number, issue, line)
}

// syncLine makes the closing line of the pull request right and keeps every other line of its body. A line for the issue with another keyword is
// corrected in place; a body with no such line gets the line at the end. The body is written only when it changes. No line means no change.
func syncLine(ctx context.Context, built forge.Forge, number, issue int, line string) error {
	if line == "" {
		return nil
	}

	body, err := built.PRBody(ctx, number)
	if err != nil {
		return fmt.Errorf("read the body of pull request %d: %w", number, err)
	}

	want := withLine(body, issue, line)
	if want == body {
		return nil
	}

	return built.SetBody(ctx, number, want)
}

// withLine returns the body with the closing line for the issue: every closing line for that issue becomes the wanted one, or the line goes at the end when none exists.
func withLine(body string, issue int, line string) string {
	own := regexp.MustCompile(fmt.Sprintf(`(?m)^(Closes|Addresses) #%d[ \t\r]*$`, issue))
	if !own.MatchString(body) {
		return joinLine(body, line)
	}

	return own.ReplaceAllLiteralString(body, line)
}

// stackOf links the pull request on top of the parent when they are not yet in one stack, then reads both ids and requires one non-empty id.
// No parent means no stack and an empty id.
func stackOf(ctx context.Context, built forge.Forge, opener forge.Opener, parent, number int) (string, error) {
	if parent == 0 {
		return "", nil
	}

	if err := linkIfApart(ctx, built, opener, parent, number); err != nil {
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
func linkIfApart(ctx context.Context, built forge.Forge, opener forge.Opener, parent, number int) error {
	same, _, err := sharedStack(ctx, built, parent, number)
	if err != nil || same {
		return err
	}

	return opener.LinkStack(ctx, parent, number)
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
