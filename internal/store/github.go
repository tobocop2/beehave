package store

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/tobocop2/obee/internal/model"
)

const (
	argAPI       = "api"
	argIssue     = "issue"
	graphqlArg   = "graphql"
	argRepo      = "--repo"
	argJSON      = "--json"
	argList      = "list"
	ghPage       = 1000
	childPage    = 100
	epicPrefix   = "epic:"
	holdLabel    = "hold"
	nullText     = "null"
	listCommand  = "gh issue list"
	viewCommand  = "gh issue view"
	graphCommand = "gh api graphql"
)

type ghIssue struct {
	Number    int       `json:"number"`
	Title     *string   `json:"title"`
	Labels    []ghLabel `json:"labels"`
	Assignees []ghUser  `json:"assignees"`
}

type ghLabel struct {
	Name string `json:"name"`
}

type ghUser struct {
	Login string `json:"login"`
}

// GithubStore is the open issues of one repository, read through gh.
type GithubStore struct {
	repo  string
	owner string
	name  string
	run   Runner
}

// NewGithubStore returns the store of repo, written owner/name.
func NewGithubStore(repo string, run Runner) (*GithubStore, error) {
	owner, name, found := strings.Cut(repo, "/")
	if !found || !repoPart(owner) || !repoPart(name) {
		return nil, refuse("REFUSE: REPO is not set to owner/name. Export it, or put it in round.env.")
	}

	return &GithubStore{repo: repo, owner: owner, name: name, run: run}, nil
}

// repoPart reports whether text is a non-empty run of letters, digits, dots, underscores and hyphens.
func repoPart(text string) bool {
	return text != "" && strings.IndexFunc(text, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '.' && r != '_' && r != '-'
	}) < 0
}

// OpenItems lists the open issues that are not epics. An issue with no P0 to P3 label has severity P?.
func (s *GithubStore) OpenItems(ctx context.Context) ([]model.Item, error) {
	rows, err := s.openPage(ctx)
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
	title, err := readableTitle(i.Title)
	if err != nil {
		return "", "", err
	}

	if i.Number <= 0 {
		return "", "", refuse("store: an item has no readable number; refusing")
	}

	return title, strconv.Itoa(i.Number), nil
}

func (i ghIssue) item(number, title string) model.Item {
	severity, hold := model.SeverityUnknown, false
	found := false

	for _, label := range i.Labels {
		hold = hold || label.Name == holdLabel
		if parsed, err := model.ParseSeverity(label.Name); err == nil && !found {
			severity, found = parsed, true
		}
	}

	logins := make([]string, len(i.Assignees))
	for index, user := range i.Assignees {
		logins[index] = user.Login
	}

	return model.Item{ID: number, Severity: severity, Hold: hold, Assignee: strings.Join(logins, ","), Title: title}
}

// Containers lists the numbers of the open issues titled as an epic.
func (s *GithubStore) Containers(ctx context.Context) ([]string, error) {
	rows, err := s.openPage(ctx)
	if err != nil {
		return nil, err
	}

	var epics []string

	for _, row := range rows {
		title, number, err := row.identity()
		if err != nil {
			return nil, err
		}

		if strings.HasPrefix(title, epicPrefix) {
			epics = append(epics, number)
		}
	}

	return epics, nil
}

type childAnswer struct {
	Data struct {
		Repository struct {
			Issue struct {
				SubIssues struct {
					TotalCount *int        `json:"totalCount"`
					Nodes      []childNode `json:"nodes"`
				} `json:"subIssues"`
			} `json:"issue"`
		} `json:"repository"`
	} `json:"data"`
}

type childNode struct {
	Number int    `json:"number"`
	State  string `json:"state"`
}

