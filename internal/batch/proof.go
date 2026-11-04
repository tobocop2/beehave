package batch

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/tobocop2/beehave/internal/model"
)

// ProofReads are the forge reads that prove a landing. Compare reads the status and both counts of the comparison of a commit with a branch.
// CommitTree reads the tree id of a commit, and TreeBlobs lists the files of a tree, path to content id. A list cut short is an error.
type ProofReads interface {
	Compare(ctx context.Context, commit, base string) (model.Comparison, error)
	CommitTree(ctx context.Context, commit string) (string, error)
	TreeBlobs(ctx context.Context, tree string) (map[string]string, error)
	// BranchRef reads the current tip of a branch. The branch of a member is named by its item id. A branch that is gone is not an error: found is false.
	BranchRef(ctx context.Context, branch string) (sha string, found bool, err error)
}

// Prove checks that each landed member is on the base, and returns an Outcome with the reason for each member that it cannot prove. A member has landed when
// it was merged, which means the forge holds its head in the history of the base, or when it is on the vehicle commit: the head is not in the history,
// as after a squash, but every file that the member changed against the tip where the batch started has the same content on the base now.
// A member that is neither is unproven. A read that fails leaves the member unproven, and never landed.
// The files that the member changed are read against start, so a member that shares a path with another member of the batch is unproven too: the check fails closed.
func Prove(ctx context.Context, reads ProofReads, base string, start, end Tip, landed []Member) []Outcome {
	unproven := []Outcome{}
	proof := &treeProof{reads: reads, start: start, end: end}

	for _, m := range landed {
		if reason := proof.reason(ctx, base, m); reason != "" {
			unproven = append(unproven, Outcome{Item: m.Item, Reason: reason})
		}
	}

	return unproven
}

// reason returns the empty text for a member that is proven, and otherwise the text that says why it is not.
func (p *treeProof) reason(ctx context.Context, base string, m Member) string {
	if why := p.moved(ctx, m); why != "" {
		return why
	}

	got, err := p.reads.Compare(ctx, m.Head, base)
	if err != nil {
		return fmt.Sprintf("cannot compare its head %s with %s, so its landing is unproven: %v", m.Head, base, err)
	}

	if got.Contains() {
		return ""
	}

	if why := p.onVehicle(ctx, m); why != "" {
		return fmt.Sprintf("its head %s is not on %s (the forge compares it: %s) and %s", m.Head, base, got, why)
	}

	return ""
}

// treeProof reads the trees of the batch once and checks each member against them.
type treeProof struct {
	reads      ProofReads
	start, end Tip
	from, to   map[string]string
}

// onVehicle returns the empty text when every path that the member changed has the same content on the base, and otherwise the reason it is not proven.
func (p *treeProof) onVehicle(ctx context.Context, m Member) string {
	if err := p.load(ctx); err != nil {
		return err.Error()
	}

	tree, err := p.reads.CommitTree(ctx, m.Head)
	if err != nil {
		return fmt.Sprintf("cannot read its tree: %v", err)
	}

	files, err := p.reads.TreeBlobs(ctx, tree)
	if err != nil {
		return fmt.Sprintf("cannot list its files: %v", err)
	}

	return p.differ(files)
}

// moved returns the line for a member whose branch holds another commit than the head that the fold used, and for a branch that cannot be read.
// The old commit stays on the forge after a force push, so the compare of the old head would prove a commit that the branch no longer holds.
// A branch that is gone, for example deleted after the merge, has no newer commit to miss, and the compare decides.
func (p *treeProof) moved(ctx context.Context, m Member) string {
	tip, found, err := p.reads.BranchRef(ctx, m.Item)
	if err != nil {
		return fmt.Sprintf("cannot read its branch, so a push after the fold is not ruled out and its landing is unproven: %v", err)
	}

	if found && tip != m.Head {
		return fmt.Sprintf("its branch holds %s now and the batch folded %s, so the new commits did not land", tip, m.Head)
	}

	return ""
}

// load reads the files of the tip where the batch started and of the tip after it. It reads each once.
func (p *treeProof) load(ctx context.Context) error {
	if p.from != nil {
		return nil
	}

	from, err := p.reads.TreeBlobs(ctx, p.start.Tree)
	if err != nil {
		return fmt.Errorf("cannot list the files of the tip where the batch started: %w", err)
	}

	to, err := p.reads.TreeBlobs(ctx, p.end.Tree)
	if err != nil {
		return fmt.Errorf("cannot list the files of the base after the batch: %w", err)
	}

	p.from, p.to = from, to

	return nil
}

// differ names the paths that the member changed and the base does not hold, or says that the member changes nothing.
// A path the member changed is one with another content than at the start. A path it removed is one the base must not hold.
func (p *treeProof) differ(files map[string]string) string {
	changed, missing := p.changes(files)

	if changed == 0 {
		return "it changes nothing against the tip where the batch started, so the base cannot show it"
	}

	if len(missing) == 0 {
		return ""
	}

	slices.Sort(missing)

	return "the base does not hold its change of " + strings.Join(missing, ", ")
}

// changes counts the paths that the member changed against the start tip and lists those that the base does not show.
func (p *treeProof) changes(files map[string]string) (changed int, missing []string) {
	for path, blob := range files {
		if p.from[path] != blob {
			changed++

			if p.to[path] != blob {
				missing = append(missing, path)
			}
		}
	}

	removed, held := p.removals(files)

	return changed + removed, append(missing, held...)
}

// removals counts the paths that the member removed against the start tip and lists those that the base still holds.
func (p *treeProof) removals(files map[string]string) (removed int, held []string) {
	for path := range p.from {
		if _, kept := files[path]; kept {
			continue
		}

		removed++

		if _, there := p.to[path]; there {
			held = append(held, path)
		}
	}

	return removed, held
}
