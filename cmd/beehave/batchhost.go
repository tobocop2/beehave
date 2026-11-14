package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/tobocop2/beehave/internal/batch"
	"github.com/tobocop2/beehave/internal/flow"
	"github.com/tobocop2/beehave/internal/forge"
	"github.com/tobocop2/beehave/internal/merge"
	"github.com/tobocop2/beehave/internal/model"
	storegithub "github.com/tobocop2/beehave/internal/store/github"
)

// batchGitTimeout is the time that one git call of batch_merge may run.
const batchGitTimeout = 10 * time.Minute

// batchIdentity is the author and committer of the union commits. They exist in the object database of the worktree only, and no forge sees them.
func batchIdentity() []string {
	return []string{"-c", "user.name=beehave", "-c", "user.email=beehave@localhost"}
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

// folder returns the worktree of the item. An item with no worktree is an error: git with no folder runs in the folder of the beehave process,
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

	if _, err := h.programs.git(ctx, dir, batchGitTimeout, "git checkout of the union "+commit, "checkout", "-q", "--detach", commit); err != nil {
		return "", err
	}

	return h.tree(ctx, dir, commit)
}

// tree reads the tree id of a commit in dir.
func (h batchHost) tree(ctx context.Context, dir, commit string) (string, error) {
	return h.programs.git(ctx, dir, batchGitTimeout, "git rev-parse of "+commit, "rev-parse", commit+"^{tree}")
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

	if _, err := h.programs.git(ctx, dir, batchGitTimeout, "git fetch", "fetch", "-q", "origin"); err != nil {
		return "", err
	}

	return dir, nil
}

// Prove checks on the forge that each landed member is on the base, merged or on the vehicle commit, and returns the item and the reason for each one it cannot prove.
// A forge that cannot compare commits or list trees leaves every member unproven, and the reasons say why.
func (h batchHost) Prove(ctx context.Context, project, base string, start, end batch.Tip, landed []batch.Member) ([]batch.Outcome, error) {
	built, err := h.pulls.forgeOf(project)
	if err != nil {
		return nil, err
	}

	return batch.Prove(ctx, built, base, start, end, landed), nil
}

// Issues reads the state of the issues after a batch. Only the GitHub tracker has issue numbers for ids. A read that fails is a finding of the report.
// The issues of members of an integration branch stay open until the integration pull request lands, so a base that is not the default branch
// is read only when the call names the integration pull request, and only once that pull request is merged. In each other case the report holds a note.
func (h batchHost) Issues(ctx context.Context, project, base string, integration int, landed []string) (batch.Report, error) {
	found, err := h.pulls.projectOf(project)
	if err != nil {
		return batch.Report{}, err
	}

	if found.Tracker != storegithub.Tracker {
		return batch.Report{Note: fmt.Sprintf("issue states: the tracker %s of project %s has no issue numbers, so no read ran.", found.Tracker, project)}, nil
	}

	built, err := h.pulls.forgeOf(project)
	if err != nil {
		return batch.Report{Findings: []string{fmt.Sprintf("cannot read the issues: %v", err)}}, nil
	}

	if held, stop := h.notYet(ctx, built, base, integration); stop {
		return held, nil
	}

	return batch.Audit(ctx, built, landed, integration), nil
}

// notYet says whether the issues cannot be read yet. The report holds a note for a state that is legitimate, and a finding for a read that failed.
func (h batchHost) notYet(ctx context.Context, built forge.Forge, base string, integration int) (batch.Report, bool) {
	if integration > 0 {
		return notMerged(ctx, built, integration)
	}

	defaultBranch, err := built.DefaultBranch(ctx)
	if err != nil {
		return batch.Report{Findings: []string{fmt.Sprintf("cannot read the default branch: %v", err)}}, true
	}

	if base != defaultBranch {
		return batch.Report{Note: fmt.Sprintf("issue states: the base %s is not the default branch %s, so the issues stay open until the integration pull request lands. Pass `integration` to read them.", base, defaultBranch)}, true
	}

	return batch.Report{}, false
}

// notMerged says whether the integration pull request is not merged yet. A read that fails is a finding.
func notMerged(ctx context.Context, built forge.Forge, integration int) (batch.Report, bool) {
	view, err := built.PRView(ctx, integration, "state")
	if err != nil {
		return batch.Report{Findings: []string{fmt.Sprintf("cannot read pull request #%d: %v", integration, err)}}, true
	}

	switch state := strings.ToUpper(strings.TrimSpace(model.Deref(view.State))); state {
	case "MERGED":
		return batch.Report{}, false
	case "OPEN", "CLOSED":
		return batch.Report{Note: fmt.Sprintf("issue states: the integration pull request %d is %s, not MERGED, so the issues are not read yet.", integration, state)}, true
	default:
		return batch.Report{Findings: []string{fmt.Sprintf("the integration pull request %d has the state %q, which is not OPEN, CLOSED or MERGED", integration, state)}}, true
	}
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

	if state := strings.ToUpper(strings.TrimSpace(model.Deref(view.State))); state != "OPEN" {
		return merge.Intent{}, fmt.Errorf("the pull request of item %s is %s, %w, so a merge cannot be tied to this run", item, state, flow.ErrMemberNotOpen)
	}

	view.Number = number

	return merge.NewIntent(view, tipTree, now)
}
