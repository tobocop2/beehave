package model

import (
	"errors"
	"slices"
	"strconv"
	"strings"
)

// NotAuthorError is the refusal of a body write because the pull request is not the work of the owner: its author is another login, or cannot be read.
// Reason is the whole text of the refusal.
type NotAuthorError struct {
	Reason string
}

func (e *NotAuthorError) Error() string { return e.Reason }

// Account is a GitHub user as gh prints it. Login is empty when gh sends none.
type Account struct {
	Login string `json:"login"`
}

// NewPull is a draft pull request to open: the branch with the commits, the branch to merge into, the title and body, and the label. An empty Label adds none.
type NewPull struct {
	Head  string
	Base  string
	Title string
	Body  string
	Label string
}

// Card is one card of a GitHub Project board: the pull request (Pull) or issue (not Pull) with the number, on the board with the number Board.
// Owner is the login that owns the board, empty for the owner of the repository. Status is the name of the column.
type Card struct {
	Board  int
	Owner  string
	Number int
	Pull   bool
	Status string
}

// changedFile is one file a pull request changes.
type changedFile struct {
	Path string `json:"path"`
}

// label is a label on a pull request.
type label struct {
	Name string `json:"name"`
}

// commitAuthor is one author of a commit.
type commitAuthor struct {
	Email string `json:"email"`
	Login string `json:"login"`
}

// commit is one commit of a pull request, as gh pr view prints it.
type commit struct {
	Oid             string         `json:"oid"`
	MessageHeadline string         `json:"messageHeadline"`
	MessageBody     string         `json:"messageBody"`
	CommittedDate   string         `json:"committedDate"`
	Authors         []commitAuthor `json:"authors"`
}

// reviewRequest is a reviewer asked for a review: a user (Login) or a team (Name).
type reviewRequest struct {
	Login string `json:"login"`
	Name  string `json:"name"`
}

// repository is the repository a pull request branch lives in.
type repository struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// pullComment is a conversation comment as gh pr view prints it. Nil CreatedAt means gh sent null.
type pullComment struct {
	Author    Account `json:"author"`
	Body      string  `json:"body"`
	CreatedAt *string `json:"createdAt"`
}

// pullReview is a review as gh pr view prints it. Nil SubmittedAt means gh sent null, as for a draft review.
type pullReview struct {
	Author      Account     `json:"author"`
	Body        string      `json:"body"`
	State       ReviewState `json:"state"`
	SubmittedAt *string     `json:"submittedAt"`
}

// ClosingRefs is what one forge read tells about the issues a pull request closes: the issues the forge lists, the base branch of the pull request and the default branch of the repository.
// An empty BaseBranch or DefaultBranch means the answer held none, so a caller must not guess.
type ClosingRefs struct {
	BaseBranch    string
	DefaultBranch string
	Issues        []int
	// Elsewhere are the issues of other repositories that the pull request closes, as full references, sorted and without a repeat. The result is never nil.
	Elsewhere []IssueRef
}

// PullRequest holds the fields of gh pr list and gh pr view that beehave reads.
// A pointer field is one a gate decides on: nil means gh sent null or the call did not ask for it, so a gate must not read nil as false or empty.
type PullRequest struct {
	Number         int             `json:"number"`
	Title          string          `json:"title"`
	Body           string          `json:"body"`
	State          *string         `json:"state"`
	Mergeable      *string         `json:"mergeable"`
	IsDraft        *bool           `json:"isDraft"`
	BaseRefName    *string         `json:"baseRefName"`
	HeadRefName    *string         `json:"headRefName"`
	HeadRefOid     *string         `json:"headRefOid"`
	ReviewDecision *string         `json:"reviewDecision"`
	MergedAt       *string         `json:"mergedAt"`
	ClosedAt       *string         `json:"closedAt"`
	UpdatedAt      string          `json:"updatedAt"`
	HeadRepository *repository     `json:"headRepository"`
	Author         Account         `json:"author"`
	Assignees      []Account       `json:"assignees"`
	Labels         []label         `json:"labels"`
	Files          []changedFile   `json:"files"`
	Commits        []commit        `json:"commits"`
	Comments       []pullComment   `json:"comments"`
	Reviews        []pullReview    `json:"reviews"`
	ReviewRequests []reviewRequest `json:"reviewRequests"`
}

