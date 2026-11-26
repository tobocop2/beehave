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
	call, err := commentArguments(b, args, kwargs)
	if err != nil {
		return nil, err
	}

	return s.durableWith("pr_comment", call.label, map[string]any{errorField: ""}, func(ctx context.Context, _ string) (stepResult, error) {
		number, err := s.writes().PRComment(ctx, s.in.Project, s.in.Item, call.pr, s.scoped(call.key), call.text)
		if err != nil && call.failure == string(workflow.FailureReturn) {
			return stepResult{Outputs: prCommentOutputs(0, s.e.redact(err.Error()))}, nil
		}

		return stepResult{Outputs: prCommentOutputs(number, "")}, err
	})
}

// commentCall is a pr_comment call after its arguments passed the checks. failure is a mode of workflow.FailureModes.
type commentCall struct {
	text, key, label, failure string
	pr                        int
}

// commentArguments unpacks the arguments of a pr_comment call and refuses a text that is empty or matches forbid_text, a pull request number below 0 and an unknown failure mode.
func commentArguments(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (commentCall, error) {
	call := commentCall{failure: string(workflow.FailureRaise)}

	rules, err := unpackPosting(b, args, kwargs, map[string]any{
		textKeyword: &call.text, keyKeyword: &call.key, nameKeyword: &call.label, prKeyword: &call.pr, failureKeyword: &call.failure,
	})
	if err != nil {
		return commentCall{}, err
	}

	err = errors.Join(workflow.NotEmpty(textKeyword, call.text), workflow.NotEmpty(keyKeyword, call.key), rules.refuse(postedText{textKeyword, call.text}))

	switch {
	case err != nil:
	case call.pr < 0:
		err = fmt.Errorf("pr_comment: pr is %d. Give the number of a pull request, or leave pr out to comment on the pull request of the item", call.pr)
	}

	return call, err
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
