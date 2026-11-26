// Package github implements store.Tracker over the gh command line and GitHub Issues.
package github

import (
	"context"
	"strconv"
	"strings"

	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc"
	"github.com/tobocop2/beehave/internal/store"
)

const (
	argAPI      = "api"
	argGraphQL  = "graphql"
	argPaginate = "--paginate"
	argSlurp    = "--slurp"
	flagRaw     = "-f"
	argIssue    = "issue"
	argRepo     = "--repo"
	argJSON     = "--json"
	argList     = "list"
	argState    = "--state"
	argLimit    = "--limit"
	epicPrefix  = "epic:"
	holdLabel   = "hold"
	listCommand = "gh issue list"
	viewCommand = "gh issue view"
	openCommand = "gh api graphql"

	// openIssuesQuery lists the open issues, newest first as gh issue list does, with the association of each author, which gh issue list does not give.
	openIssuesQuery = "query($o:String!,$n:String!,$l:[String!],$endCursor:String){repository(owner:$o,name:$n){list:issues(states:OPEN,labels:$l," +
		"orderBy:{field:CREATED_AT,direction:DESC},first:100,after:$endCursor){totalCount pageInfo{hasNextPage endCursor} " +
		"nodes{number title labels(first:100){nodes{name}} assignees(first:100){nodes{login}} authorAssociation}}}}"
)

// ghIssue is one open issue of the GraphQL list.
type ghIssue struct {
	Number            int               `json:"number"`
	Title             *string           `json:"title"`
	Labels            ghNodes[ghLabel]  `json:"labels"`
	Assignees         ghNodes[ghUser]   `json:"assignees"`
	AuthorAssociation model.Association `json:"authorAssociation"`
}

// ghNodes is a GraphQL list of one page.
type ghNodes[T any] struct {
	Nodes []T `json:"nodes"`
}

type ghLabel struct {
	Name string `json:"name"`
}

type ghUser struct {
	Login string `json:"login"`
}

// Store is the issues of one repository, read and written through gh. It implements store.Tracker and store.Writer.
type Store struct {
	repo string
	run  proc.Runner
}

// Tracker is the name the GitHub tracker is chosen by.
const Tracker model.TrackerName = "github"

// Tool is the program that the GitHub tracker needs on the path.
const Tool = "gh"

// New returns the store of repo, written owner/name. It reads issues, and it writes them through the store.Writer methods. The claim rows live in beads.
func New(repo string, run proc.Runner) (*Store, error) {
	if _, _, ok := model.SplitRepo(repo); !ok {
		return nil, store.Refuse("REFUSE: the repository must be written owner/name.")
	}

	return &Store{repo: repo, run: run}, nil
}

// ItemList lists the open issues that are not epics, and with a label only those that carry it: the query of the forge filters them.
// An issue with no P0 to P3 label has severity P?.
func (s *Store) ItemList(ctx context.Context, label string) ([]model.Item, error) {
	rows, err := s.openIssues(ctx, label)
	if err != nil {
		return nil, err
	}

	items := make([]model.Item, 0, len(rows))
	for _, row := range rows {
		title, number, err := row.identity()
		if err != nil {
			return nil, err
		}

		if !strings.HasPrefix(title, epicPrefix) {
			items = append(items, row.item(number, title))
		}
	}

	return items, nil
}

func (i ghIssue) identity() (string, string, error) {
	title, err := store.ReadableTitle(i.Title)
	if err != nil {
		return "", "", err
	}

	if i.Number <= 0 {
		return "", "", store.Refuse("store: an item has no readable number; refusing")
	}

	return title, strconv.Itoa(i.Number), nil
}

func (i ghIssue) item(number, title string) model.Item {
	severity, hold := severityOf(i.Labels.Nodes), false

	for _, label := range i.Labels.Nodes {
		hold = hold || label.Name == holdLabel
	}

	var labels []string
	for _, label := range i.Labels.Nodes {
		labels = append(labels, label.Name)
	}

	logins := make([]string, len(i.Assignees.Nodes))
	for index, user := range i.Assignees.Nodes {
		logins[index] = user.Login
	}

	return model.Item{ID: number, Severity: severity, Hold: hold, Assignee: strings.Join(logins, ","), Title: title, Labels: labels, Association: i.AuthorAssociation}
}

// severityOf gives the first label that names a severity, or P? when none does.
func severityOf(labels []ghLabel) model.Severity {
	for _, label := range labels {
		if parsed, err := model.ParseSeverity(label.Name); err == nil {
			return parsed
		}
	}

	return model.SeverityUnknown
}

type ghView struct {
	Number int       `json:"number"`
	Title  *string   `json:"title"`
	Body   *string   `json:"body"`
	Labels []ghLabel `json:"labels"`
}

// ItemFields declares the fields that Item returns.
func (*Store) ItemFields() []string { return model.ItemFields() }

// Item returns the number, title, body and severity of an issue. An unreadable issue is a refusal with code 2.
func (s *Store) Item(ctx context.Context, item string) (map[string]string, error) {
	issue, err := read[ghView](ctx, s, item, "number,title,body,labels")
	if err != nil {
		return nil, err
	}

	title, number, err := ghIssue{Number: issue.Number, Title: issue.Title}.identity()
	if err != nil {
		return nil, err
	}

	return map[string]string{"id": number, "title": title, "body": model.Deref(issue.Body), "severity": string(severityOf(issue.Labels))}, nil
}

// openIssues reads every page of the open issues in one call, with a label only those that carry it. A list cut short is unreadable.
func (s *Store) openIssues(ctx context.Context, label string) ([]ghIssue, error) {
	owner, name, _ := model.SplitRepo(s.repo)

	args := []string{argAPI, argGraphQL, argPaginate, argSlurp, flagRaw, "o=" + owner, flagRaw, "n=" + name}
	if label != "" {
		args = append(args, flagRaw, "l[]="+label)
	}

	done := s.run.Run(ctx, append(args, flagRaw, "query="+openIssuesQuery))
	if done.ExitCode != 0 {
		return nil, store.Refuse("store: cannot list the open issues; refusing")
	}

	rows, err := model.DecodeGraphPages[ghIssue](openCommand, done.Stdout)

	return rows, store.Unreadable(err)
}
