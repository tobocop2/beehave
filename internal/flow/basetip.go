package flow

import (
	"context"
	"errors"
	"fmt"

	"go.starlark.net/starlark"

	"github.com/tobocop2/beehest/internal/workflow"
)

// baseTipKind is the step kind and the builtin name of base_tip.
const baseTipKind = "base_tip"

var (
	errNoBaseTipReader = &absentError{message: "this build cannot read the tip of a base. Use a build that sets a batch host", step: "read the tip of a base"}
	errEmptyTip        = errors.New("the forge gave no commit")
)

// baseTip reads the tip commit of the base on the forge and the tree of that commit, after it fetches the branches of the remote into the worktree of the item.
// The read is a recorded step, so a replay returns the first read. A read that fails ends the step, and a tip with no commit is a failed read.
func (s *script) baseTip(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	var base, label string

	if err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{baseKeyword: &base, nameKeyword: &label}); err != nil {
		return nil, err
	}

	if s.e.deps.Batch == nil {
		return nil, errNoBaseTipReader
	}

	return s.durable(baseTipKind, label, func(ctx context.Context, _ string) (stepResult, error) { return s.readBaseTip(ctx, base) })
}

// readBaseTip makes the worktree of the item when it has none, then reads the tip of the base into it. A tip with no commit is a failed read.
// The git commands of the batch library run in that worktree, so the library reads the tip before its first git command.
func (s *script) readBaseTip(ctx context.Context, base string) (stepResult, error) {
	if _, err := s.tree(ctx); err != nil {
		return stepResult{}, fmt.Errorf("find the folder of item %s: %w", s.in.Item, err)
	}

	tip, err := s.e.deps.Batch.Tip(ctx, s.in.Project, s.in.Item, base)
	if err == nil && tip.Commit == "" {
		err = errEmptyTip
	}

	if err != nil {
		return stepResult{}, fmt.Errorf("read the tip of %s: %w", base, err)
	}

	return stepResult{Outputs: baseTipOutputs(tip.Commit, tip.Tree)}, nil
}
