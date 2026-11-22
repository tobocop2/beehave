package takeover

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/tobocop2/beehave/internal/model"
)

// closing is the answer of gh pr view with the url and the closing issues. An issue of another repository is listed too.
type closing struct {
	URL    string `json:"url"`
	Issues []struct {
		Number int    `json:"number"`
		URL    string `json:"url"`
	} `json:"closingIssuesReferences"`
}

// repoOf is the owner/name part of the url of a pull request or an issue: the text between the host and the noun.
func repoOf(url, noun string) string {
	head, _, _ := strings.Cut(url, noun)
	head = strings.TrimPrefix(head, "https://")
	_, path, _ := strings.Cut(head, "/")

	return path
}

// linked reads the issues of this repository that merging the pull request closes, as gh lists them.
func (s *session) linked(ctx context.Context) ([]int, error) {
	number := strconv.Itoa(s.request.PR)

	done := s.c.Gh.Run(ctx, []string{"pr", "view", number, "--json", "url,closingIssuesReferences"})
	if done.ExitCode != 0 {
		return nil, fmt.Errorf("gh pr view %s: %s", number, strings.TrimSpace(done.Stderr))
	}

	found, err := model.Decode[closing]("gh pr view --json closingIssuesReferences", done.Stdout)
	if err != nil {
		return nil, err
	}

	return found.own(), nil
}

// own gives the numbers of the closing issues in the repository of the pull request.
func (c closing) own() []int {
	home := repoOf(c.URL, "/pull/")
	issues := []int{}

	for _, issue := range c.Issues {
		if repoOf(issue.URL, "/issues/") == home {
			issues = append(issues, issue.Number)
		}
	}

	return issues
}

// assign adds the acting login to the pull request and to every issue that it closes. The issues are read first, so a failed read leaves nothing assigned.
// gh adds an assignee that is already there without a change, so a repeated take is safe, and no assignee is removed.
func (s *session) assign(ctx context.Context) error {
	number := s.request.PR

	issues, err := s.linked(ctx)
	if err != nil {
		return unread(err, "cannot read the issues that #%d closes.", number)
	}

	if err = s.assignOne(ctx, "pr", number); err != nil {
		return err
	}

	for _, issue := range issues {
		if err = s.assignOne(ctx, "issue", issue); err != nil {
			return err
		}
	}

	s.say(s.assigned(issues))

	return nil
}

// assigned is the line that says what a take assigned.
func (s *session) assigned(issues []int) string {
	if len(issues) == 0 {
		return fmt.Sprintf("assigned #%d to %s.", s.request.PR, s.c.Me)
	}

	names := make([]string, len(issues))
	for i, issue := range issues {
		names[i] = strconv.Itoa(issue)
	}

	return fmt.Sprintf("assigned #%d and the issues %s to %s.", s.request.PR, strings.Join(names, ", "), s.c.Me)
}

// assignOne runs gh pr edit or gh issue edit with --add-assignee.
func (s *session) assignOne(ctx context.Context, noun string, number int) error {
	done := s.c.Gh.Run(ctx, []string{noun, "edit", strconv.Itoa(number), "--add-assignee", s.c.Me})
	if done.ExitCode != 0 {
		cause := fmt.Errorf("gh %s edit %d: %s", noun, number, strings.TrimSpace(done.Stderr))

		return unread(cause, "cannot assign %s %d to %s.", noun, number, s.c.Me)
	}

	return nil
}
