package github

import (
	"context"
	"fmt"
	"strconv"

	"github.com/tobocop2/obee/internal/forge"
	"github.com/tobocop2/obee/internal/model"
)

const (
	flagRaw   = "-f"
	flagTyped = "-F"

	changeRequestsQuery = "query($o:String!,$n:String!,$p:Int!){repository(owner:$o,name:$n){pullRequest(" +
		"number:$p){latestOpinionatedReviews(first:100,writersOnly:false){totalCount nodes{" +
		"author{login} state commit{oid}}}}}}"
	threadsQuery = "query($o:String!,$n:String!,$p:Int!){repository(owner:$o,name:$n){pullRequest(" +
		"number:$p){reviewThreads(first:100){totalCount nodes{isResolved path line " +
		"comments(first:1){nodes{author{login} body}}}}}}}"
	stackQuery = "query($o:String!,$n:String!,$p:Int!){repository(owner:$o,name:$n){pullRequest(" +
		"number:$p){stack{id}}}}"
)

type envelope[T any] struct {
	Data *struct {
		Repository *struct {
			PullRequest *T `json:"pullRequest"`
		} `json:"repository"`
	} `json:"data"`
}

// pull returns the pull request of the answer, or nil when the answer holds none.
func (e envelope[T]) pull() *T {
	if e.Data == nil || e.Data.Repository == nil {
		return nil
	}

	return e.Data.Repository.PullRequest
}

type reviewNode struct {
	Author model.Account
	State  model.ReviewState
	Commit struct{ Oid string }
}

type reviewPage struct {
	TotalCount int          `json:"totalCount"`
	Nodes      []reviewNode `json:"nodes"`
}

type threadNode struct {
	IsResolved bool    `json:"isResolved"`
	Path       *string `json:"path"`
	Line       *int    `json:"line"`
	Comments   struct {
		Nodes []model.ThreadComment `json:"nodes"`
	} `json:"comments"`
}

type threadPage struct {
	TotalCount int          `json:"totalCount"`
	Nodes      []threadNode `json:"nodes"`
}

// graphql runs one query for a pull request. The owner and the name go as strings; the number goes typed.
func (f *Forge) graphql(ctx context.Context, pr int, query string) model.CommandResult {
	return f.run.Run(ctx, []string{argAPI, "graphql", flagRaw, "o=" + f.owner, flagRaw, "n=" + f.name, flagTyped, "p=" + strconv.Itoa(pr), flagRaw, "query=" + query})
}

// readPull runs a query and decodes the pull request part of the answer. A nil result means the answer holds none.
func readPull[T any](ctx context.Context, f *Forge, pr int, query, base string) (*T, error) {
	done := f.graphql(ctx, pr, query)
	if done.ExitCode != 0 {
		return nil, forge.Refuse("%s", base)
	}

	answer, err := model.Decode[envelope[T]](cmdGraphQL, done.Stdout)
	if err != nil {
		return nil, forge.Refusal(base, err)
	}

	return answer.pull(), nil
}

// OpenChangeRequests returns the change requests that still stand. More reviewers than one page, or a failed read, is a refusal.
func (f *Forge) OpenChangeRequests(ctx context.Context, pr int) ([]model.ChangeRequest, error) {
	base := fmt.Sprintf("could not read the reviews of pull request %d", pr)

	pull, err := readPull[struct {
		Latest *reviewPage `json:"latestOpinionatedReviews"`
	}](ctx, f, pr, changeRequestsQuery, base)
	if err != nil {
		return nil, err
	}

	if pull == nil || pull.Latest == nil {
		return nil, forge.Refuse("%s: the answer holds none", base)
	}

	more := fmt.Sprintf("pull request %d has reviews from more than one page of reviewers; the rest are unknown", pr)
	if err := fullPage(pull.Latest.Nodes, pull.Latest.TotalCount, base, "review list", more); err != nil {
		return nil, err
	}

	return standing(pull.Latest.Nodes), nil
}

// fullPage refuses an answer with no node list, and an answer whose total is more than the nodes it holds.
func fullPage[N any](nodes []N, total int, base, list, more string) error {
	if nodes == nil {
		return forge.Refuse("%s: the answer holds no %s", base, list)
	}

	if total > len(nodes) {
		return forge.Refuse("%s", more)
	}

	return nil
}

func standing(nodes []reviewNode) []model.ChangeRequest {
	found := []model.ChangeRequest{}

	for _, node := range nodes {
		if node.State == model.ReviewChangesRequested {
			found = append(found, model.ChangeRequest{Login: node.Author.Login, Commit: node.Commit.Oid})
		}
	}

	return found
}

// UnresolvedThreads returns the review threads that are not resolved. More threads than one page, or a failed read, is a refusal.
func (f *Forge) UnresolvedThreads(ctx context.Context, pr int) ([]model.ReviewThread, error) {
	base := fmt.Sprintf("cannot read the review threads of pull request %d", pr)

	pull, err := readPull[struct {
		Threads *threadPage `json:"reviewThreads"`
	}](ctx, f, pr, threadsQuery, base)
	if err != nil {
		return nil, err
	}

	if pull == nil || pull.Threads == nil {
		return nil, forge.Refuse("%s: the answer holds none", base)
	}

	more := fmt.Sprintf("pull request %d has %d review threads, more than one page; the rest are unknown, not resolved", pr, pull.Threads.TotalCount)
	if err := fullPage(pull.Threads.Nodes, pull.Threads.TotalCount, base, "thread list", more); err != nil {
		return nil, err
	}

	return unresolved(pull.Threads.Nodes), nil
}

func unresolved(nodes []threadNode) []model.ReviewThread {
	open := []model.ReviewThread{}

	for _, node := range nodes {
		if node.IsResolved {
			continue
		}

		thread := model.ReviewThread{Path: node.Path, Line: node.Line}
		if len(node.Comments.Nodes) > 0 {
			thread.First = &node.Comments.Nodes[0]
		}

		open = append(open, thread)
	}

	return open
}

// StackID returns the native stack a pull request belongs to, or an empty string when it is in none.
func (f *Forge) StackID(ctx context.Context, pr int) (string, error) {
	base := fmt.Sprintf("cannot read the stack of pull request %d", pr)

	pull, err := readPull[struct {
		Stack *struct {
			ID string `json:"id"`
		} `json:"stack"`
	}](ctx, f, pr, stackQuery, base)
	if err != nil {
		return "", err
	}

	switch {
	case pull == nil:
		return "", forge.Refuse("%s: no such pull request", base)
	case pull.Stack == nil:
		return "", nil
	}

	return pull.Stack.ID, nil
}
