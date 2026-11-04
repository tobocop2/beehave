package flow

import (
	"context"
	"errors"

	"go.starlark.net/starlark"

	trackerstore "github.com/tobocop2/obee/internal/store"
	"github.com/tobocop2/obee/internal/workflow"
)

// writer changes the items of the tracker of a project. Each method is the method of store.Writer with the project in front.
// The tracker adapter must make Create and Comment safe to repeat for one key. A step runs at least once, so a crash between the write and the step record runs the call again.
type writer interface {
	Create(ctx context.Context, project, key string, item trackerstore.NewItem) (id string, created bool, err error)
	SetStatus(ctx context.Context, project, id string, status trackerstore.Status) error
	Comment(ctx context.Context, project, id, key, text string) error
	// Assign sets the assignee of an item. The same assignee again changes nothing, so a step that runs twice after a crash is safe.
	Assign(ctx context.Context, project, id, assignee string) error
}

// The keywords that the item and pull request writes share.
const (
	assigneeKeyword = "assignee"
	textKeyword     = "text"
	keyKeyword      = "key"
	parentKeyword   = "parent"
)

var errNoWriter = errors.New("this build cannot write to a tracker. Use a build that sets a tracker writer")

// scoped joins the key of a file to the run id, so two runs never share a key. The run id is the id of the DBOS workflow, which a replay keeps.
func (s *script) scoped(key string) string { return workflowID(s.ctx) + ":" + key }

// check returns the first problem of a write call before the step starts: an empty argument, a status that is not known, or a build with no writer.
func (s *script) check(problems ...error) error {
	if s.e.deps.Writer == nil {
		problems = append(problems, errNoWriter)
	}

	return errors.Join(problems...)
}

// write records one call of a write builtin as a step. fn returns the outputs of the call.
func (s *script) write(builtin, label string, fn func(ctx context.Context) (map[string]any, error)) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	return s.durable(builtin, label, func(ctx context.Context, _ string) (stepResult, error) {
		outputs, err := fn(ctx)

		return stepResult{Outputs: outputs}, err
	})
}

// itemCreate files an item in the tracker of the project. The key makes the call safe to repeat: the same key gives the same item and created = False.
func (s *script) itemCreate(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	var title, key, body, parent, assignee, label string

	if err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{
		titleKeyword: &title, keyKeyword: &key, bodyKeyword: &body, parentKeyword: &parent, assigneeKeyword: &assignee, nameKeyword: &label,
	}); err != nil {
		return nil, err
	}

	if err := s.check(workflow.NotEmpty("title", title), workflow.NotEmpty(keyKeyword, key)); err != nil {
		return nil, err
	}

	return s.write("item_create", label, func(ctx context.Context) (map[string]any, error) {
		id, created, err := s.e.deps.Writer.Create(ctx, s.in.Project, s.scoped(key), trackerstore.NewItem{Title: title, Body: body, Parent: parent, Assignee: assignee})

		return createOutputs(id, created), err
	})
}

// itemStatus sets the status of an item to open, closed or hold. The same status again changes nothing.
func (s *script) itemStatus(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	var id, text, label string

	if err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{
		"id": &id, statusKeyword: &text, nameKeyword: &label,
	}); err != nil {
		return nil, err
	}

	status, parseErr := trackerstore.ParseStatus(text)
	if err := s.check(workflow.NotEmpty("id", id), parseErr); err != nil {
		return nil, err
	}

	return s.write("item_status", label, func(ctx context.Context) (map[string]any, error) {
		return statusOutputs(id, string(status)), s.e.deps.Writer.SetStatus(ctx, s.in.Project, id, status)
	})
}

// itemAssign sets the assignee of an item. The same assignee again changes nothing.
func (s *script) itemAssign(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	var id, assignee, label string

	if err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{
		"id": &id, assigneeKeyword: &assignee, nameKeyword: &label,
	}); err != nil {
		return nil, err
	}

	if err := s.check(workflow.NotEmpty("id", id), workflow.NotEmpty(assigneeKeyword, assignee)); err != nil {
		return nil, err
	}

	return s.write("item_assign", label, func(ctx context.Context) (map[string]any, error) {
		return commentOutputs(id), s.e.deps.Writer.Assign(ctx, s.in.Project, id, assignee)
	})
}

// itemComment adds a comment to an item. The key makes the call safe to repeat: the same key on the same item adds one comment.
func (s *script) itemComment(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	var id, text, key, label string

	if err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{
		"id": &id, textKeyword: &text, keyKeyword: &key, nameKeyword: &label,
	}); err != nil {
		return nil, err
	}

	if err := s.check(workflow.NotEmpty("id", id), workflow.NotEmpty(textKeyword, text), workflow.NotEmpty(keyKeyword, key)); err != nil {
		return nil, err
	}

	return s.write("item_comment", label, func(ctx context.Context) (map[string]any, error) {
		return commentOutputs(id), s.e.deps.Writer.Comment(ctx, s.in.Project, id, s.scoped(key), text)
	})
}
