package flow

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc"
)

// releaseTimeout bounds every git call of one release when the clock sets none.
const releaseTimeout = 30 * time.Second

// treeStepName names the workflow step that removes the worktree of an item.
const treeStepName = "worktree"

// treeMakeStepName names the workflow step that makes the worktree of a round item.
const treeMakeStepName = "make worktree"

// remoteName is the remote that holds the branch of an item.
const remoteName = "origin"

// trees finds the worktree of an item. WithTree runs fn with the worktree while it holds the lock of the clone, so no other worktree command on the
// clone runs meanwhile. An empty path means the item has none. Heads runs inside fn and returns the git ls-remote --heads lines of the remote of the
// project, read from the address that the engine holds and with no setting of the clone config.
type trees interface {
	WithTree(ctx context.Context, project, item string, fn func(dir string)) error
	Heads(ctx context.Context, project string) (string, error)
	// Git returns the runner of the engine for git in a folder that the trees hold: a clone or a worktree.
	Git(dir string) proc.Runner
}

// treeMaker finds the worktree of an item and makes it when it is missing. Ensure returns the folder, and an empty folder is not a success.
// A round item with a start is made again at that commit; with an empty start it is made at the tip of its base.
// Any other item is cut at base, or at the base branch of the project when base is empty.
type treeMaker interface {
	trees
	Ensure(ctx context.Context, project, item, start, base string) (string, error)
}

// placer puts the worktree of an item at the commit head before a fork runs. A round item gets a detached worktree. Any other item gets branch, cut at head,
// and its own branch stays as it is. A clone that does not hold head, and a worktree with changes or with commits that no remote branch holds, are refused.
type placer interface {
	Place(ctx context.Context, project, item, head, branch string) error
}

// read is one git call that only reads, with the words that name it in a reason to keep the tree.
type read struct {
	doing string
	args  []string
}

// removeTree removes the worktree of an item and returns the reason it kept the tree instead.
// It keeps the tree on any doubt: a git error, a timeout, a dirty tree, or a HEAD that no remote branch tip shows.
// One timeout covers every git call.
func (e *Engine) removeTree(ctx context.Context, project, item string) (string, error) {
	return ReleaseDeps{Trees: e.deps.Trees, Clock: e.deps.Clock, Repository: e.deps.Repository}.removeTree(ctx, project, item)
}

// ReleaseDeps holds what gives an item back outside its workflow: the worktree and the claim.
// The engine uses it for a canceled run, and the client for a run that no engine executes.
type ReleaseDeps struct {
	Lifecycle lifecycle
	// Beats reads the claim lease of a run, so a move to this build refuses a run whose holder is alive. A nil value reads none.
	Beats beats
	Trees trees
	// Place cuts the worktree of an item at the head of a fork. A nil value cuts none, as in a build with no repository.
	Place placer
	Clock clock
	// Repository says whether a project has a git repository, as Deps.Repository does. A nil value means every project has one.
	// beehave keeps the plain item folder of a project with no repository: it has no remote branch to show that its work is safe.
	Repository func(project string) bool
	// Merge turns off the queued merges of a run that is canceled while no engine runs it. A nil value turns none off.
	Merge merger
}

func (d ReleaseDeps) removeTree(ctx context.Context, project, item string) (string, error) {
	if d.Repository != nil && !d.Repository(project) {
		return "the project has no repository, so its folder holds work that no remote shows", nil
	}

	kept := "item has no worktree"

	if err := d.Trees.WithTree(ctx, project, item, func(dir string) {
		if dir == "" {
			return
		}

		bounded, cancel := d.limit(ctx)
		defer cancel()

		kept = tryRemove(bounded, d.Trees, dir, func(ctx context.Context) (string, error) { return d.Trees.Heads(ctx, project) })
	}); err != nil {
		return "", fmt.Errorf("find the worktree of item %s: %w", item, err)
	}

	return kept, nil
}

// limit bounds one release call: the git calls of a worktree, or the call to the tracker that gives the claim back.
func (d ReleaseDeps) limit(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, cmp.Or(d.Clock.Release, releaseTimeout))
}

// newerRunExists reports whether a newer run of the item than the attempt exists. The worktree is the folder of the item, so it belongs to that newer run.
func newerRunExists(view storeView, a *attempt) (bool, error) {
	last, found, err := latestRun(view, a.in.Item)

	return found && last.Status.ID != a.id, err
}

