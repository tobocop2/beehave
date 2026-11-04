package github

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/tobocop2/obee/internal/forge"
	"github.com/tobocop2/obee/internal/model"
)

const (
	cmdIssueView = "gh issue view"
	cmdAnnotate  = "gh api check-runs"
	cmdSearchAPI = "gh api graphql search"
	notStarted   = "was not started"
	ownBase      = "cannot read our open pull requests"

	ownLimit = 1000

	ownQuery = "query($q:String!,$endCursor:String){search(query:$q,type:ISSUE,first:100,after:$endCursor){issueCount pageInfo{hasNextPage endCursor} nodes{... on PullRequest{" +
		"number isDraft baseRefName title commits(last:1){nodes{commit{committedDate}}} " +
		"reviews(first:100){totalCount nodes{author{login} submittedAt}} " +
		"latestOpinionatedReviews(first:100,writersOnly:false){totalCount nodes{author{login} state commit{oid}}} " +
		"reviewThreads(first:100){totalCount nodes{isResolved}}}}}}"
)

type ownAnswer struct {
	Data *struct {
		Search *struct {
			IssueCount int       `json:"issueCount"`
			Nodes      []ownNode `json:"nodes"`
		} `json:"search"`
	} `json:"data"`
}

type ownNode struct {
	Number      int    `json:"number"`
	IsDraft     bool   `json:"isDraft"`
	BaseRefName string `json:"baseRefName"`
	Title       string `json:"title"`
	Commits     struct {
		Nodes []struct {
			Commit struct {
				CommittedDate string `json:"committedDate"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
	Reviews struct {
		TotalCount int `json:"totalCount"`
		Nodes      []struct {
			Author      model.Account `json:"author"`
			SubmittedAt *string       `json:"submittedAt"`
		} `json:"nodes"`
	} `json:"reviews"`
	Latest  *reviewPage `json:"latestOpinionatedReviews"`
	Threads *threadPage `json:"reviewThreads"`
}

// OwnPulls reads the reviews, change requests, threads and last push of every open pull request of an author in one call.
// The pull requests come in pages of 100 up to 1000, sorted by number; more than 1000 is a refusal. A pull request with more reviews, change requests or threads than one page is read again
// one list at a time; a list the per-pull-request read also refuses for length is marked unknown.
func (f *Forge) OwnPulls(ctx context.Context, author string) ([]model.OwnPull, error) {
	nodes, err := f.searchOwn(ctx, author)
	if err != nil {
		return nil, err
	}

	pulls := make([]model.OwnPull, 0, len(nodes))

	for _, node := range nodes {
		pull, err := f.ownPull(ctx, node)
		if err != nil {
			return nil, err
		}

		pulls = append(pulls, pull)
	}

	slices.SortFunc(pulls, func(a, b model.OwnPull) int { return cmp.Compare(a.Number, b.Number) })

	return pulls, nil
}

// searchOwn runs the batched query with gh api graphql --paginate --slurp, which follows the cursor, and returns every pull request node.
// A count of the first page above ownLimit is a refusal, and so is a node count that differs from the count the forge reports.
func (f *Forge) searchOwn(ctx context.Context, author string) ([]ownNode, error) {
	done := f.run.Run(ctx, []string{argAPI, "graphql", argPaginate, "--slurp", flagRaw, "q=repo:" + f.repo + " is:pr is:open author:" + author, flagRaw, "query=" + ownQuery})
	if done.ExitCode != 0 {
		return nil, forge.Refuse("%s", ownBase)
	}

	answers, err := model.Decode[[]ownAnswer](cmdSearchAPI, done.Stdout)
	if err != nil {
		return nil, forge.Refusal(ownBase, err)
	}

	return countedNodes(answers)
}

// countedNodes joins the nodes of the pages. The count of the first page is the count of all rows.
func countedNodes(answers []ownAnswer) ([]ownNode, error) {
	var nodes []ownNode

	for _, answer := range answers {
		if answer.Data == nil || answer.Data.Search == nil {
			return nil, forge.Refuse("%s: the answer holds none", ownBase)
		}

		nodes = append(nodes, answer.Data.Search.Nodes...)
	}

	if len(answers) == 0 {
		return nil, forge.Refuse("%s: the answer holds none", ownBase)
	}

	return nodes, checkCount(answers[0].Data.Search.IssueCount, len(nodes))
}

func checkCount(count, read int) error {
	switch {
	case count > ownLimit:
		return forge.Refuse("%s: %d pull requests, more than %d", ownBase, count, ownLimit)
	case read != count:
		return forge.Refuse("%s: %d pull requests counted, %d read", ownBase, count, read)
	}

	return nil
}

// ownPull builds the state of one pull request. A list longer than one page is read again with the per-pull-request reads.
func (f *Forge) ownPull(ctx context.Context, node ownNode) (model.OwnPull, error) {
	pull := model.OwnPull{Number: node.Number, Draft: node.IsDraft, Base: node.BaseRefName, Title: node.Title, Reviews: []model.ReviewStamp{}, ChangeRequests: []model.ChangeRequest{}}

	if len(node.Commits.Nodes) > 0 {
		pull.Push = node.Commits.Nodes[0].Commit.CommittedDate
	}

	if err := f.fillReviews(ctx, &pull, node); err != nil {
		return model.OwnPull{}, err
	}

	if err := f.fillChanges(ctx, &pull, node.Latest); err != nil {
		return model.OwnPull{}, err
	}

	return pull, f.fillThreads(ctx, &pull, node.Threads)
}

func (f *Forge) fillReviews(ctx context.Context, pull *model.OwnPull, node ownNode) error {
	if node.Reviews.TotalCount > len(node.Reviews.Nodes) {
		all, err := f.Reviews(ctx, pull.Number)
		if err != nil {
			return err
		}

		for _, review := range all {
			pull.Reviews = appendStamp(pull.Reviews, review.User.Login, review.SubmittedAt)
		}

		return nil
	}

	for _, review := range node.Reviews.Nodes {
		pull.Reviews = appendStamp(pull.Reviews, review.Author.Login, review.SubmittedAt)
	}

	return nil
}

func appendStamp(stamps []model.ReviewStamp, login string, at *string) []model.ReviewStamp {
	if at == nil {
		return stamps
	}

	return append(stamps, model.ReviewStamp{Login: login, SubmittedAt: *at})
}

func (f *Forge) fillChanges(ctx context.Context, pull *model.OwnPull, latest *reviewPage) error {
	switch {
	case latest == nil:
		return nil
	case latest.TotalCount <= len(latest.Nodes):
		pull.ChangesKnown, pull.ChangeRequests = true, standing(latest.Nodes)

		return nil
	}

	found, err := f.OpenChangeRequests(ctx, pull.Number)
	if errors.Is(err, model.ErrMorePages) {
		return nil
	}

	pull.ChangesKnown, pull.ChangeRequests = err == nil, found

	return err
}

func (f *Forge) fillThreads(ctx context.Context, pull *model.OwnPull, threads *threadPage) error {
	switch {
	case threads == nil:
		return nil
	case threads.TotalCount <= len(threads.Nodes):
		pull.ThreadsKnown, pull.OpenThreads = true, len(unresolved(threads.Nodes))

		return nil
	}

	open, err := f.UnresolvedThreads(ctx, pull.Number)
	if errors.Is(err, model.ErrMorePages) {
		return nil
	}

	pull.ThreadsKnown, pull.OpenThreads = err == nil, len(open)

	return err
}

// Title returns the title of an issue or pull request.
func (f *Forge) Title(ctx context.Context, number int) (string, error) {
	text, err := f.issueField(ctx, number, "title", fmt.Sprintf("cannot read the title of #%d", number))

	return strings.TrimSuffix(text, "\n"), err
}

// issueField prints one field of an issue with gh issue view. A failed read is a refusal with the message.
func (f *Forge) issueField(ctx context.Context, number int, field, message string) (string, error) {
	done := f.run.Run(ctx, []string{"issue", argView, strconv.Itoa(number), "-R", f.repo, argJSON, field, argJQ, "." + field})
	if done.ExitCode != 0 {
		return "", forge.Refuse("%s", message)
	}

	return done.Stdout, nil
}

// IssueState returns the state of an issue as the forge names it: OPEN or CLOSED.
func (f *Forge) IssueState(ctx context.Context, number int) (string, error) {
	base := fmt.Sprintf("cannot read the state of issue #%d", number)

	text, err := f.issueField(ctx, number, "state", base)
	if err != nil {
		return "", err
	}

	state := strings.ToUpper(strings.TrimSpace(text))
	if state != "OPEN" && state != "CLOSED" {
		return "", forge.Refuse("%s: the forge answered %q", base, state)
	}

	return state, nil
}

type annotation struct {
	Message *string `json:"message"`
}

// CheckNotStarted reports whether the first annotation of a check run, named by the last part of its link, says the run was not started.
func (f *Forge) CheckNotStarted(ctx context.Context, link string) (bool, error) {
	id := link[strings.LastIndex(link, "/")+1:]
	if _, err := strconv.ParseUint(id, 10, 64); err != nil {
		return false, forge.Refuse("the link %q does not end in a check run number", link)
	}

	done := f.run.Run(ctx, []string{argAPI, "repos/" + f.repo + "/check-runs/" + id + "/annotations"})
	if done.ExitCode != 0 {
		return false, forge.Refuse("cannot read the annotations of check run %s", id)
	}

	notes, err := model.Decode[[]annotation](cmdAnnotate, done.Stdout)
	if err != nil {
		return false, forge.Refusal("cannot read the annotations of check run "+id, err)
	}

	return len(notes) > 0 && notes[0].Message != nil && strings.Contains(*notes[0].Message, notStarted), nil
}