// PullFacts is the typed read of one pull request: its number, state, head commit, base branch, author login and assignee logins,
// the draft flag, the label names, the issues that its body links in the order of the body, and the logins that reviewed it or were asked to.
// A label with no name reads as null.
type PullFacts struct {
	Number    int
	State     string
	Head      string
	Base      string
	Author    string
	Assignees []string
	Draft     bool
	Labels    []string
	Links     []IssueLink
	Reviewers []string
	// Unread names what a lenient read could not give: the assignee list, or a link of the body. A caller that needs it refuses the facts.
	Unread []string
}

// Facts is the typed read of the view: a field the view lacks is empty. The links of the body are not read here, because a link can be refused: see Read.
func (p PullRequest) Facts() PullFacts {
	facts := PullFacts{Number: p.Number, State: Deref(p.State), Head: Deref(p.HeadRefOid), Base: Deref(p.BaseRefName), Author: p.Author.Login}
	if p.IsDraft != nil {
		facts.Draft = *p.IsDraft
	}

	facts.Assignees = logins(p.Assignees)
	facts.Labels = names(p.Labels)
	facts.Reviewers = append(authors(p.Reviews), requested(p.ReviewRequests)...)

	return facts
}

// logins lists the login of each account.
func logins(accounts []Account) []string {
	var out []string
	for _, who := range accounts {
		out = append(out, who.Login)
	}

	return out
}

// names lists the name of each label. A label with no name reads as the word null.
func names(tags []label) []string {
	var out []string
	for _, tag := range tags {
		out = append(out, OrNull(tag.Name))
	}

	return out
}

// authors lists the login of the author of each review.
func authors(reviews []pullReview) []string {
	var out []string
	for _, review := range reviews {
		out = append(out, review.Author.Login)
	}

	return out
}

// requested lists the login of each review request.
func requested(asks []reviewRequest) []string {
	var out []string
	for _, ask := range asks {
		out = append(out, ask.Login)
	}

	return out
}

// Read is the typed read of pull request number pr from a view of the fields number, state, headRefOid, baseRefName, author, assignees, isDraft, labels, body, reviews and reviewRequests.
// A read that lacks the state, the head, the base, the draft flag, the label list or the assignee list is an *IncompletePull, because a null would pass as an open, ready pull request that nobody holds.
// A body whose link names a number that is not an int is refused too.
func (p PullRequest) Read(pr int) (PullFacts, error) {
	facts := p.Facts()
	facts.Number = pr

	links, err := BodyLinks(p.Body)
	if err != nil {
		if hole := facts.incomplete(p.nullFields()); hole != nil {
			return facts, hole
		}

		return facts, &LinkError{Facts: facts, Cause: err}
	}

	facts.Links = links

	return facts, facts.incomplete(p.nullFields())
}

// WithoutListRefusals is a typed read as the merge needs it: a read that only lacks the draft flag, the label list or the assignee list, or whose body has a link
// that is not an int, gives the facts it did read and no error. The merge uses none of those. Any other refusal stays.
func WithoutListRefusals(facts PullFacts, err error) (PullFacts, error) {
	var (
		hole *IncompletePull
		link *LinkError
	)

	switch {
	case errors.As(err, &link):
		return link.Facts, nil
	case errors.As(err, &hole) && onlyListFields(hole.Missing):
		return hole.Facts, nil
	default:
		return facts, err
	}
}

// onlyListFields is true when every missing field is the draft flag, the label list or the assignee list.
func onlyListFields(missing []string) bool {
	for _, name := range missing {
		if !slices.Contains([]string{"draft", "labels", "assignees"}, name) {
			return false
		}
	}

	return true
}

// LinkError is the refusal of a read whose body has a link with a number that is not an int. Facts holds what the read did give, with no links.
type LinkError struct {
	Facts PullFacts
	Cause error
}

func (e *LinkError) Error() string { return e.Cause.Error() }

func (e *LinkError) Unwrap() error { return e.Cause }

// nullFields names the fields that the view gave as null and that a typed read needs: the draft flag, the label list and the assignee list.
func (p PullRequest) nullFields() []string {
	var nulls []string

	for _, field := range []struct {
		name string
		null bool
	}{{"draft", p.IsDraft == nil}, {"labels", p.Labels == nil}, {"assignees", p.Assignees == nil}} {
		if field.null {
			nulls = append(nulls, field.name)
		}
	}

	return nulls
}

// IncompletePull is the refusal of a typed read that lacks the state, the head, the base, the draft flag, the label list or the assignee list. Facts holds what the read did give, and Missing names each field it lacked.
type IncompletePull struct {
	Facts   PullFacts
	Missing []string
}

