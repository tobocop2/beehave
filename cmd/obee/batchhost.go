package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/tobocop2/obee/internal/batch"
	"github.com/tobocop2/obee/internal/flow"
	"github.com/tobocop2/obee/internal/merge"
	"github.com/tobocop2/obee/internal/model"
)

// batchGitTimeout is the time that one git call of batch_merge may run.
const batchGitTimeout = 10 * time.Minute

// batchIdentity is the author and committer of the union commits. They exist in the object database of the worktree only, and no forge sees them.
func batchIdentity() []string {
	return []string{"-c", "user.name=obee", "-c", "user.email=obee@localhost"}
}

// batchHost serves batch_merge: git in the worktree of the item, the base tip on the forge, and the intent of a member merge.
type batchHost struct {
	pulls    pulls
	programs programs
}

// signed runs git with the identity of the union commits in front of the arguments.
type signed struct {
	run func(ctx context.Context, args []string) model.CommandResult
}

func (s signed) Run(ctx context.Context, args []string) model.CommandResult {
	return s.run(ctx, append(batchIdentity(), args...))
}

// folder returns the worktree of the item. An item with no worktree is an error: git with no folder runs in the folder of the obee process,
// which is the checkout of the owner.
func (h batchHost) folder(ctx context.Context, project, item string) (string, error) {
	dir, err := h.programs.trees.Path(ctx, project, item)
	if err != nil {
		return "", fmt.Errorf("find the folder of item %s: %w", item, err)
	}

	if dir == "" {
		return "", fmt.Errorf("item %s of project %s has no worktree, so batch_merge cannot run git for it. Make the worktree of the item first", item, project)
	}

	return dir, nil
}

// Git returns git over the worktree of the item.
func (h batchHost) Git(ctx context.Context, project, item string) (batch.Git, error) { //nolint:ireturn // the interface batchHost serves in internal/flow returns it
	dir, err := h.folder(ctx, project, item)
	if err != nil {
		return nil, err
	}

	return batch.GitCLI{Exec: signed{run: h.programs.local("git", dir, batchGitTimeout).Run}}, nil
}

// Checkout puts the commit in the worktree of the item, without a branch, and returns its tree id.
func (h batchHost) Checkout(ctx context.Context, project, item, commit string) (string, error) {
	dir, err := h.folder(ctx, project, item)
	if err != nil {
		return "", err
	}

	run := h.programs.local("git", dir, batchGitTimeout).Run

	if out := run(ctx, []string{"checkout", "-q", "--detach", commit}); out.ExitCode != 0 {
		return "", fmt.Errorf("git checkout of the union %s in %s: %s", commit, dir, strings.TrimSpace(out.Stderr))
	}

	return h.tree(ctx, dir, commit)
}

// tree reads the tree id of a commit in dir.
func (h batchHost) tree(ctx context.Context, dir, commit string) (string, error) {
	out := h.programs.local("git", dir, batchGitTimeout).Run(ctx, []string{"rev-parse", commit + "^{tree}"})
	if out.ExitCode != 0 {
		return "", fmt.Errorf("git rev-parse of %s in %s: %s", commit, dir, strings.TrimSpace(out.Stderr))
	}

	return strings.TrimSpace(out.Stdout), nil
}

// Tip fetches the remote, reads the tip of the base on the forge, and returns it with its tree id.
func (h batchHost) Tip(ctx context.Context, project, item, base string) (batch.Tip, error) {
	commit, err := h.baseTip(ctx, project, base)
	if err != nil {
		return batch.Tip{}, err
	}

	dir, err := h.fetched(ctx, project, item)
	if err != nil {
		return batch.Tip{}, err
	}

	tree, err := h.tree(ctx, dir, commit)

	return batch.Tip{Commit: commit, Tree: tree}, err
}

// baseTip reads the tip commit of the base on the forge of the project.
func (h batchHost) baseTip(ctx context.Context, project, base string) (string, error) {
	built, err := h.pulls.forgeOf(project)
	if err != nil {
		return "", err
	}

	commit, found, err := built.BranchRef(ctx, base)
	if err != nil {
		return "", fmt.Errorf("cannot read the tip of %s on the forge of project %s: %w", base, project, err)
	}

	if !found {
		return "", fmt.Errorf("cannot read the tip of %s on the forge of project %s: the branch is not there", base, project)
	}

	return commit, nil
}

// fetched fetches the remote in the worktree of the item and returns the folder of the worktree.
func (h batchHost) fetched(ctx context.Context, project, item string) (string, error) {
	dir, err := h.folder(ctx, project, item)
	if err != nil {
		return "", err
	}

	if out := h.programs.local("git", dir, batchGitTimeout).Run(ctx, []string{"fetch", "-q", "origin"}); out.ExitCode != 0 {
		return "", fmt.Errorf("git fetch in %s: %s", dir, strings.TrimSpace(out.Stderr))
	}

	return dir, nil
}

// Intent reads the pull request of the item once and requires it OPEN. It writes nothing to the forge.
func (h batchHost) Intent(ctx context.Context, project, item, repo, _, tipTree string, now time.Time) (merge.Intent, error) {
	built, number, err := h.pulls.target(ctx, project, item, repo)
	if err != nil {
		return merge.Intent{}, err
	}

	view, err := built.PRView(ctx, number, "state", "headRefOid", "baseRefName", "number")
	if err != nil {
		return merge.Intent{}, fmt.Errorf("read the pull request of item %s: %w", item, err)
	}

	if state := strings.ToUpper(strings.TrimSpace(deref(view.State))); state != "OPEN" {
		return merge.Intent{}, fmt.Errorf("the pull request of item %s is %s, %w, so a merge cannot be tied to this run", item, state, flow.ErrMemberNotOpen)
	}

	view.Number = number

	return merge.NewIntent(view, tipTree, now)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}

	return *s
}
