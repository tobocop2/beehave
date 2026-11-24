package flow

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"

	"go.starlark.net/starlark"

	forgepkg "github.com/tobocop2/beehave/internal/forge"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/workflow"
)

// prClosingKind is the step kind and the builtin name of pr_closing, itemKeyword is the keyword that names the item whose pull request it reads,
// and foundField is the result field that tells whether the item has a pull request.
const (
	prClosingKind = "pr_closing"
	itemKeyword   = "item"
	foundField    = "found"
)

var errNoClosingReader = &absentError{message: "this build cannot read the issues that a pull request closes. Use a build that sets a forge", step: "read the issues that a pull request closes"}

// closingRead is what pr_closing records and what earlier_reads gives back for it: whether the item has a pull request, the issues that merging it closes, and whether the project has a forge.
type closingRead struct {
	Found  bool             `json:"found"`
	Issues []model.IssueRef `json:"issues"`
	Forge  bool             `json:"forge"`
}

// prClosingOutputs are the fields of pr_closing. The issues are never nil: an item with no pull request, and a project with no forge, close none.
func prClosingOutputs(read closingRead) map[string]any {
	return map[string]any{foundField: read.Found, "issues": append([]model.IssueRef{}, read.Issues...), "forge": read.Forge}
}

// prClosing reads the issues that merging a pull request closes, in one recorded step. The pull request is the one with the number pr, or else the pull request of the item.
// The item is the item of the run when the call names none. A call with both item and pr is refused.
// A replay returns the first read. A read that fails ends the step. A project with no forge gives forge false and no issues, and an item with no pull request gives found false.
// A pull request that is named by its number is found when the read succeeds: a pull request that is gone is a failed read.
func (s *script) prClosing(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	var (
		item, label string
		pr          int
	)

	if err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{itemKeyword: &item, prKeyword: &pr, nameKeyword: &label}); err != nil {
		return nil, err
	}

	reader, err := s.closingReader(item, pr)
	if err != nil {
		return nil, err
	}

	return s.durable(prClosingKind, label, func(ctx context.Context, _ string) (stepResult, error) { return closingStep(ctx, reader) })
}

// closingStep runs the read. A project with no forge records forge false and no issues.
func closingStep(ctx context.Context, reader func(context.Context) (closingRead, error)) (stepResult, error) {
	read, err := reader(ctx)
	if errors.Is(err, forgepkg.ErrNoForge) {
		return stepResult{Outputs: prClosingOutputs(closingRead{})}, nil
	}

	return stepResult{Outputs: prClosingOutputs(closingRead{Found: read.Found, Issues: read.Issues, Forge: true})}, err
}

// closingReader checks the item and the number of a pr_closing call and returns the read to run: the pull request of the item, or the pull request with the number.
// Both named, a number below zero, and a build with no reader for the read are refused before any read.
func (s *script) closingReader(item string, pr int) (func(context.Context) (closingRead, error), error) {
	switch {
	case item != "" && pr != 0:
		return nil, fmt.Errorf("pass `item` or `pr`, not both: item is %q and pr is %d", item, pr)
	case pr < 0:
		return nil, fmt.Errorf("`pr` is %d. Pass the number of a pull request", pr)
	case pr > 0 && s.e.deps.Queue == nil, pr == 0 && s.e.deps.Forge == nil:
		return nil, errNoClosingReader
	case pr > 0:
		return func(ctx context.Context) (closingRead, error) { return s.readClosingOfPull(ctx, pr) }, nil
	}

	item = cmp.Or(item, s.in.Item)

	return func(ctx context.Context) (closingRead, error) { return s.readClosing(ctx, item) }, nil
}

// readClosing reads the closing issues of the pull request of the item, sorted and without a repeat.
func (s *script) readClosing(ctx context.Context, item string) (closingRead, error) {
	issues, found, err := s.e.deps.Forge.ClosingIssues(ctx, s.in.Project, item)
	if err != nil {
		return closingRead{}, fmt.Errorf("read the issues that the pull request of item %s closes: %w", item, err)
	}

	return closingRead{Found: found, Issues: sortedIssues(issues)}, nil
}

// readClosingOfPull reads the closing issues of the pull request with the number, sorted and without a repeat.
func (s *script) readClosingOfPull(ctx context.Context, pr int) (closingRead, error) {
	issues, err := s.e.deps.Queue.PullClosing(ctx, s.in.Project, pr)
	if err != nil {
		return closingRead{}, fmt.Errorf("read the issues that pull request %d closes: %w", pr, err)
	}

	return closingRead{Found: true, Issues: sortedIssues(issues)}, nil
}

// sortedIssues returns the issues in order, without a repeat. The result is never nil, so a recorded empty set stays a set.
func sortedIssues(issues []model.IssueRef) []model.IssueRef {
	out := append([]model.IssueRef{}, issues...)
	slices.SortFunc(out, model.CompareIssues)

	return slices.Compact(out)
}
