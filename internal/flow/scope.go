package flow

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// The recorded steps that keep the scope of a pull request. The first gate of an item fixes the scope. A merge that merges checks it.
const (
	scopeStep      = "scope"
	scopeCheckStep = "scope-check"
)

// errScopeRefused ends an item whose pull request closes an issue that is new since the first review and has no owner line.
var errScopeRefused = errors.New("the pull request closes an issue that is new since its first review")

// keepForm is the one form of an owner line: the word keep, then issue numbers with a leading #, separated by commas.
var keepForm = regexp.MustCompile(`(?i)^\s*keep\s+#\d+(?:\s*,\s*#\d+)*\s*$`)

// issueNumber finds one number in an owner line.
var issueNumber = regexp.MustCompile(`\d+`)

// scopeRead is what one step of the scope records: the closing issues of the pull request, or Found false when the item has no pull request yet.
type scopeRead struct {
	Found  bool  `json:"found"`
	Issues []int `json:"issues"`
}

// fixScope records the issues that the pull request of the item closes at the first review, which is the first gate that finds a pull request.
// A later gate of the run keeps that set and reads nothing. A replay reads the recorded set, so it holds the same scope.
func (s *script) fixScope(label string) error {
	if s.fixed {
		return nil
	}

	read, err := runStep(s.e, s.ctx, s.name(scopeStep, label), func(c context.Context) (scopeRead, error) {
		return s.readClosing(c)
	})
	if err != nil {
		return fmt.Errorf("%s of item %s: %w", scopeStep, s.in.Item, err)
	}

	if read.Found {
		s.fixed, s.scope = true, read.Issues
	}

	return nil
}

// readClosing reads the closing issues of the pull request of the item, sorted and without a repeat.
func (s *script) readClosing(ctx context.Context) (scopeRead, error) {
	issues, found, err := s.e.deps.Forge.ClosingIssues(ctx, s.in.Project, s.in.Item)
	if err != nil {
		return scopeRead{}, fmt.Errorf("read the issues that the pull request of item %s closes: %w", s.in.Item, err)
	}

	return scopeRead{Found: found, Issues: sortedIssues(issues)}, nil
}

// sortedIssues returns the numbers in order, without a repeat. The result is never nil, so a recorded empty set stays a set.
func sortedIssues(numbers []int) []int {
	out := append([]int{}, numbers...)
	slices.Sort(out)

	return slices.Compact(out)
}

// holdScope runs before a merge. It reads the closing issues again and refuses an issue that is new since the first review and has no owner line.
// The read is a recorded step. A read that fails, or a pull request that is gone, refuses. A run that has fixed no scope has nothing to compare.
// A new issue makes the run ask the owner. The answer is the owner line: it must name each new issue.
func (s *script) holdScope(label string) error {
	fresh, err := runStep(s.e, s.ctx, s.name(scopeCheckStep, label), s.readFresh)
	if err != nil {
		return fmt.Errorf("%s of item %s: %w", scopeCheckStep, s.in.Item, err)
	}

	if len(fresh) == 0 {
		return nil
	}

	return s.askScope(fresh)
}

// readFresh reads the closing issues and returns the ones that are new since the first review and have no owner line.
// A run with no scope has nothing to compare, so it reads nothing.
func (s *script) readFresh(ctx context.Context) ([]int, error) {
	if !s.fixed {
		return []int{}, nil
	}

	read, err := s.readClosing(ctx)
	if err != nil {
		return nil, err
	}

	if !read.Found {
		return nil, fmt.Errorf("read the issues that the pull request of item %s closes: the item has no pull request", s.in.Item)
	}

	return s.newIssues(read.Issues), nil
}

// newIssues lists the issues that are not in the scope of the first review and have no owner line.
func (s *script) newIssues(closing []int) []int {
	fresh := []int{}

	for _, number := range closing {
		if !slices.Contains(s.scope, number) && !slices.Contains(s.owned, number) {
			fresh = append(fresh, number)
		}
	}

	return fresh
}

// askScope asks the owner about the new issues. The answer is the owner line: it names every issue to keep.
// An issue that the answer does not name refuses the merge, and the error says how to add the line.
func (s *script) askScope(fresh []int) error {
	question := fmt.Sprintf("The pull request of item %s closes %s now, and did not at its first review (then: %s). Answer in the form \"keep #N[, #N ...]\", for example \"keep %s\". Any other answer stops the merge.",
		s.in.Item, issueList(fresh), issueList(s.scope), strings.Join(strings.Fields(issueList(fresh)), ", "))

	answer, err := s.e.waitOwner(s.ctx, s.in.Item, question)
	if err != nil {
		return err
	}

	named, _ := keptIssues(answer)
	missing := slices.DeleteFunc(slices.Clone(fresh), func(number int) bool { return slices.Contains(named, number) })
	if len(missing) > 0 {
		return fmt.Errorf("%w: item %s: %s has no owner line. The accepted answer is \"keep #N[, #N ...]\", for example obee answer %s \"keep %s\". Or remove the closing keyword",
			errScopeRefused, s.in.Item, issueList(missing), s.in.Item, strings.Join(strings.Fields(issueList(missing)), ", "))
	}

	s.owned = append(s.owned, fresh...)

	return nil
}

// keptIssues returns the issue numbers of an owner line in the form "keep #N[, #N ...]". Any other text, or a number too large for int, gives false.
func keptIssues(text string) ([]int, bool) {
	if !keepForm.MatchString(text) {
		return nil, false
	}

	kept := []int{}

	for _, digits := range issueNumber.FindAllString(text, -1) {
		number, err := strconv.Atoi(digits)
		if err != nil {
			return nil, false
		}

		kept = append(kept, number)
	}

	return kept, true
}

// issueList shows issue numbers as "#12 #13", or "no issue" for none.
func issueList(numbers []int) string {
	if len(numbers) == 0 {
		return "no issue"
	}

	parts := make([]string, len(numbers))
	for i, number := range numbers {
		parts[i] = "#" + strconv.Itoa(number)
	}

	return strings.Join(parts, " ")
}
