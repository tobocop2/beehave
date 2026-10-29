// Package batch lands a set of reviewed pull requests with one gate run for the whole set.
// It folds the member heads onto the base tip with git, gates that union tree, and splits the set when the gate is red.
// The gate, the git calls, the forge and the step recorder are interfaces that the workflow builtin fills.
package batch

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Reasons a member is rejected or deferred.
const (
	// ReasonConflict is a member that conflicts with the base tip alone.
	ReasonConflict = "conflict"
	// ReasonRed is a member that fails the gate alone.
	ReasonRed = "red"
	// ReasonRedOnTop is a member that failed the gate after other members landed.
	ReasonRedOnTop = "red on top of the merged members"
	// ReasonMoved is a member that a stop left unlanded because the base moved.
	ReasonMoved = "the base moved while the batch ran; run the batch again"
)

// Member is one pull request of a batch. Head is the full commit id that a reviewer gated.
type Member struct {
	Item string `json:"item"`
	Head string `json:"head"`
}

// Tip is the base tip: a commit and its tree.
type Tip struct {
	Commit string `json:"commit"`
	Tree   string `json:"tree"`
}

// MergeResult is what git's three-way merge of two commits made. A conflict still has a tree id, and it is not a result to use.
type MergeResult struct {
	Tree     string
	Conflict bool
}

// Git is the small set of git calls the union needs. Version is called first, once, then HasCommit once for each member.
// MergeTree and NewCommit need no working copy.
// NewCommit needs an author and committer identity in the git environment of the implementation.
// The interface is declared here and not in the merge package. The wiring implements it over the command runner.
type Git interface {
	Version(ctx context.Context) (string, error)
	// HasCommit reports whether the repository holds the commit id, so that an unfetched head is found before the fold.
	HasCommit(ctx context.Context, id string) (bool, error)
	MergeTree(ctx context.Context, ours, theirs string) (MergeResult, error)
	NewCommit(ctx context.Context, tree string, parents ...string) (string, error)
}

// GateInput is the union to gate. The gate checks out Commit, and Tree is the tree id of that commit.
type GateInput struct {
	Commit string
	Tree   string
}

// GateOutcome is what one gate run reported. Tree is the tree id the gate ran on, which must equal the tree of the union.
type GateOutcome struct {
	Tree   string `json:"tree"`
	Passed bool   `json:"passed"`
	Report string `json:"report"`
}

// Gate runs the project gate on one union. It runs one gate at a time.
// Run gates exactly GateInput.Commit, and the Tree of its outcome is the tree of that checkout, not a copy of the input.
type Gate interface {
	Run(ctx context.Context, in GateInput) (GateOutcome, error)
}

// MergeStatus is how the forge answered a member merge.
type MergeStatus int

const (
	// StatusMerged means the member is on the base, and the base tree equals the prefix tree.
	StatusMerged MergeStatus = iota
	// StatusRefused means a guard of the member refused the merge for good: its own work, a hold, its checks or its review.
	// The batch rejects the member with the reason, and gates the rest again.
	StatusRefused
	// StatusUnsettled means the forge could not answer yet, for example when mergeable stayed unknown after three reads.
	// The batch defers the member and every later one with the reason.
	StatusUnsettled
)

// MergeOutcome is the answer of Base.Merge. Reason is set for a refusal or an unsettled answer.
type MergeOutcome struct {
	Status MergeStatus
	Reason string
}

// Base reads the base tip and merges one member. Merge pins the merge to Member.Head and checks the base tip tree against prefixTree.
// A refusal of the member is an outcome and not an error. An error, for example a failed tree proof, stops the call.
// Merge is idempotent for one member: a replay after a crash calls it again for a member that may be merged already.
// The wiring makes it so with the intent record of merge.Run (merge.Request.Intent).
// Tip is read again before each member merge, and the read is a recorded step.
type Base interface {
	Tip(ctx context.Context) (Tip, error)
	Merge(ctx context.Context, m Member, prefixTree string) (MergeOutcome, error)
}

// Recorder is the durable step store. Step returns the recorded bytes when the name has a record, and otherwise runs and records them.
// Step does not record a run that failed, so a replay runs it again.
type Recorder interface {
	Step(ctx context.Context, name string, run func() ([]byte, error)) ([]byte, error)
}

// Deps is what a batch needs. Name makes the step names unique to one batch call.
// Name must not be empty, and it must differ from the name of every other batch call in the same workflow. Run refuses an empty one.
type Deps struct {
	Git   Git
	Gate  Gate
	Base  Base
	Steps Recorder
	Name  string
}

// Outcome is a member with the reason the batch did not land it.
type Outcome struct {
	Item   string `json:"item"`
	Reason string `json:"reason"`
	Report string `json:"report,omitempty"`
}

// Result is what a batch did. Gates counts the gate runs, and a union skipped by the tree memo is not one.
type Result struct {
	Merged   []string
	Rejected []Outcome
	Deferred []Outcome
	Tip      Tip
	Gates    int
}

// Bound is the most gate runs for n members and k bad members with a deterministic gate.
func Bound(n, k int) int {
	if n < 1 {
		return 0
	}

	depth := 0
	for 1<<depth < n {
		depth++
	}

	return min(2*n-1, 1+2*k*depth)
}

// Run lands the members. It folds them in item order onto the base tip, gates the union, and splits a red union.
// A member that conflicts only with an earlier member is retried once on the new tip. A member the base left behind is deferred.
// On an error, Result still lists the members that landed and the members rejected before the stop, and the error names the member it stopped at.
func Run(ctx context.Context, deps Deps, members []Member) (Result, error) {
	if deps.Name == "" {
		return Result{}, errors.New("a batch needs a name that is not empty and differs from every other batch call in the workflow")
	}

	sorted, err := normalize(members)
	if err != nil {
		return Result{}, err
	}

	if err := CheckGit(ctx, deps.Git); err != nil {
		return Result{}, err
	}

	return newRunner(deps).run(ctx, sorted)
}

func normalize(members []Member) ([]Member, error) {
	sorted := slices.Clone(members)
	slices.SortFunc(sorted, func(a, b Member) int { return strings.Compare(a.Item, b.Item) })

	for i, m := range sorted {
		if m.Item == "" || m.Head == "" {
			return nil, errors.New("a batch member needs an item and a head commit")
		}

		if i > 0 && sorted[i-1].Item == m.Item {
			return nil, fmt.Errorf("the batch lists %s twice", m.Item)
		}
	}

	return sorted, nil
}
