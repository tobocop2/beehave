package merge

import (
	"fmt"
	"slices"
	"strings"

	"github.com/tobocop2/beehave/internal/model"
)

// closing refuses a pull request whose closing references are not the issues that the request names. An extra issue would be closed by mistake,
// and a missing one stays open. The forge lists the issues that the pull request closes: its own list for a pull request into the default branch,
// and the keywords of the body and the commit messages for a pull request into any other base. The guard reads the commit messages too (see commitKeywords).
func (a *attempt) closing() *refusal {
	refs, err := a.forge.ClosingRefs(a.ctx, a.req.PR)
	if err != nil {
		return transient(fmt.Sprintf("REFUSE: cannot read the issues that #%d closes", a.req.PR))
	}

	if refs.BaseBranch == "" || refs.DefaultBranch == "" {
		return refuse(fmt.Sprintf("REFUSE: cannot read the base branch and the default branch of #%d, so its closing references cannot be audited.", a.req.PR))
	}

	if stop := a.sameIssues(refs.Issues); stop != nil {
		return stop
	}

	if stop := a.samePinned(refs); stop != nil {
		return stop
	}

	return a.commitKeywords()
}

// samePinned refuses when the member pins the closing issues that a check read, and the forge lists others now. A member that pins no set passes.
func (a *attempt) samePinned(refs model.ClosingRefs) *refusal {
	if a.req.Pinned == nil {
		return nil
	}

	now := make([]model.IssueRef, 0, len(refs.Issues)+len(refs.Elsewhere))
	for _, number := range refs.Issues {
		now = append(now, model.OwnIssue(number))
	}

	now = sortedIssues(append(now, refs.Elsewhere...))

	if pinned := sortedIssues(a.req.Pinned); !slices.Equal(now, pinned) {
		return refuse(fmt.Sprintf("REFUSE: #%d closes %s now, and closed %s when the scope check read it. The owner decides: run the scope check again.", a.req.PR, issueRefs(now), issueRefs(pinned)))
	}

	return nil
}

// sortedIssues returns the references in order, without a repeat.
func sortedIssues(issues []model.IssueRef) []model.IssueRef {
	out := slices.Clone(issues)
	slices.SortFunc(out, model.CompareIssues)

	return slices.Compact(out)
}

// issueRefs shows references as "#12 other-org/other-repo#5", or "no issue" for none.
func issueRefs(issues []model.IssueRef) string {
	if len(issues) == 0 {
		return "no issue"
	}

	parts := make([]string, len(issues))
	for i, issue := range issues {
		parts[i] = string(issue)
	}

	return strings.Join(parts, " ")
}

// sameIssues refuses when the issues that the forge lists are not the issues that the request names.
func (a *attempt) sameIssues(listed []int) *refusal {
	got, want := sortedUnique(listed), sortedUnique(a.req.Names)
	if slices.Equal(got, want) {
		return nil
	}

	return refuse(fmt.Sprintf("REFUSE: #%d closes %s and the closes list of the member holds %s. The owner decides: fix the closing keywords or the closes list.", a.req.PR, issueList(got), issueList(want)))
}

func sortedUnique(numbers []int) []int {
	out := slices.Clone(numbers)
	slices.Sort(out)

	return slices.Compact(out)
}

// issueList shows issue numbers as "#12 #13", or "no issue" for none.
func issueList(numbers []int) string {
	if len(numbers) == 0 {
		return "no issue"
	}

	parts := make([]string, len(numbers))
	for i, n := range numbers {
		parts[i] = fmt.Sprintf("#%d", n)
	}

	return strings.Join(parts, " ")
}