func (e *IncompletePull) Error() string {
	return "cannot read pull request " + strconv.Itoa(e.Facts.Number) + ": the read has no " + strings.Join(e.Missing, ", ")
}

// Check is nil when the facts hold a state, a head and a base. Else it is an IncompletePull that names each missing field.
func (f PullFacts) Check() error { return f.incomplete(nil) }

// incomplete is Check with the names of more fields that the read lacked. Those come after the state, head and base.
func (f PullFacts) incomplete(more []string) error {
	var missing []string

	for _, field := range []struct{ name, value string }{{"state", f.State}, {"head", f.Head}, {"base", f.Base}} {
		if field.value == "" {
			missing = append(missing, field.name)
		}
	}

	missing = append(missing, more...)
	if len(missing) == 0 {
		return nil
	}

	return &IncompletePull{Facts: f, Missing: missing}
}

// OpenPull is one open pull request as the typed list read gives it: its number, head branch, base branch, author login, draft state, title, label names and review decision.
// ReviewDecision is empty when the forge shows none.
type OpenPull struct {
	Number         int
	Branch         string
	Base           string
	Author         string
	Draft          bool
	Title          string
	Labels         []string
	ReviewDecision string
}

// Open is the typed read of a pull request of a list: a field the list lacks is empty.
func (p PullRequest) Open() OpenPull {
	open := OpenPull{
		Number: p.Number, Branch: Deref(p.HeadRefName), Base: Deref(p.BaseRefName), Author: p.Author.Login,
		Draft: p.IsDraft != nil && *p.IsDraft, Title: p.Title, ReviewDecision: Deref(p.ReviewDecision),
	}

	for _, one := range p.Labels {
		open.Labels = append(open.Labels, one.Name)
	}

	return open
}

// Check is nil when the pull request holds a number, a head branch and a base branch. Else it is an IncompletePull that names each missing field.
func (p OpenPull) Check() error {
	var missing []string

	if p.Number < 1 {
		missing = append(missing, "number")
	}

	if p.Branch == "" {
		missing = append(missing, "branch")
	}

	if p.Base == "" {
		missing = append(missing, "base")
	}

	if len(missing) == 0 {
		return nil
	}

	return &IncompletePull{Facts: PullFacts{Number: p.Number, Base: p.Base}, Missing: missing}
}

// PriorPull is one pull request of an item in any state. MergedAt is empty when it never merged; ClosingComment is the body of its last comment made at or before its close, empty for an open one.
type PriorPull struct {
	Number         int    `json:"number"`
	Title          string `json:"title"`
	State          string `json:"state"`
	Branch         string `json:"branch"`
	Base           string `json:"base"`
	MergedAt       string `json:"merged_at"`
	ClosingComment string `json:"closing_comment"`
}

// Committer is the committer of one commit of a pull request. A Login or an Email that the forge does not send is empty.
// The commit list of gh pr view names no committer, so the REST API supplies this one.
type Committer struct {
	Login string
	Email string
}

// ReviewState is the state gh reports for a review.
type ReviewState string

// The review states GitHub reports.
const (
	ReviewApproved         ReviewState = "APPROVED"
	ReviewChangesRequested ReviewState = "CHANGES_REQUESTED"
	ReviewCommented        ReviewState = "COMMENTED"
	ReviewDismissed        ReviewState = "DISMISSED"
	ReviewPending          ReviewState = "PENDING"
)

// Review is one review of a pull request, as the REST API prints it. Nil SubmittedAt or Body means the API sent null.
type Review struct {
	User        Account     `json:"user"`
	State       ReviewState `json:"state"`
	SubmittedAt *string     `json:"submitted_at"`
	CommitID    string      `json:"commit_id"`
	Body        *string     `json:"body"`
}

// Comment is an inline or conversation comment, as the REST API prints it. Nil Line means the API sent null.
type Comment struct {
	User         Account `json:"user"`
	Path         string  `json:"path"`
	Line         *int    `json:"line"`
	OriginalLine *int    `json:"original_line"`
	CreatedAt    string  `json:"created_at"`
	Body         string  `json:"body"`
}

// ThreadComment is the first comment of a review thread.
type ThreadComment struct {
	Author Account
	Body   *string
}

// ReviewThread is an unresolved review thread. First is nil when the thread holds no comment.
type ReviewThread struct {
	Path  *string
	Line  *int
	First *ThreadComment
}

