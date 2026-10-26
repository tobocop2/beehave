package model

import (
	"strconv"
	"strings"
)

// Account is a GitHub user as gh prints it. Login is empty when gh sends none.
type Account struct {
	Login string `json:"login"`
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

// PullRequest holds the fields of gh pr list and gh pr view that obee reads.
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
	MergedAt       *string         `json:"mergedAt"`
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
	path, line := nullWord, nullWord
	if t.Path != nil {
		path = *t.Path
	}

	if t.Line != nil {
		line = strconv.Itoa(*t.Line)
	}

	author, body := nullWord, nullWord
	if t.First != nil {
		author = OrNull(t.First.Author.Login)
		body = firstRunes(t.First.Body)
	}

	return path + ":" + line + " " + author + ": " + body
}

func firstRunes(text *string) string {
	if text == nil {
		return nullWord
	}

	runes := []rune(*text)

	return string(runes[:min(len(runes), threadCommentWidth)])
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

// CheckPass is the bucket of a check that passed. gh also reports fail, pending, skipping and cancel; the decoder keeps them as text.
const CheckPass CheckBucket = "pass"

// Check is one check of a pull request.
type Check struct {
	Name     string      `json:"name"`
	Bucket   CheckBucket `json:"bucket"`
	Workflow string      `json:"workflow"`
	Link     string      `json:"link"`
}

// OrNull returns text, or the word null when text is empty, as jq prints a missing value.
func OrNull(text string) string {
	if text == "" {
		return nullWord
	}

	return text
}
