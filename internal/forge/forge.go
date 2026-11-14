// Package forge holds the contract of a code host, with its errors. Each backend is a child package.
package forge

import (
	"context"

	"github.com/tobocop2/beehave/internal/model"
)

// Forge is a code host: pull requests, reviews, threads and checks. GitHub implements it.
// A failed command is an error. Only BranchRef tells a missing branch (found false, no error) from a failure.
type Forge interface {
	PullReads
	Activity
	Lookup
	Standing
	ItemReads
	Remote
}

// Opener is the optional write side of a forge that opens pull requests and links them into native stacks. A forge that lacks it cannot be used by pr_open.
// CreatePR opens a draft pull request and returns its number. The caller finds an open pull request on the branch first, so a repeat opens no second one.
// LinkStack puts child on top of parent in a native stack, and starts the stack when parent is in none. Linking pull requests that are already linked changes nothing.
// A non-empty base is the branch that the bottom of the stack targets. An empty base leaves the choice to the forge, which can move the bottom to the default branch.
// Unstack removes the whole stack that the pull request is in.
type Opener interface {
	CreatePR(ctx context.Context, req model.NewPull) (int, error)
	LinkStack(ctx context.Context, parent, child int, base string) error
	Unstack(ctx context.Context, pr int) error
}

// Boarder is the optional side of a forge that keeps project boards. A forge that lacks it cannot be used by board_sync.
// PlaceCard puts the card on the board and sets its Status column. The same call again changes nothing, and a column that the board lacks is refused before any write.
// CardStatus reads the Status column of the card from the board. It is an error when the card is not on the board.
type Boarder interface {
	PlaceCard(ctx context.Context, card model.Card) error
	CardStatus(ctx context.Context, card model.Card) (string, error)
}

// Remote names where the repository of the forge is cloned from. CloneURL needs no call to the forge.
type Remote interface {
	CloneURL() string
}

// ItemReads reads a pull request as a workflow item. ItemFields lists the names of the fields that Item returns and needs no call to the forge.
// Item returns one value for each declared field. A failed read is an error.
type ItemReads interface {
	ItemFields() []string
	Item(ctx context.Context, pr int) (map[string]string, error)
}

// Standing is the review state of our own open pull requests, read in one call.
type Standing interface {
	OwnPulls(ctx context.Context, author string) ([]model.OwnPull, error)
}

// PullReads are the reads of one pull request: its reviews, body, fields, checks, threads and overlap.
type PullReads interface {
	Reviews(ctx context.Context, pr int) ([]model.Review, error)
	PRBody(ctx context.Context, pr int) (string, error)
	PRView(ctx context.Context, pr int, fields ...string) (model.PullRequest, error)
	PRList(ctx context.Context, limit int, args ...string) ([]model.PullRequest, error)
	PRChecks(ctx context.Context, pr int) ([]model.Check, error)
	OpenChangeRequests(ctx context.Context, pr int) ([]model.ChangeRequest, error)
	UnresolvedThreads(ctx context.Context, pr int) ([]model.ReviewThread, error)
	StackID(ctx context.Context, pr int) (string, error)
	ExistingReview(ctx context.Context, pr int) ([]string, error)
	Overlap(ctx context.Context, req model.OverlapRequest) (model.Overlap, error)
}

// Activity is what is said on a pull request and what is done to it. Every method but the two reads changes the forge.
// MergePR merges only if the head of the pull request is still sha, a full 40-character commit id. A shorter pin is refused.
// CreateBranch makes a branch at sha and fails if the branch exists. SetBase retargets a pull request. SetBody replaces its body, but only on a pull request that the owner wrote: any other author, and an author that cannot be read, is a model.NotAuthorError.
type Activity interface {
	Comments(ctx context.Context, number int) ([]model.Comment, error)
	ReviewComments(ctx context.Context, pr int) ([]model.Comment, error)
	PostComment(ctx context.Context, pr int, body string) error
	SetDraft(ctx context.Context, pr int, draft bool) error
	MergePR(ctx context.Context, pr int, sha string) error
	CreateBranch(ctx context.Context, branch, sha string) error
	SetBase(ctx context.Context, pr int, base string) error
	SetBody(ctx context.Context, pr int, body string) error
	// AddAssignee assigns a pull request or an issue to a login. The same login again changes nothing, so a repeated call is safe.
	AddAssignee(ctx context.Context, number int, login string) error
}

// Lookup finds items and branches: a search, the default branch and the tip of a branch.
// TokenScopes lists the scopes of the token the forge command line uses.
// PullCounts returns the forge's own count of the commits and changed files of a pull request. A list in PRView can be cut short, so a caller compares the count to the list.
// Committers returns the committer of each commit of a pull request, in the order of the commits.
type Lookup interface {
	PullCounts(ctx context.Context, pr int) (commits, files int, err error)
	Committers(ctx context.Context, pr int) ([]model.Committer, error)
	TokenScopes(ctx context.Context) ([]string, error)
	Search(ctx context.Context, req model.SearchRequest) ([]int, error)
	DefaultBranch(ctx context.Context) (string, error)
	BranchRef(ctx context.Context, branch string) (sha string, found bool, err error)
	Title(ctx context.Context, number int) (string, error)
	CheckNotStarted(ctx context.Context, link string) (bool, error)
	// ClosingRefs reads the issues that merging a pull request closes, as the forge lists them, with the base branch and the default branch.
	// A forge that cannot answer returns an error, never an empty list.
	ClosingRefs(ctx context.Context, pr int) (model.ClosingRefs, error)
	// IssueState reads the state of an issue: OPEN or CLOSED. A forge that cannot answer returns an error, never a state.
	IssueState(ctx context.Context, number int) (string, error)
}
