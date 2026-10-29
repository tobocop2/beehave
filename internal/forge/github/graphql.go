package github

import (
	"context"
	"fmt"
	"strconv"
	"strings"

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
	closingQuery = "query($o:String!,$n:String!,$p:Int!){repository(owner:$o,name:$n){defaultBranchRef{name} pullRequest(" +
		"number:$p){baseRefName baseRepository{nameWithOwner} " +
		"closingIssuesReferences(first:100){totalCount nodes{number repository{nameWithOwner}}}}}}"
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
		return &forge.Error{Code: forge.CodeRefused, Message: more, Err: model.ErrMorePages}
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

type closingNode struct {
	Number     int `json:"number"`
	Repository struct {
		NameWithOwner string `json:"nameWithOwner"`
	} `json:"repository"`
}

type closingPage struct {
	TotalCount int           `json:"totalCount"`
	Nodes      []closingNode `json:"nodes"`
}

type closingAnswer struct {
	Data *struct {
		Repository *closingRepository `json:"repository"`
	} `json:"data"`
}

type closingRepository struct {
	DefaultBranchRef *struct {
		Name string `json:"name"`
	} `json:"defaultBranchRef"`
	PullRequest *struct {
		BaseRefName    *string `json:"baseRefName"`
		BaseRepository *struct {
			NameWithOwner string `json:"nameWithOwner"`
		} `json:"baseRepository"`
		Closing *closingPage `json:"closingIssuesReferences"`
	} `json:"pullRequest"`
}

// repository returns the repository of the answer when it holds a pull request with an issue list, and nil otherwise.
func (a closingAnswer) repository() *closingRepository {
	if a.Data == nil || a.Data.Repository == nil || a.Data.Repository.PullRequest == nil || a.Data.Repository.PullRequest.Closing == nil {
		return nil
	}

	return a.Data.Repository
}

// ClosingRefs reads, in one call, what the scope check needs: the issues that merging a pull request closes as GitHub lists them,
// the base branch of the pull request and the default branch of the repository. The list holds the keywords of the body and the issues linked in the sidebar.
// An issue is kept when its repository is the base repository of the same answer, so a renamed repository still works.
// More issues than one page, a list whose every issue is dropped, or a failed read, is a refusal.
func (f *Forge) ClosingRefs(ctx context.Context, pr int) (model.ClosingRefs, error) {
	base := fmt.Sprintf("cannot read the issues that pull request %d closes", pr)

	done := f.graphql(ctx, pr, closingQuery)
	if done.ExitCode != 0 {
		return model.ClosingRefs{}, forge.Refuse("%s", base)
	}

	answer, err := model.Decode[closingAnswer](cmdGraphQL, done.Stdout)
	if err != nil {
		return model.ClosingRefs{}, forge.Refusal(base, err)
	}

	repository := answer.repository()
	if repository == nil {
		return model.ClosingRefs{}, forge.Refuse("%s: the answer holds none", base)
	}

	return closingRefsOf(repository, pr, base)
}

// closingRefsOf checks the answer and builds the refs. It refuses a list of more than one page, an answer with no base repository,
// and a non-empty list whose every issue is in another repository.
func closingRefsOf(repository *closingRepository, pr int, base string) (model.ClosingRefs, error) {
	pull := repository.PullRequest

	more := fmt.Sprintf("pull request %d closes %d issues, more than one page; the rest are unknown", pr, pull.Closing.TotalCount)
	if err := fullPage(pull.Closing.Nodes, pull.Closing.TotalCount, base, "issue list", more); err != nil {
		return model.ClosingRefs{}, err
	}

	if pull.BaseRepository == nil {
		return model.ClosingRefs{}, forge.Refuse("%s: the answer holds no base repository", base)
	}

	issues := ownNumbers(pull.Closing.Nodes, pull.BaseRepository.NameWithOwner)
	if len(issues) == 0 && len(pull.Closing.Nodes) > 0 {
		return model.ClosingRefs{}, forge.Refuse("pull request %d lists %d closing issues, and none is in %s; the answer cannot be trusted", pr, len(pull.Closing.Nodes), pull.BaseRepository.NameWithOwner)
	}

	return model.ClosingRefs{Issues: issues, BaseBranch: repository.baseBranch(), DefaultBranch: repository.defaultBranch()}, nil
}

// baseBranch is the base branch of the pull request, or empty when the answer holds null.
func (r *closingRepository) baseBranch() string {
	if r.PullRequest.BaseRefName == nil {
		return ""
	}

	return *r.PullRequest.BaseRefName
}

// defaultBranch is the default branch of the repository, or empty when the answer holds null.
func (r *closingRepository) defaultBranch() string {
	if r.DefaultBranchRef == nil {
		return ""
	}

	return r.DefaultBranchRef.Name
}

// ownNumbers keeps the issues of the repository. An issue of another repository has a number of its own, and the ledger holds only this one's.
func ownNumbers(nodes []closingNode, repository string) []int {
	numbers := make([]int, 0, len(nodes))

	for _, node := range nodes {
		if strings.EqualFold(node.Repository.NameWithOwner, repository) {
			numbers = append(numbers, node.Number)
		}
	}

	return numbers
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
