package github

import (
	"context"
	"slices"
	"strconv"

	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/store"
)

var _ store.Writer = (*Store)(nil)

const (
	argView   = "view"
	argBody   = "--body"
	argEdit   = "edit"
	closed    = "CLOSED"
	keyedPage = 1000
	// recentPage is how many of the newest issues the second lookup of a key reads.
	recentPage = 100
)

// ghKeyed is one issue found by a search for a key.
type ghKeyed struct {
	Number int    `json:"number"`
	Body   string `json:"body"`
}

type ghState struct {
	State  string    `json:"state"`
	Labels []ghLabel `json:"labels"`
}

// ghComment is one comment of `gh api repos/<repo>/issues/<n>/comments`.
type ghComment struct {
	Body string `json:"body"`
}

// Create files an issue for the key. It writes the line `beehave-key: <key>` at the end of the body and, before it writes, searches all issues for that line, so a repeated key returns the first issue.
// The search index of GitHub can lag behind a create, so a search that finds nothing is followed by a read of the newest issues, which does not use the index.
// A parent is set with --parent, which needs gh 2.101 or newer. The step record of the engine is the first guard
// against a second write, and the key covers the window in which a crash comes between the create and the record.
func (s *Store) Create(ctx context.Context, key string, item store.NewItem) (string, bool, error) {
	found, err := s.keyed(ctx, key)
	if err != nil || found != "" {
		return found, false, err
	}

	id, err := s.file(ctx, key, item)

	return id, err == nil, err
}

// file runs gh issue create and returns the number of the new issue, which is the end of the URL that gh prints.
func (s *Store) file(ctx context.Context, key string, item store.NewItem) (string, error) {
	done := s.run.Run(ctx, s.createArgs(key, item))
	if done.ExitCode != 0 {
		return "", store.Refuse("REFUSE: cannot create an item in %s", s.repo)
	}

	id, ok := model.URLNumber(done.Stdout, "issues")
	if !ok {
		return "", store.Refuse("store: gh did not print the URL of the new issue in %s; refusing", s.repo)
	}

	return strconv.Itoa(id), nil
}

// createArgs are the arguments of gh issue create, with the marker of the key. The parent, the assignee and the severity label are set only when the item has one.
func (s *Store) createArgs(key string, item store.NewItem) []string {
	args := []string{argIssue, "create", argRepo, s.repo, "--title", item.Title, argBody, store.WithMarker(item.Body, key)}
	if item.Parent != "" {
		args = append(args, "--parent", item.Parent)
	}

	if item.Assignee != "" {
		args = append(args, "--assignee", item.Assignee)
	}

	if item.Severity != "" {
		args = append(args, "--label", item.Severity)
	}

	if item.Operator {
		args = append(args, "--label", store.OperatorLabel)
	}

	return args
}

// keyed returns the number of the issue whose body holds the marker line of the key, or empty. A full page of matches is a refusal, as in every list.
func (s *Store) keyed(ctx context.Context, key string) (string, error) {
	found, err := s.searched(ctx, key)
	if err != nil || found != "" {
		return found, err
	}

	return s.listed(ctx, key)
}

// listed is the second lookup. The search index has no promise of freshness, so a create that landed a moment ago can be missing from it.
// A list without --search reads the newest issues of the repository, and the issue of a resumed create is among them.
func (s *Store) listed(ctx context.Context, key string) (string, error) {
	done := s.run.Run(ctx, []string{
		argIssue, argList, argRepo, s.repo, argState, "all", argJSON, "number,body", argLimit, strconv.Itoa(recentPage),
	})
	if done.ExitCode != 0 {
		return "", store.Refuse("store: cannot list the issues of %s; refusing", s.repo)
	}

	rows, err := model.Decode[[]ghKeyed](listCommand, done.Stdout)
	if err != nil {
		return "", store.Unreadable(err)
	}

	return firstWithMarker(rows, key), nil
}

// searched finds the issue with the index of the GitHub search.
func (s *Store) searched(ctx context.Context, key string) (string, error) {
	done := s.run.Run(ctx, []string{
		argIssue, argList, argRepo, s.repo, argState, "all", "--search", `"` + store.Marker(key) + `" in:body`,
		argJSON, "number,body", argLimit, strconv.Itoa(keyedPage),
	})
	if done.ExitCode != 0 {
		return "", store.Refuse("store: cannot search the issues of %s; refusing", s.repo)
	}

	rows, err := store.DecodeRows[ghKeyed](listCommand, done.Stdout, keyedPage, "the issues of a key")
	if err != nil {
		return "", err
	}

	return firstWithMarker(rows, key), nil
}

