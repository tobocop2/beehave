package github

import (
	"context"

	"github.com/tobocop2/beehave/internal/forge"
	"github.com/tobocop2/beehave/internal/model"
)

// The list reads that need the association of each author. gh pr list and gh issue list do not give it, so each list is one paged GraphQL query.
// Each query aliases its one paged list as list. The two pull request lists order newest first, as gh pr list does; the search keeps the order of the forge.
const (
	pageOf       = "first:100,after:$endCursor"
	pagedList    = "totalCount pageInfo{hasNextPage endCursor} nodes"
	historyNode  = "{number title body state headRefName baseRefName mergedAt closedAt authorAssociation}"
	newestFirst  = "orderBy:{field:CREATED_AT,direction:DESC}"
	repositoryOf = "repository(owner:$o,name:$n)"

	openPullsQuery = "query($o:String!,$n:String!,$b:String,$endCursor:String){" + repositoryOf + "{list:pullRequests(states:OPEN,baseRefName:$b," + newestFirst + "," + pageOf + "){" +
		pagedList + "{number headRefName baseRefName labels(first:100){nodes{name}} reviewDecision author{login} authorAssociation isDraft title}}}}"
	branchPullsQuery = "query($o:String!,$n:String!,$h:String!,$endCursor:String){" + repositoryOf + "{list:pullRequests(headRefName:$h," + newestFirst + "," + pageOf + "){" +
		pagedList + historyNode + "}}}"
	searchPullsQuery = "query($q:String!,$endCursor:String){list:search(query:$q,type:ISSUE," + pageOf + "){" +
		"totalCount:issueCount pageInfo{hasNextPage endCursor} nodes{... on PullRequest" + historyNode + "}}}"
)

// openNode is one pull request of the open list. GraphQL gives the labels as a page of nodes.
type openNode struct {
	model.PullRequest

	Labels struct {
		Nodes []struct {
			Name string `json:"name"`
		} `json:"nodes"`
	} `json:"labels"`
}

// open is the typed row of the node.
func (n openNode) open() model.OpenPull {
	open := n.Open()
	for _, one := range n.Labels.Nodes {
		open.Labels = append(open.Labels, one.Name)
	}

	return open
}

// ofRepository is the owner and the name of the repository as variables, then the other variables.
func (f *Forge) ofRepository(vars ...string) []string {
	return append([]string{"o=" + f.owner, "n=" + f.name}, vars...)
}

// readList runs a paged GraphQL query with the variables, each written name=value, and returns every row of every page.
// A failed call, an answer of another shape and a cut list are refusals that start with base.
func readList[T any](ctx context.Context, f *Forge, base, query string, vars []string) ([]T, error) {
	args := []string{argAPI, argGraphQL, argPaginate, argSlurp}
	for _, one := range vars {
		args = append(args, flagRaw, one)
	}

	done := f.run.Run(ctx, append(args, flagRaw, "query="+query))
	if done.ExitCode != 0 {
		return nil, forge.Refuse("%s", base)
	}

	rows, err := model.DecodeGraphPages[T](cmdGraphQL, done.Stdout)
	if err != nil {
		return nil, forge.Refusal(base, err)
	}

	return rows, nil
}

// BranchPulls lists the pull requests in every state whose head is the branch, newest first, with the association of each author.
func (f *Forge) BranchPulls(ctx context.Context, branch string) ([]model.PullRequest, error) {
	return readList[model.PullRequest](ctx, f, "cannot list the pull requests of branch "+branch, branchPullsQuery, f.ofRepository("h="+branch))
}

// SearchPulls lists the pull requests in every state that the search words of the forge find in the repository, with the association of each author.
func (f *Forge) SearchPulls(ctx context.Context, words string) ([]model.PullRequest, error) {
	return readList[model.PullRequest](ctx, f, "cannot search the pull requests ("+words+")", searchPullsQuery, []string{"q=repo:" + f.repo + " is:pr " + words})
}
