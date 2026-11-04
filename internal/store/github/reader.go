package github

import (
	"context"
	"strconv"
	"strings"

	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/store"
)

var _ store.Reader = (*Store)(nil)

// Comments returns the text of every comment of the issue, oldest first. A marker line that a write added is dropped.
func (s *Store) Comments(ctx context.Context, id string) ([]string, error) {
	rows, err := s.comments(ctx, id)
	if err != nil {
		return nil, err
	}

	texts := make([]string, len(rows))
	for i, row := range rows {
		texts[i] = store.Visible(row.Body)
	}

	return texts, nil
}

// ghParent is the parent issue that `gh api repos/<repo>/issues/<n>/parent` prints.
type ghParent struct {
	Number int `json:"number"`
}

// noParent turns a failed read of the parent endpoint into nil for an issue with no parent, or into a refusal.
// The same 404 comes for a missing issue, a wrong repo and a token with no access, so only an issue that reads is one with no parent.
func (s *Store) noParent(ctx context.Context, id, stderr string) error {
	if !strings.Contains(stderr, "HTTP 404") {
		return store.Refuse("REFUSE: cannot read the parent of item %s in %s", id, s.repo)
	}

	_, err := s.Item(ctx, id)

	return err
}

// Parent returns the number of the parent issue, or an empty text for an issue with no parent. It reads the REST endpoint for sub-issues.
// The endpoint answers 404 for an issue with no parent, and for an issue that does not exist or cannot be read.
// After a 404 the issue itself is read: an issue that reads has no parent, and any other failure is a refusal.
// Manual: https://docs.github.com/en/rest/issues/sub-issues#get-parent-issue
func (s *Store) Parent(ctx context.Context, id string) (string, error) {
	if err := number(id); err != nil {
		return "", err
	}

	done := s.run.Run(ctx, []string{"api", "repos/" + s.repo + "/issues/" + id + "/parent"})
	if done.ExitCode != 0 {
		return "", s.noParent(ctx, id, done.Stderr)
	}

	parent, err := model.Decode[ghParent]("gh api", done.Stdout)
	if err != nil {
		return "", store.Unreadable(err)
	}

	return strconv.Itoa(parent.Number), nil
}
