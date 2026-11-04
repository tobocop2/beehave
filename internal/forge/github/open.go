package github

import (
	"context"
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
func (f *Forge) LinkStack(ctx context.Context, parent, child int) error {
	base := "https://github.com/" + strings.TrimSpace(f.repo) + "/pull/"

	done := f.run.Run(ctx, []string{"stack", "link", base + strconv.Itoa(parent), base + strconv.Itoa(child)})
	if done.ExitCode != 0 {
		return forge.Refuse("cannot link pull request %d into the stack of pull request %d", child, parent)
	}

	return nil
}
