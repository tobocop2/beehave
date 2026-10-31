package batch

import (
	"context"
	"fmt"
	"slices"
	"strconv"

	"github.com/tobocop2/obee/internal/model"
)

// IssueReads are the two forge reads that the check after a batch needs.
type IssueReads interface {
	IssueState(ctx context.Context, number int) (string, error)
	ClosingRefs(ctx context.Context, pr int) (model.ClosingRefs, error)
}

// Report is what the check after a batch found. Findings holds one line for each problem. Note says why no read ran, or is empty.
// The check only reads. It never closes or reopens an issue, and the batch has already landed, so a finding is for the owner to act on.
type Report struct {
	Findings []string `json:"findings"`
	Note     string   `json:"note"`
}

// Audit reads the issue of each landed item and, when the integration pull request is known, its closing references.
// The id of an item of the GitHub tracker is the number of its issue. A landed item whose issue is still open is a finding.
// An issue that the integration pull request closes and no landed item names is a finding. A read that fails is a finding, never a pass.
func Audit(ctx context.Context, reads IssueReads, landed []string, integration int) Report {
	findings, claimed := landedFindings(ctx, reads, landed)

	if integration > 0 {
		findings = append(findings, unclaimed(ctx, reads, integration, claimed)...)
	}

	return Report{Findings: findings}
}

// landedFindings reads the issue of each landed item. It returns the findings and the issue numbers that the items name.
func landedFindings(ctx context.Context, reads IssueReads, landed []string) ([]string, map[int]bool) {
	findings := []string{}
	claimed := map[int]bool{}

	for _, item := range landed {
		number, err := strconv.Atoi(item)
		if err != nil || number <= 0 || strconv.Itoa(number) != item {
			findings = append(findings, fmt.Sprintf("the id %q of a landed item is not a plain issue number, so its issue was not read", item))

			continue
		}

		claimed[number] = true

		findings = append(findings, openFinding(ctx, reads, item, number)...)
	}

	return findings, claimed
}

// openFinding returns the finding for the issue of one item, or none when the issue is closed.
func openFinding(ctx context.Context, reads IssueReads, item string, number int) []string {
	state, err := reads.IssueState(ctx, number)

	switch {
	case err != nil:
		return []string{fmt.Sprintf("cannot read issue #%d: %v", number, err)}
	case state != "CLOSED":
		return []string{fmt.Sprintf("issue #%d of item %s is not closed after the batch landed (the forge says %q)", number, item, state)}
	}

	return nil
}

// unclaimed returns a finding for each issue that the integration pull request closes and no landed item names.
func unclaimed(ctx context.Context, reads IssueReads, integration int, claimed map[int]bool) []string {
	refs, err := reads.ClosingRefs(ctx, integration)
	if err != nil {
		return []string{fmt.Sprintf("cannot read the closing references of pull request #%d: %v", integration, err)}
	}

	extra := []string{}

	for _, number := range slices.Sorted(slices.Values(refs.Issues)) {
		if !claimed[number] {
			extra = append(extra, fmt.Sprintf("pull request #%d closes issue #%d and no landed item names it", integration, number))
		}
	}

	return extra
}
