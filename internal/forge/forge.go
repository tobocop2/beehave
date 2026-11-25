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
	PullFactsRead
	OpenPullsRead
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

// Syncer is the optional side of a forge that brings one branch into another with no checkout. A forge that lacks it cannot sync a branch.
// CommitsAhead returns the number of commits that head has and base lacks. A failed read is an error, never zero.
// MergeBranch merges head into base on the forge and returns the sha of the new merge commit. An empty sha means base already holds head, and nothing changed.
// A merge that conflicts changes nothing and is an error with the cause ErrMergeConflict.
type Syncer interface {
	CommitsAhead(ctx context.Context, base, head string) (int, error)
	MergeBranch(ctx context.Context, base, head string) (string, error)
}

// Statuses is the optional side of a forge that posts commit statuses and reads what a branch requires. A forge that lacks it cannot post beehave/review or beehave/gate.
// PostStatus sets one status on the commit that status.SHA names, a full 40-character commit id. The same status again changes nothing, and a later state replaces an earlier one.
// Protection reads the pull request rule and the required status contexts that apply to a branch. A failed read is an error, never an empty protection.
type Statuses interface {
	PostStatus(ctx context.Context, status model.CommitStatus) error
	Protection(ctx context.Context, branch string) (model.Protection, error)
}

// AssigneeReads is the optional side of a forge that reads who holds an issue. A forge that lacks it cannot serve the assignee watch of preflight.
// IssueAssignees returns the logins that hold the issue. An empty repo is the repository of the forge, and any other repo is read on that repository. A failed read is an error, never an empty list.
type AssigneeReads interface {
	IssueAssignees(ctx context.Context, repo string, number int) ([]string, error)
}

// AssociationReads is the optional side of a forge that reads who wrote an issue or a pull request. A forge that lacks it cannot tell an item of the owner from a foreign one.
// Association returns how the author of the issue or pull request with the number stands to the repository of the forge. A failed read or an unknown value is an error.
type AssociationReads interface {
	Association(ctx context.Context, number int) (model.Association, error)
}

// ReadyReads is the optional side of a forge that reads when a pull request was marked ready for review.
// ReadyMarks returns the login of each account that marked the pull request ready, in order. A failed read is an error, never an empty list.
type ReadyReads interface {
	ReadyMarks(ctx context.Context, pr int) ([]string, error)
}

// Boarder is the optional side of a forge that keeps project boards. A forge that lacks it cannot be used by the board command of the remediation example.
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

// PullFactsRead is the typed read of one pull request.
// PullRead returns the number asked for, the state, head commit, base branch, author and assignees.
// A read that lacks the state, the head or the base is a *model.IncompletePull, never a partial answer: the facts it holds are in the error.
type PullFactsRead interface {
	PullRead(ctx context.Context, pr int) (model.PullFacts, error)
}

// OpenPullLimit is the limit of the search for the open pull requests. A list at the limit is refused, so a caller never reads a cut list.
const OpenPullLimit = 500

// OpenPullsRead is the typed list read of the open pull requests.
// OpenPulls returns the open pull requests of the base branch, in the order of the forge, or every open pull request when base is empty. A base with none gives an empty list.
// A pull request that lacks the number, the head branch or the base branch is a hole. The list still holds it, and the error joins one *model.IncompletePull for each hole.
// A failed read returns no list. A list at the limit of the forge is a failed read.
type OpenPullsRead interface {
	OpenPulls(ctx context.Context, base string) ([]model.OpenPull, error)
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
}

// Lookup finds items and branches: a search, the default branch and the tip of a branch.
// CanWriteCI answers whether the token the forge command line uses can change CI files: yes, no or unknown. A read that fails is an error, never an answer.
// PullCounts returns the forge's own count of the commits and changed files of a pull request. A list in PRView can be cut short, so a caller compares the count to the list.
// Committers returns the committer of each commit of a pull request, in the order of the commits.
type Lookup interface {
	PullCounts(ctx context.Context, pr int) (commits, files int, err error)
	Committers(ctx context.Context, pr int) ([]model.Committer, error)
	CanWriteCI(ctx context.Context) (model.CIWrite, error)
	Search(ctx context.Context, req model.SearchRequest) ([]int, error)
	DefaultBranch(ctx context.Context) (string, error)
	BranchRef(ctx context.Context, branch string) (sha string, found bool, err error)
	Title(ctx context.Context, number int) (string, error)
	CheckNotStarted(ctx context.Context, link string) (bool, error)
	// ClosingRefs reads the issues that merging a pull request closes, with the base branch and the default branch.
	// For a base other than the default branch, where the forge lists none, the issues are the closing keywords of the body and the commit messages.
	// A forge that cannot answer returns an error, never an empty list.
	ClosingRefs(ctx context.Context, pr int) (model.ClosingRefs, error)
	// IssueState reads the state of an issue: OPEN or CLOSED. A forge that cannot answer returns an error, never a state.
	IssueState(ctx context.Context, number int) (string, error)
}