// threadCommentWidth is how many characters of the first comment a thread summary shows.
const threadCommentWidth = 80

const nullWord = "null"

// Summary returns `<path>:<line> <author>: <comment>`, with null for a missing part, as jq prints it.
func (t ReviewThread) Summary() string {
	return t.Where() + ": " + t.Comment()
}

// Where returns `<path>:<line> <author>`, with null for a missing part.
func (t ReviewThread) Where() string {
	path, line, author := nullWord, nullWord, nullWord
	if t.Path != nil {
		path = *t.Path
	}

	if t.Line != nil {
		line = strconv.Itoa(*t.Line)
	}

	if t.First != nil {
		author = OrNull(t.First.Author.Login)
	}

	return path + ":" + line + " " + author
}

// Comment returns at most threadCommentWidth characters of the first comment, and null when there is none.
func (t ReviewThread) Comment() string {
	if t.First == nil {
		return nullWord
	}

	return firstRunes(t.First.Body)
}

func firstRunes(text *string) string {
	if text == nil {
		return nullWord
	}

	return FirstRunes(*text, threadCommentWidth)
}

// FirstRunes returns at most limit characters of text, cut at a character and never inside one.
func FirstRunes(text string, limit int) string {
	runes := []rune(text)

	return string(runes[:min(len(runes), limit)])
}

// shaWidth is how many characters of a commit id a change request shows.
const shaWidth = 9

// ChangeRequest is a change request that still stands.
type ChangeRequest struct {
	Login  string
	Commit string
}

// String returns `<login>@<first 9 characters of the commit id>`.
func (c ChangeRequest) String() string {
	return c.Login + "@" + string([]rune(c.Commit)[:min(len([]rune(c.Commit)), shaWidth)])
}

// JoinChangeRequests lists change requests separated by commas.
func JoinChangeRequests(requests []ChangeRequest) string {
	names := make([]string, len(requests))
	for i, request := range requests {
		names[i] = request.String()
	}

	return strings.Join(names, ",")
}

// CheckBucket is how gh judges one check.
type CheckBucket string

// The buckets a merge tells apart: a check that passed, one still running, one that skipped, one that failed and one that was canceled.
// The decoder keeps every bucket as text.
const (
	CheckPass     CheckBucket = "pass"
	CheckPending  CheckBucket = "pending"
	CheckSkipping CheckBucket = "skipping"
	CheckFail     CheckBucket = "fail"
	CheckCancel   CheckBucket = "cancel"
)

// Check is one check of a pull request.
type Check struct {
	Name     string      `json:"name"`
	Bucket   CheckBucket `json:"bucket"`
	Workflow string      `json:"workflow"`
	Link     string      `json:"link"`
}

// SearchKind is what a search looks for: issues or pull requests.
type SearchKind string

// The kinds a search takes. The values are the gh search subcommands.
const (
	SearchIssues SearchKind = "issues"
	SearchPulls  SearchKind = "prs"
)

// SearchRequest asks for the open items of one kind that involve a user and changed on or after Since.
// Since is an ISO date or time. A search that returns Limit rows may be cut off and is refused.
type SearchRequest struct {
	Kind     SearchKind
	Involves string
	Since    string
	Limit    int
}

// OrNull returns text, or the word null when text is empty, as jq prints a missing value.
func OrNull(text string) string {
	if text == "" {
		return nullWord
	}

	return text
}

// The contexts of the commit statuses that a base must require before a merge: the gate on the head, and the review of the head.
const (
	ContextGate   = "beehave/gate"
	ContextReview = "beehave/review"
)

// StatusState is the state of a commit status. The values are the states that GitHub accepts.
type StatusState string

// The states a commit status takes.
const (
	StatusSuccess StatusState = "success"
	StatusFailure StatusState = "failure"
	StatusPending StatusState = "pending"
	StatusError   StatusState = "error"
)

// CommitStatus is one status of one commit: Context names the check, State says how it went, and SHA is the full commit id that it stands for.
// A status belongs to its commit, so a push to a branch leaves the new head with no status.
type CommitStatus struct {
	SHA         string      `json:"sha"`
	Context     string      `json:"context"`
	State       StatusState `json:"state"`
	Description string      `json:"description"`
}

// Protection is what a forge requires of a branch before a pull request merges into it: whether a pull request is required, and the contexts of the statuses.
type Protection struct {
	PullRequest      bool
	RequiredStatuses []string
}
