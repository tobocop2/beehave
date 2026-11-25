package flow

import (
	"context"
	"fmt"
	"slices"

	"go.starlark.net/starlark"

	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/workflow"
)

// prHistoryKind is the step kind and the builtin name of pr_history, and pullsField is the field of a result that lists pull requests.
const (
	prHistoryKind = "pr_history"
	pullsField    = "pulls"
	prHistoryItem = "item"
)

// prHistory reads the pull requests of the item of the run, or of the item that the keyword item names, in every state, with the closing comment of each closed or merged one.
// The read is a recorded step, so a replay returns the list of the first read. A failed read ends the run: no later step works from a guess.
func (s *script) prHistory(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	var label, of string

	if err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{nameKeyword: &label, prHistoryItem: &of}); err != nil {
		return nil, err
	}

	if of == "" {
		of = s.in.Item
	}

	return s.durable(prHistoryKind, label, func(ctx context.Context, _ string) (stepResult, error) {
		pulls, err := s.e.deps.Forge.History(ctx, s.in.Project, of)
		if err != nil {
			return stepResult{}, fmt.Errorf("read the pull requests of item %s: %w", of, err)
		}

		foreign := slices.ContainsFunc(pulls, func(p model.PriorPull) bool { return p.Foreign || p.ClosingForeign })

		return stepResult{Outputs: prHistoryOutputs(pulls, s.e.withholdForeign(ctx, s.in, foreign))}, nil
	})
}
