package flow

import (
	"cmp"
	"context"
	"errors"

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

// prClosing reads the issues that merging the pull request of the item closes, in one recorded step. The item is the item of the run when the call names none.
// A replay returns the first read. A read that fails ends the step. A project with no forge gives forge false and no issues, and an item with no pull request gives found false.
func (s *script) prClosing(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	var item, label string

	if err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{itemKeyword: &item, nameKeyword: &label}); err != nil {
		return nil, err
	}

	if s.e.deps.Forge == nil {
		return nil, errNoClosingReader
	}

	item = cmp.Or(item, s.in.Item)

	return s.durable(prClosingKind, label, func(ctx context.Context, _ string) (stepResult, error) {
		read, err := s.readClosing(ctx, item)
		if errors.Is(err, forgepkg.ErrNoForge) {
			return stepResult{Outputs: prClosingOutputs(closingRead{})}, nil
		}

		return stepResult{Outputs: prClosingOutputs(closingRead{Found: read.Found, Issues: read.Issues, Forge: true})}, err
	})
}
