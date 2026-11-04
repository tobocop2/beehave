package flow

import (
	"context"
	"errors"
	"fmt"

	"go.starlark.net/starlark"

	"github.com/tobocop2/obee/internal/workflow"
)

// prComment adds a comment to the pull request of the item. The key makes the call safe to repeat: the same key on the same pull request adds one comment.
// The forge reads the comments of the pull request before it posts, so a crash between the post and the step record does not post twice.
func (s *script) prComment(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	var text, key, label string

	if err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{
		textKeyword: &text, keyKeyword: &key, nameKeyword: &label,
	}); err != nil {
		return nil, err
	}

	if err := errors.Join(workflow.NotEmpty(textKeyword, text), workflow.NotEmpty(keyKeyword, key)); err != nil {
		return nil, err
	}

	return s.durable("pr_comment", label, func(ctx context.Context, _ string) (stepResult, error) {
		number, err := s.e.deps.Forge.Comment(ctx, s.in.Project, s.in.Item, s.scoped(key), text)

		return stepResult{Outputs: prCommentOutputs(number)}, err
	})
}

// prOpen opens the draft pull request of the item once, sets its body and links it into the native stack of its parent. The forge finds an open pull request
// on the branch of the item first, so a resume after a lost record opens no second one. The body is the text plus a closing line that the forge picks from the base.
func (s *script) prOpen(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	var (
		req   OpenRequest
		label string
	)

	if err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{
		titleKeyword: &req.Title, baseKeyword: &req.Base, bodyKeyword: &req.Body, "issue": &req.Issue, parentKeyword: &req.Parent, nameKeyword: &label,
	}); err != nil {
		return nil, err
	}

	if err := req.check(); err != nil {
		return nil, err
	}

	return s.durable("pr_open", label, func(ctx context.Context, _ string) (stepResult, error) {
		opened, err := s.e.deps.Forge.Open(ctx, s.in.Project, s.in.Item, req)

		return stepResult{Outputs: openOutputs(opened)}, err
	})
}

// boardSync puts the card of the item on a project board, sets its status column, and reads the card back. A read-back that shows another status fails the step,
// so a board that lost the write, or a column that the board lacks, stops the run instead of passing as done.
func (s *script) boardSync(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	var (
		req   CardRequest
		label string
	)

	if err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{
		"board": &req.Board, "owner": &req.Owner, statusKeyword: &req.Status, "issue": &req.Issue, nameKeyword: &label,
	}); err != nil {
		return nil, err
	}

	if err := req.check(); err != nil {
		return nil, err
	}

	return s.durable("board_sync", label, func(ctx context.Context, _ string) (stepResult, error) {
		return s.syncCard(ctx, req)
	})
}

// syncCard places the card, reads it back and compares. A failed write ends the step before the read.
func (s *script) syncCard(ctx context.Context, req CardRequest) (stepResult, error) {
	number, err := s.e.deps.Forge.PlaceCard(ctx, s.in.Project, s.in.Item, req)
	if err != nil {
		return stepResult{}, err
	}

	shown, err := s.e.deps.Forge.ReadCard(ctx, s.in.Project, s.in.Item, req)
	if err != nil {
		return stepResult{}, err
	}

	return stepResult{Outputs: boardOutputs(number, shown)}, req.confirm(shown)
}

// check refuses a request with no board, no status or a negative issue number.
func (r CardRequest) check() error {
	if r.Board <= 0 {
		return errors.New("board must be the number of a project board")
	}

	if r.Issue < 0 {
		return errors.New("issue must be an issue number, or 0 for the pull request of the item")
	}

	return workflow.NotEmpty("status", r.Status)
}

// confirm fails when the status that the board shows is not the one that was set.
func (r CardRequest) confirm(shown string) error {
	switch shown {
	case r.Status:
		return nil
	case "":
		return fmt.Errorf("the board %d shows the card with no status after the write; wanted %q", r.Board, r.Status)
	}

	return fmt.Errorf("the board %d shows %q for the card after the write; wanted %q", r.Board, shown, r.Status)
}

// check refuses a request with no title, no base or a negative issue number.
func (r OpenRequest) check() error {
	if r.Issue < 0 {
		return errors.New("issue must be an issue number, or 0 for none")
	}

	return errors.Join(workflow.NotEmpty("title", r.Title), workflow.NotEmpty(baseKeyword, r.Base))
}

// prAssign assigns the pull request of the item to a login. With linked = True it also assigns each issue that merging the pull request closes.
// The forge keeps one entry for a login, so the same call twice changes nothing.
func (s *script) prAssign(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	var (
		assignee, label string
		linked          bool
	)

	if err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{
		assigneeKeyword: &assignee, "linked": &linked, nameKeyword: &label,
	}); err != nil {
		return nil, err
	}

	if err := workflow.NotEmpty(assigneeKeyword, assignee); err != nil {
		return nil, err
	}

	return s.durable("pr_assign", label, func(ctx context.Context, _ string) (stepResult, error) {
		number, issues, err := s.e.deps.Forge.Assign(ctx, s.in.Project, s.in.Item, assignee, linked)

		return stepResult{Outputs: prOutputs(number, issues)}, err
	})
}
