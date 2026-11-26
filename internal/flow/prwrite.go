package flow

import (
	"context"
	"errors"
	"fmt"

	"go.starlark.net/starlark"

	"github.com/tobocop2/beehave/internal/workflow"
)

// prComment adds a comment to the pull request of the item. The key makes the call safe to repeat: the same key on the same pull request adds one comment.
// The forge reads the comments of the pull request before it posts, so a crash between the post and the step record does not post twice.
func (s *script) prComment(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	text, key, label, pr, err := commentArguments(b, args, kwargs)
	if err != nil {
		return nil, err
	}

	return s.durable("pr_comment", label, func(ctx context.Context, _ string) (stepResult, error) {
		number, err := s.writes().PRComment(ctx, s.in.Project, s.in.Item, pr, s.scoped(key), text)

		return stepResult{Outputs: prCommentOutputs(number)}, err
	})
}

// commentArguments unpacks the arguments of a pr_comment call and refuses a text that is empty or matches forbid_text, and a pull request number below 0.
func commentArguments(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (text, key, label string, pr int, err error) {
	rules, err := unpackPosting(b, args, kwargs, map[string]any{
		textKeyword: &text, keyKeyword: &key, nameKeyword: &label, prKeyword: &pr,
	})
	if err != nil {
		return "", "", "", 0, err
	}

	err = errors.Join(workflow.NotEmpty(textKeyword, text), workflow.NotEmpty(keyKeyword, key), rules.refuse(postedText{textKeyword, text}))
	if err == nil && pr < 0 {
		err = fmt.Errorf("pr_comment: pr is %d. Give the number of a pull request, or leave pr out to comment on the pull request of the item", pr)
	}

	return text, key, label, pr, err
}

// prOpen opens the draft pull request of the item once, sets its body and links it into the native stack of its parent. The forge finds an open pull request
// on the branch of the item first, so a resume after a lost record opens no second one. The body is written as given, and a pull request that is open is not written.
func (s *script) prOpen(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	var req OpenRequest

	label, rules, err := unpackOpen(b, args, kwargs, &req)
	if err != nil {
		return nil, err
	}

	if err = errors.Join(req.check(), rules.refuse(postedText{"title", req.Title}, postedText{"body", req.Body})); err != nil {
		return nil, err
	}

	return s.durable("pr_open", label, func(ctx context.Context, _ string) (stepResult, error) {
		opened, err := s.writes().Open(ctx, s.in.Project, s.in.Item, req)

		return stepResult{Outputs: openOutputs(opened)}, err
	})
}

// unpackOpen unpacks the arguments of a pr_open call into req. It returns the label of the call and the forbid_text patterns.
func unpackOpen(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple, req *OpenRequest) (label string, rules textRules, err error) {
	rules, err = unpackPosting(b, args, kwargs, map[string]any{
		titleKeyword: &req.Title, baseKeyword: &req.Base, bodyKeyword: &req.Body, parentKeyword: &req.Parent, nameKeyword: &label,
	})

	return label, rules, err
}

// check refuses a request with no title or no base.
func (r OpenRequest) check() error {
	return errors.Join(workflow.NotEmpty("title", r.Title), workflow.NotEmpty(baseKeyword, r.Base))
}