// firstWithMarker returns the number of the first issue whose body holds the whole marker line of the key, or empty.
func firstWithMarker(rows []ghKeyed, key string) string {
	for _, row := range rows {
		if store.HasMarker(row.Body, key) {
			return strconv.Itoa(row.Number)
		}
	}

	return ""
}

// number checks that the id is an issue number.
func number(id string) error {
	if n, err := strconv.Atoi(id); err != nil || n <= 0 {
		return store.Refuse("REFUSE: %q is not an issue number", id)
	}

	return nil
}

// read runs gh issue view for the fields and decodes the answer.
func read[T any](ctx context.Context, s *Store, id, fields string) (T, error) { //nolint:ireturn // T is the shape of the fields
	var zero T

	if err := number(id); err != nil {
		return zero, err
	}

	done := s.run.Run(ctx, []string{argIssue, argView, id, argRepo, s.repo, argJSON, fields})
	if done.ExitCode != 0 {
		return zero, store.Refuse("REFUSE: cannot read item %s in %s", id, s.repo)
	}

	got, err := model.Decode[T](viewCommand, done.Stdout)
	if err != nil {
		return zero, store.Unreadable(err)
	}

	return got, nil
}

// SetStatus reads the state and the labels of the issue and runs only the commands that change them: close, reopen, and the hold label. open = no hold label and not closed.
func (s *Store) SetStatus(ctx context.Context, id string, status store.Status) error {
	now, err := read[ghState](ctx, s, id, "state,labels")
	if err != nil {
		return err
	}

	held := slices.ContainsFunc(now.Labels, func(l ghLabel) bool { return l.Name == holdLabel })

	if status == store.StatusClosed {
		return s.step(ctx, now.State != closed, "close", id)
	}

	if err = s.step(ctx, now.State == closed, "reopen", id); err != nil {
		return err
	}

	if status == store.StatusHold {
		return s.hold(ctx, id, held)
	}

	return s.step(ctx, held, argEdit, id, "--remove-label", holdLabel)
}

// hold adds the hold label. gh does not create a label, so the refusal keeps the cause and names the label as the likely fix.
func (s *Store) hold(ctx context.Context, id string, held bool) error {
	if err := s.step(ctx, !held, argEdit, id, "--add-label", holdLabel); err != nil {
		return store.Refuse("REFUSE: cannot put item %s in hold in %s: %v. If the repository has no label named %q, create it first", id, s.repo, err, holdLabel)
	}

	return nil
}

// step runs one gh issue command when run is true.
func (s *Store) step(ctx context.Context, run bool, command, id string, extra ...string) error {
	if !run {
		return nil
	}

	done := s.run.Run(ctx, append([]string{argIssue, command, id, argRepo, s.repo}, extra...))
	if done.ExitCode != 0 {
		return store.Refuse("REFUSE: cannot %s item %s in %s", command, id, s.repo)
	}

	return nil
}

// Assign adds the assignee to the issue. The forge keeps one entry for a login, so the same call twice changes nothing.
func (s *Store) Assign(ctx context.Context, id, assignee string) error {
	return s.step(ctx, true, argEdit, id, "--add-assignee", assignee)
}

// Comment adds a comment with the marker line of the key at its end, once for each key: it reads the comments of the issue first and returns when one holds the marker.
func (s *Store) Comment(ctx context.Context, id, key, text string) error {
	seen, err := s.comments(ctx, id)
	if err != nil {
		return err
	}

	if slices.ContainsFunc(seen, func(c ghComment) bool { return store.HasMarker(c.Body, key) }) {
		return nil
	}

	return s.step(ctx, true, "comment", id, argBody, store.WithMarker(text, key))
}

// comments reads every comment of the issue. `gh issue view --json comments` gives no page option, so this reads the REST list with --paginate, which follows every page;
// --slurp joins the pages into one JSON array. Manual: https://cli.github.com/manual/gh_api
func (s *Store) comments(ctx context.Context, id string) ([]ghComment, error) {
	if err := number(id); err != nil {
		return nil, err
	}

	done := s.run.Run(ctx, []string{"api", "--paginate", "--slurp", "repos/" + s.repo + "/issues/" + id + "/comments"})
	if done.ExitCode != 0 {
		return nil, store.Refuse("REFUSE: cannot read the comments of item %s in %s", id, s.repo)
	}

	rows, err := model.DecodePages[ghComment]("gh api", done.Stdout)

	return rows, store.Unreadable(err)
}
