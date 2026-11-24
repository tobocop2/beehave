package merge

import (
	"fmt"
	"slices"
	"strings"
)

// closing refuses a pull request whose closing references are not the issues that the request names. An extra issue would be closed by mistake,
// and a missing one stays open. The forge lists the issues that the pull request closes: GitHub's own list for a pull request into the default branch,
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

	return a.commitKeywords()
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
