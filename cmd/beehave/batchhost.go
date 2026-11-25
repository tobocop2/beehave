package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tobocop2/beehave/internal/flow"
	"github.com/tobocop2/beehave/internal/forge"
	"github.com/tobocop2/beehave/internal/landing"
	"github.com/tobocop2/beehave/internal/merge"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/review"
)

// batchGitTimeout is the time that one git call of merge may run.
const batchGitTimeout = 10 * time.Minute

// batchIdentity is the author and committer of the union commits. They exist in the object database of the worktree only, and no forge sees them.
func batchIdentity() []string {
	return []string{"-c", "user.name=" + flow.EngineName, "-c", "user.email=" + flow.EngineEmail}
}

// batchHost serves merge: git in the worktree of the item, the base tip on the forge, and the intent of a member merge.
type batchHost struct {
	pulls    pulls
	programs programs
	base     baseFetcher
}

// baseFetcher fetches every branch of the remote of the project, and the tags that git follows from them, into its clone, from the address that the
// engine read.
type baseFetcher interface {
	Fetch(ctx context.Context, project string) error
}

// signed runs git with the identity of the union commits in front of the arguments.
type signed struct {
	run func(ctx context.Context, args []string) model.CommandResult
}

func (s signed) Run(ctx context.Context, args []string) model.CommandResult {
	return s.run(ctx, append(batchIdentity(), args...))
}

// folder returns the worktree of the item. An item with no worktree is an error: git with no folder runs in the folder of the beehave process,
// which is the checkout of the owner.
func (h batchHost) folder(ctx context.Context, project, item string) (string, error) {
	dir, err := h.programs.trees.Path(ctx, project, item)
	if err != nil {
		return "", fmt.Errorf("find the folder of item %s: %w", item, err)
	}

	if dir == "" {
		return "", fmt.Errorf("item %s of project %s has no worktree, so merge cannot run git for it. Make the worktree of the item first", item, project)
	}

	return dir, nil
}

// Git returns git over the worktree of the item.
func (h batchHost) Git(ctx context.Context, project, item string) (landing.GitCLI, error) {
	dir, err := h.folder(ctx, project, item)
	if err != nil {
		return landing.GitCLI{}, err
	}

	return landing.GitCLI{Exec: signed{run: h.programs.local(project, "git", dir, batchGitTimeout).Run}}, nil
}

// tree reads the tree id of a commit in dir of the project.
func (h batchHost) tree(ctx context.Context, project, dir, commit string) (string, error) {
	return h.programs.git(ctx, project, dir, batchGitTimeout, "git rev-parse of "+commit, "rev-parse", commit+"^{tree}")
}

// Tip reads the tip of the base on the forge, fetches the branches of the remote into the clone, and returns the tip with its tree id.
func (h batchHost) Tip(ctx context.Context, project, item, base string) (landing.Tip, error) {
	commit, err := h.baseTip(ctx, project, base)
	if err != nil {
		return landing.Tip{}, err
	}

	dir, err := h.fetched(ctx, project, item, base)
	if err != nil {
		return landing.Tip{}, err
	}

	tree, err := h.tree(ctx, project, dir, commit)

	return landing.Tip{Commit: commit, Tree: tree}, err
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

// fetched fetches the branches of the remote into the clone of the project and returns the folder of the worktree of the item.
func (h batchHost) fetched(ctx context.Context, project, item, base string) (string, error) {
	dir, err := h.folder(ctx, project, item)
	if err != nil {
		return "", err
	}

	if err := h.base.Fetch(ctx, project); err != nil {
		return "", fmt.Errorf("fetch the base %s of project %s: %w", base, project, err)
	}

	return dir, nil
}

// Prove checks on the forge that each landed member is on the base, merged or on the vehicle commit, and returns the item and the reason for each one it cannot prove.
// A forge that cannot compare commits or list trees leaves every member unproven, and the reasons say why.
func (h batchHost) Prove(ctx context.Context, project, base string, start, end landing.Tip, landed []landing.Member) ([]landing.Outcome, error) {
	built, err := h.pulls.forgeOf(project)
	if err != nil {
		return nil, err
	}

	return landing.Prove(ctx, built, base, start, end, landed), nil
}

// Intent reads the pull request of the item once and requires it OPEN. It writes nothing to the forge.
func (h batchHost) Intent(ctx context.Context, project, item, repo, _, tipTree string, now time.Time) (merge.Intent, error) {
	built, number, err := h.pulls.target(ctx, project, item, repo)
	if err != nil {
		return merge.Intent{}, err
	}

	facts, err := model.WithoutListRefusals(built.PullRead(ctx, number))
	if err != nil {
		return merge.Intent{}, fmt.Errorf("read the pull request of item %s: %w", item, err)
	}

	if facts.Number != number {
		return merge.Intent{}, fmt.Errorf("the read of item %s answered for pull request %d, not %d", item, facts.Number, number)
	}

	if state := strings.ToUpper(strings.TrimSpace(facts.State)); state != stateOpen {
		return merge.Intent{}, fmt.Errorf("the pull request of item %s is %s, %w, so a merge cannot be tied to this run", item, state, flow.ErrMemberNotOpen)
	}

	return merge.NewIntent(facts, tipTree, now), nil
}

// Pull reads the standing of the pull request of the item through the standing review. A non-empty reviewed is the commit that was marked ready: a head that is another commit
// with the same tree is a rewrite of it. A read that fails goes into Unread, and an item with only a closed pull request is closed.
func (h batchHost) Pull(ctx context.Context, project, item, repo, reviewed string) (review.Standing, error) {
	built, number, err := h.pulls.target(ctx, project, item, repo)
	if errors.Is(err, forge.ErrNoPull) {
		return closedPull(ctx, built, item, err)
	}

	if err != nil {
		return review.Standing{}, err
	}

	standing := review.Pull(ctx, built, number)
	if reviewed != "" {
		standing.Compare(ctx, built, reviewed)
	}

	return standing, nil
}

// stateOpen is the state of a pull request that is open.
const stateOpen = "OPEN"

// SetDraft marks the pull request of the item a draft, or ready for review when draft is false.
func (h batchHost) SetDraft(ctx context.Context, project, item, repo string, draft bool) error {
	built, number, err := h.pulls.target(ctx, project, item, repo)
	if err != nil {
		return err
	}

	return built.SetDraft(ctx, number, draft)
}

// closedPull is the standing of an item that has no open or merged pull request: closed when the forge lists a closed one, else the error of the lookup.
func closedPull(ctx context.Context, built forge.Forge, item string, missing error) (review.Standing, error) {
	number, err := listed(ctx, built, item, "closed")
	if err != nil || number == 0 {
		return review.Standing{}, cmp.Or(err, missing)
	}

	return review.Standing{Number: number, Closed: true}, nil
}
