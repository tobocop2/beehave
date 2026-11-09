package github

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/tobocop2/beehave/internal/forge"
	"github.com/tobocop2/beehave/internal/model"
)

// CreatePR opens a draft pull request and returns its number, which gh prints as the last part of the address. Title and body go as single arguments.
// The branch must be on the remote already: gh refuses a branch that is not.
func (f *Forge) CreatePR(ctx context.Context, req model.NewPull) (int, error) {
	done := f.run.Run(ctx, []string{argPR, "create", "--draft", argRepo, f.repo, "--head", req.Head, "--base", req.Base, "--title", req.Title, argBody, req.Body})
	if done.ExitCode != 0 {
		return 0, forge.Refuse("cannot open a draft pull request for branch %s into %s. Push the branch first", req.Head, req.Base)
	}

	number, ok := model.URLNumber(done.Stdout, "pull")
	if !ok {
		return 0, forge.Refuse("cannot read the number of the pull request that gh opened for branch %s", req.Head)
	}

	return number, nil
}

// LinkStack puts the child pull request on top of the parent in a native stack. gh takes the pull requests by address, because the command has no repository flag.
// A non-empty base goes to gh as the branch of the bottom pull request.
func (f *Forge) LinkStack(ctx context.Context, parent, child int, base string) error {
	prefix := "https://github.com/" + strings.TrimSpace(f.repo) + "/pull/"
	args := []string{"stack", "link"}

	if base != "" {
		args = append(args, "--base", base)
	}

	done := f.run.Run(ctx, append(args, prefix+strconv.Itoa(parent), prefix+strconv.Itoa(child)))
	if done.ExitCode != 0 {
		return forge.Refuse("cannot link pull request %d into the stack of pull request %d", child, parent)
	}

	return nil
}

// Unstack removes the stack of the pull request on GitHub. gh takes the number of the stack, which the forge reads first.
func (f *Forge) Unstack(ctx context.Context, pr int) error {
	number, err := f.stackNumber(ctx, pr)
	if err != nil {
		return err
	}

	done := f.run.Run(ctx, []string{"stack", "unstack", strconv.Itoa(number)})
	if done.ExitCode != 0 {
		return forge.Refuse("cannot unstack pull request %d", pr)
	}

	return nil
}

// stackNumber returns the number that gh uses for the stack of the pull request. A pull request in no stack is a refusal.
func (f *Forge) stackNumber(ctx context.Context, pr int) (int, error) {
	base := fmt.Sprintf("cannot unstack pull request %d", pr)

	pull, err := readPull[struct {
		Stack *struct {
			Number int `json:"number"`
		} `json:"stack"`
	}](ctx, f, pr, stackNumberQuery, base)
	if err != nil {
		return 0, err
	}

	if pull == nil || pull.Stack == nil || pull.Stack.Number == 0 {
		return 0, forge.Refuse("%s: it is in no stack", base)
	}

	return pull.Stack.Number, nil
}