// Children lists the open sub-issues of one issue. More than one page is a refusal.
func (s *GithubStore) Children(ctx context.Context, container string) ([]string, error) {
	failure := refuse("store: cannot read the children of %s; refusing to rank", container)
	if !model.IsDigits(container) {
		return nil, failure
	}

	query := fmt.Sprintf(`{repository(owner:"%s",name:"%s"){issue(number:%s){subIssues(first:%d){totalCount nodes{number state}}}}}`,
		s.owner, s.name, container, childPage)

	done := s.run.Run(ctx, []string{argAPI, graphqlArg, "-f", "query=" + query})
	if done.ExitCode != 0 {
		return nil, failure
	}

	answer, err := decode[childAnswer](graphCommand, done.Stdout)
	if err != nil {
		return nil, err
	}

	return openChildren(container, answer, failure)
}

func openChildren(container string, answer childAnswer, failure error) ([]string, error) {
	sub := answer.Data.Repository.Issue.SubIssues
	if sub.TotalCount == nil {
		return nil, refuse("store: no child count for %s; refusing to rank", container)
	}

	if *sub.TotalCount > childPage {
		return nil, refuse("store: %s has %d children, more than one page; refusing to rank", container, *sub.TotalCount)
	}

	if sub.Nodes == nil {
		return nil, failure
	}

	return openNumbers(sub.Nodes)
}

func openNumbers(nodes []childNode) ([]string, error) {
	var open []string

	for _, node := range nodes {
		if node.Number <= 0 {
			return nil, refuse("store: an item has no readable number; refusing")
		}

		if node.State == "OPEN" {
			open = append(open, strconv.Itoa(node.Number))
		}
	}

	return open, nil
}

type ghView struct {
	Number   int         `json:"number"`
	Title    *string     `json:"title"`
	Body     *string     `json:"body"`
	Comments []ghComment `json:"comments"`
}

type ghComment struct {
	Author    struct{ Login *string } `json:"author"`
	CreatedAt *string                 `json:"createdAt"`
	Body      *string                 `json:"body"`
}

// Show returns the title, body and every comment of an issue. An unreadable issue is a refusal with code 2.
func (s *GithubStore) Show(ctx context.Context, item string) (string, error) {
	unreadable := refuse("REFUSE: cannot read item %s in %s", item, s.repo)

	done := s.run.Run(ctx, []string{argIssue, "view", item, argRepo, s.repo, argJSON, "number,title,body,comments"})
	if done.ExitCode != 0 {
		return "", unreadable
	}

	issue, err := decode[ghView](viewCommand, done.Stdout)
	if err != nil {
		return "", err
	}

	if issue.Comments == nil {
		return "", unreadable
	}

	return issue.render()
}

func (v ghView) render() (string, error) {
	title, number, err := ghIssue{Number: v.Number, Title: v.Title}.identity()
	if err != nil {
		return "", err
	}

	comments := make([]string, len(v.Comments))
	for i, c := range v.Comments {
		comments[i] = fmt.Sprintf("## comment by %s at %s\n\n%s", orNull(c.Author.Login), orNull(c.CreatedAt), orNull(c.Body))
	}

	return fmt.Sprintf("# #%s %s\n\n%s\n\n%s\n", number, title, derefText(v.Body), strings.Join(comments, "\n\n")), nil
}

func orNull(text *string) string {
	if text == nil {
		return nullText
	}

	return *text
}

func derefText(text *string) string {
	if text == nil {
		return ""
	}

	return *text
}

// Close closes an issue with a comment. A rejected close is an error with code 1.
func (s *GithubStore) Close(ctx context.Context, item, reason string) error {
	done := s.run.Run(ctx, []string{argIssue, "close", item, argRepo, s.repo, "--comment", reason})
	if done.ExitCode != 0 {
		return &Error{Code: CodeCloseFailed, Message: fmt.Sprintf("store: cannot close %s: %s", item, strings.TrimSpace(done.Stderr))}
	}

	return nil
}

// Create files an issue and returns its number. A parent that cannot be linked is an error with code 4 that carries the number.
func (s *GithubStore) Create(ctx context.Context, request CreateRequest) (string, error) {
	if err := request.Validate(); err != nil {
		return "", err
	}

	titles, err := s.openTitles(ctx)
	if err != nil {
		return "", err
	}

	if err = refuseDuplicate(request, titles); err != nil {
		return "", err
	}

	number, err := s.file(ctx, request)
	if err != nil || request.Parent == "" {
		return number, err
	}

	return number, s.link(ctx, request.Parent, number)
}