// giveBack ends an item with no workflow step: first the worktree, then the claim.
// A worktree that stays is not a failure; the log says why. With newer true a newer run of the item uses the worktree, so the give-back keeps it and releases the claim only.
func (d ReleaseDeps) giveBack(ctx context.Context, logger *slog.Logger, project, item, owner string, newer bool) error {
	treeErr := d.giveTree(ctx, logger, project, item, newer)

	bounded, cancel := d.limit(ctx)
	defer cancel()

	if err := d.Lifecycle.Release(bounded, project, item, owner); err != nil {
		return errors.Join(treeErr, fmt.Errorf("%s of item %s: %w", model.StepRelease, item, err))
	}

	return treeErr
}

// giveTree removes the worktree of the item, unless newer says that a newer run uses it. A worktree that stays is logged with the reason.
func (d ReleaseDeps) giveTree(ctx context.Context, logger *slog.Logger, project, item string, newer bool) error {
	kept, err := "a newer run of the item uses the worktree", error(nil)
	if !newer {
		kept, err = d.removeTree(ctx, project, item)
	}

	if kept != "" {
		logger.InfoContext(ctx, "kept the worktree", "item", item, "reason", kept)
	}

	if err != nil {
		return fmt.Errorf("%s of item %s: %w", treeStepName, item, err)
	}

	return nil
}

// tryRemove removes the worktree from the clone that owns it, or returns the reason it stays. heads reads the branches of the remote.
func tryRemove(ctx context.Context, found trees, dir string, heads func(context.Context) (string, error)) string {
	common, reason := keepReason(ctx, found.Git(dir), heads)
	if reason != "" {
		return reason
	}

	if _, err := git(ctx, found.Git(filepath.Dir(common)), "worktree", "remove", dir); err != nil {
		return explain(ctx, "remove the worktree", err)
	}

	return ""
}

// keepReason returns why the tree must stay. When the tree is clean and its HEAD is the tip of a remote branch,
// it returns the repository that owns the tree and an empty reason.
// The remote is read, not the local tracking refs: a tracking ref can show a push that the remote lost.
func keepReason(ctx context.Context, tree proc.Runner, heads func(context.Context) (string, error)) (common, reason string) {
	head, common, reason := cleanHead(ctx, tree)
	if reason != "" {
		return "", reason
	}

	listing, err := heads(ctx)
	if err != nil {
		return "", explain(ctx, "read the branches of the remote", err)
	}

	if !isTip(listing, head) {
		return "", "HEAD is not the tip of a branch on the remote"
	}

	return common, ""
}

// cleanHead returns the commit at HEAD and the repository that owns a tree with no changes that are not committed. A dirty tree is a reason to keep it.
func cleanHead(ctx context.Context, tree proc.Runner) (head, common, reason string) {
	local, reason := readAll(ctx, tree,
		read{"read the state of the worktree", []string{"status", "--porcelain"}},
		read{"read HEAD", []string{"rev-parse", "HEAD"}},
		read{"read the repository of the worktree", []string{"rev-parse", "--path-format=absolute", "--git-common-dir"}})
	if reason != "" {
		return "", "", reason
	}

	if strings.TrimSpace(local[0]) != "" {
		return "", "", "the worktree has changes that are not committed"
	}

	return strings.TrimSpace(local[1]), strings.TrimSpace(local[2]), ""
}

// isTip reports whether a line of git ls-remote lists sha.
func isTip(listing, sha string) bool {
	for line := range strings.SplitSeq(listing, "\n") {
		if tip, _, _ := strings.Cut(line, "\t"); tip == sha {
			return true
		}
	}

	return false
}

// readAll runs the reads in order and returns their outputs. The first failure ends it with the reason to keep the tree.
func readAll(ctx context.Context, tree proc.Runner, reads ...read) ([]string, string) {
	outputs := make([]string, 0, len(reads))

	for _, r := range reads {
		out, err := git(ctx, tree, r.args...)
		if err != nil {
			return nil, explain(ctx, r.doing, err)
		}

		outputs = append(outputs, out)
	}

	return outputs, ""
}

// explain turns a failed git call into the reason to keep the tree. A timeout is named as one.
func explain(ctx context.Context, doing string, err error) string {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Sprintf("git did not %s in time", doing)
	}

	return fmt.Sprintf("git could not %s: %v", doing, err)
}

// gitExit is a git call that exited with a code other than 0.
type gitExit struct {
	code   int
	stderr string
}

func (e *gitExit) Error() string {
	return fmt.Sprintf("exit status %d: %s", e.code, e.stderr)
}

// git runs one git command with the runner of the engine for a tree and returns its output.
func git(ctx context.Context, tree proc.Runner, args ...string) (string, error) {
	result := tree.Run(ctx, args)
	if result.ExitCode != 0 {
		return "", &gitExit{code: result.ExitCode, stderr: strings.TrimSpace(result.Stderr)}
	}

	return result.Stdout, nil
}
