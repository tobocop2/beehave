package flow

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"go.starlark.net/starlark"

	"github.com/tobocop2/obee/internal/workflow"
)

// ofKeyword is the keyword of item_read that names the item to read: the item of the run or its parent.
const ofKeyword = "of"

var errNoReader = &absentError{
	message: "this tracker cannot read the comments or the parent of an item. Use a tracker that has a reader (the local store, beads or GitHub Issues), or remove the item_read call",
	step:    "read the comments and the parent of an item",
}

// itemRead reads the fields, the comments and the parent of the item of the run, or of its parent when of = "parent".
// The read is a recorded step, so a replay reads the answer of the first start and not the tracker again. The fields are the ones that the tracker declares for an item.
// An item with no parent gives found = False and empty values when of = "parent".
func (s *script) itemRead(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	of, label := "self", ""

	if err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{ofKeyword: &of, nameKeyword: &label}); err != nil {
		return nil, err
	}

	if !slices.Contains(workflow.ItemTargets(), of) {
		return nil, fmt.Errorf("`%s` is %q. Use one of: %s", ofKeyword, of, strings.Join(workflow.ItemTargets(), ", "))
	}

	if s.e.deps.Reader == nil {
		return nil, errNoReader
	}

	return s.durable("item_read", label, func(ctx context.Context, _ string) (stepResult, error) {
		outputs, err := s.readItem(ctx, of)

		return stepResult{Outputs: outputs}, err
	})
}

// readItem reads the item that of names. The item of the run is read through the same path as the fields of main.
func (s *script) readItem(ctx context.Context, of string) (map[string]any, error) {
	id := s.in.Item

	if of == "parent" {
		parent, err := s.e.deps.Reader.Parent(ctx, s.in.Project, id)
		if err != nil || parent == "" {
			return readOutputs(false, "", nil, nil, ""), err
		}

		id = parent
	}

	return s.readFound(ctx, id)
}

// readFound reads the fields, the comments and the parent of the item id.
func (s *script) readFound(ctx context.Context, id string) (map[string]any, error) {
	fields, err := s.e.deps.Items.Fields(ctx, s.in.Project, id)
	if err != nil {
		return nil, fmt.Errorf("read the fields of item %s: %w", id, err)
	}

	comments, err := s.e.deps.Reader.Comments(ctx, s.in.Project, id)
	if err != nil {
		return nil, fmt.Errorf("read the comments of item %s: %w", id, err)
	}

	above, err := s.e.deps.Reader.Parent(ctx, s.in.Project, id)
	if err != nil {
		return nil, fmt.Errorf("read the parent of item %s: %w", id, err)
	}

	return readOutputs(true, id, fields, comments, above), nil
}

// readOutputs makes the outputs of item_read. A list or a dict that is nil becomes an empty one, so a workflow file never meets None.
func readOutputs(found bool, id string, fields map[string]string, comments []string, parent string) map[string]any {
	if fields == nil {
		fields = map[string]string{}
	}

	if comments == nil {
		comments = []string{}
	}

	return map[string]any{"found": found, "id": id, "fields": fields, "comments": comments, "parent": parent}
}