func (s *GithubStore) openTitles(ctx context.Context) ([]openTitle, error) {
	rows, err := s.openPage(ctx)
	if err != nil {
		return nil, err
	}

	return openTitlesOf(rows)
}

func openTitlesOf(rows []ghIssue) ([]openTitle, error) {
	titles := make([]openTitle, len(rows))

	for i, row := range rows {
		title, number, err := row.identity()
		if err != nil {
			return nil, err
		}

		titles[i] = openTitle{id: number, title: title}
	}

	return titles, nil
}

func (s *GithubStore) file(ctx context.Context, request CreateRequest) (string, error) {
	args := []string{
		argIssue, "create", argRepo, s.repo, "--title", request.Title,
		"--body-file", request.BodyFile, "--label", string(request.Severity),
	}
	if request.Kind != "" {
		args = append(args, "--label", request.Kind)
	}

	if request.Assignee != "" {
		args = append(args, "--assignee", request.Assignee)
	}

	done := s.run.Run(ctx, args)
	if done.ExitCode != 0 {
		return "", refuse("store: gh issue create failed")
	}

	return issueNumber(done.Stdout)
}

// issueNumber reads the number at the end of the URL that gh issue create prints.
func issueNumber(stdout string) (string, error) {
	url := strings.TrimRight(stdout, "\n")
	number := url[strings.LastIndex(url, "/")+1:]

	if !model.IsDigits(number) {
		return "", refuse("store: gh issue create printed '%s', not an issue URL", url)
	}

	return number, nil
}

type nodeIDs struct {
	Data struct {
		Repository struct {
			P struct{ ID string } `json:"p"`
			C struct{ ID string } `json:"c"`
		} `json:"repository"`
	} `json:"data"`
}

// link makes child a sub-issue of parent by node id, because gh issue create has no flag for it.
func (s *GithubStore) link(ctx context.Context, parent, child string) error {
	failure := &Error{
		Code:    CodeUnlinked,
		Message: fmt.Sprintf("store: filed #%s but could not link it under #%s; link it by hand", child, parent),
		ItemID:  child,
	}
	if !model.IsDigits(parent) {
		return failure
	}

	parentID, childID, ok := s.nodeIDs(ctx, parent, child)
	if !ok {
		return failure
	}

	mutation := fmt.Sprintf(`mutation{addSubIssue(input:{issueId:"%s",subIssueId:"%s"}){issue{number}}}`, parentID, childID)
	if s.run.Run(ctx, []string{argAPI, graphqlArg, "-f", "query=" + mutation}).ExitCode != 0 {
		return failure
	}

	return nil
}

func (s *GithubStore) nodeIDs(ctx context.Context, parent, child string) (string, string, bool) {
	ids := fmt.Sprintf(`{repository(owner:"%s",name:"%s"){p:issue(number:%s){id} c:issue(number:%s){id}}}`, s.owner, s.name, parent, child)

	done := s.run.Run(ctx, []string{argAPI, graphqlArg, "-f", "query=" + ids})
	if done.ExitCode != 0 {
		return "", "", false
	}

	found, err := decode[nodeIDs](graphCommand, done.Stdout)
	repository := found.Data.Repository

	return repository.P.ID, repository.C.ID, err == nil && repository.P.ID != "" && repository.C.ID != ""
}

// openPage reads one page of open issues. A full page may be cut off, so it is a refusal, in every read.
func (s *GithubStore) openPage(ctx context.Context) ([]ghIssue, error) {
	done := s.run.Run(ctx, []string{
		argIssue, argList, argRepo, s.repo, "--state", "open",
		"--limit", strconv.Itoa(ghPage), argJSON, "number,title,labels,assignees",
	})
	if done.ExitCode != 0 {
		return nil, refuse("store: cannot list the open issues; refusing")
	}

	return decodeRows[ghIssue](listCommand, done.Stdout, ghPage, "the open issues")
}
