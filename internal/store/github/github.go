// Package github implements store.Tracker over the gh command line and GitHub Issues.
package github

import (
	"context"
	"strconv"
	"strings"

	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/proc"
	"github.com/tobocop2/obee/internal/store"
)

const (
	argIssue    = "issue"
	argRepo     = "--repo"
	argJSON     = "--json"
	argList     = "list"
	argState    = "--state"
	argLimit    = "--limit"
	ghPage      = 1000
	epicPrefix  = "epic:"
	holdLabel   = "hold"
	listCommand = "gh issue list"
	viewCommand = "gh issue view"
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

// Store is the issues of one repository, read and written through gh. It implements store.Tracker and store.Writer.
type Store struct {
	repo string
	run  proc.Runner
}

// New returns the store of repo, written owner/name. It reads issues, and it writes them through the store.Writer methods. The claim rows live in beads.
func New(repo string, run proc.Runner) (*Store, error) {
	if _, _, ok := model.SplitRepo(repo); !ok {
		return nil, store.Refuse("REFUSE: the repository must be written owner/name.")
	}

	return &Store{repo: repo, run: run}, nil
}

// OpenItems lists the open issues that are not epics. An issue with no P0 to P3 label has severity P?.
func (s *Store) OpenItems(ctx context.Context) ([]model.Item, error) {
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

type ghView struct {
	Number int     `json:"number"`
	Title  *string `json:"title"`
	Body   *string `json:"body"`
}

// ItemFields declares the fields that Item returns.
func (*Store) ItemFields() []string { return model.ItemFields() }

// Item returns the number, title and body of an issue. An unreadable issue is a refusal with code 2.
func (s *Store) Item(ctx context.Context, item string) (map[string]string, error) {
	issue, err := read[ghView](ctx, s, item, "number,title,body")
	if err != nil {
		return nil, err
	}

	title, number, err := ghIssue{Number: issue.Number, Title: issue.Title}.identity()
	if err != nil {
		return nil, err
	}

	return map[string]string{"id": number, "title": title, "body": model.Deref(issue.Body)}, nil
}

// openPage reads one page of open issues. A full page may be cut off, so it is a refusal, in every read.
func (s *Store) openPage(ctx context.Context) ([]ghIssue, error) {
	done := s.run.Run(ctx, []string{
		argIssue, argList, argRepo, s.repo, argState, "open",
		argLimit, strconv.Itoa(ghPage), argJSON, "number,title,labels,assignees",
	})
	if done.ExitCode != 0 {
		return nil, store.Refuse("store: cannot list the open issues; refusing")
	}

	return store.DecodeRows[ghIssue](listCommand, done.Stdout, ghPage, "the open issues")
}
