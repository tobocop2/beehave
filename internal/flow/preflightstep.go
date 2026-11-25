package flow

import (
	"context"
	"errors"
	"slices"
	"strings"

	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/preflight"
	"github.com/tobocop2/beehave/internal/workflow"
)

// preflighter judges the pull request of an item before an agent works on it. The caller finds the pull request: Request.PR is zero.
// An item with no pull request yet has nothing that can have changed, so it gives Unchanged.
// ReadPull finds the pull request of the item the same way and returns its facts. Facts with number 0 mean that the item has no pull request in any state.
// With author true, the facts and the result of Preflight carry the association of the author of the pull request that they read, from that one lookup.
// With author false, the reader makes no read of the author, and a caller must not judge one.
// IssueAssignees returns the logins that hold an issue. An empty repo is the repository of the project. A failed read is an error, never an empty list.
type preflighter interface {
	Preflight(ctx context.Context, project, item string, req preflight.Request, author bool) (preflight.Result, error)
	ReadPull(ctx context.Context, project, item string, author bool) (model.PullFacts, error)
	IssueAssignees(ctx context.Context, project, repo string, number int) ([]string, error)
	// IssueAuthor reads who wrote the issue of the item, when the tracker of the project keeps its items as issues on the forge, whatever the forge of the project.
	// Any other item has none. A failed read and an unknown author are errors.
	IssueAuthor(ctx context.Context, project, item string) (model.Authored, error)
}

var errNoPreflight = errors.New("this build cannot read the forge before a step. Use a build that sets a preflight reader")

// preflightOutputs are the fields of a preflight call. A stop is a result, not an error: ok is false, and code and stop say why.
// The reason is empty when ok is true.
func preflightOutputs(result preflight.Result) map[string]any {
	reason := ""
	if !result.OK() {
		reason = strings.Join(result.Lines, "; ")
	}

	return map[string]any{
		"ok": result.OK(), "stop": result.Stop.String(), codeField: result.Stop.Code(), reasonKey: reason,
	}
}

// preflight reads the pull request of the item and returns the first stop in the fixed order of the preflight package.
// A read that fails ends the step with an error, because the call then knows nothing.
// A read of the item of the run applies the rule of the start again to the pull request that it judged, so a foreign pull request that opened after the start ends
// a run that may not hold it. A read of another item reads no author.
func (s *script) preflight(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	req, label, id, err := preflightArgs(b, args, kwargs)
	if err != nil {
		return nil, err
	}

	item, err := s.itemOf(id)
	if err != nil {
		return nil, err
	}

	if s.e.deps.Preflight == nil {
		return nil, errNoPreflight
	}

	value, err := s.durable("preflight", label, s.ask(item, req))
	if stop := stopOf(label, value); err == nil && stop != nil {
		s.stopped = stop
	}

	return value, err
}

// preflightArgs reads the keywords of a preflight call: the request, the name of the step and the id of the item to read.
// The id is empty when the call leaves it out, and the call then reads the item of the run.
// A claim keyword is refused with the name of claim_stop, also when a splat of a dict brings it.
func preflightArgs(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (req preflight.Request, label, id string, err error) {
	for _, pair := range kwargs {
		if key, isText := pair[0].(starlark.String); isText && slices.Contains(workflow.ClaimKeywords(), key.GoString()) {
			return req, "", "", errors.New(workflow.PreflightClaimRefusal(key.GoString()))
		}
	}

	err = workflow.Unpack(b.Name(), args, kwargs, map[string]any{headKeyword: &req.ExpectHead, baseKeyword: &req.ExpectBase, nameKeyword: &label, idKeyword: &id})

	return req, label, id, err
}

// ask is the recorded read of one preflight call, on the pull request of the item with the id.
func (s *script) ask(id string, req preflight.Request) func(context.Context, string) (stepResult, error) {
	return func(ctx context.Context, _ string) (stepResult, error) {
		own := s.owns(id)

		result, err := s.e.deps.Preflight.Preflight(ctx, s.in.Project, id, req, own)
		if err != nil {
			return stepResult{Outputs: preflightOutputs(result)}, err
		}

		if err := s.admitOwn(ctx, own, result.Pull); err != nil {
			return stepResult{}, err
		}

		return stepResult{Outputs: preflightOutputs(result)}, nil
	}
}

// stopOf is the stop that a preflight call reports in the result of the item, as a command that stopped the item does: the step, the exit code and the reason.
// It reads the recorded outputs, so a replay reports the stop of the first run. It is nil when the call passed.
func stopOf(label string, value starlark.Value) *Stopped {
	fields, ok := value.(*starlarkstruct.Struct)
	if !ok {
		return nil
	}

	// preflightOutputs records all four fields on every run, so these reads cannot miss.
	out := starlark.StringDict{}
	fields.ToStringDict(out)

	if out["ok"] == starlark.True {
		return nil
	}

	number, _ := workflow.Optional[starlark.Int](out[codeField])
	code, _ := number.Int64()
	reason, _ := workflow.Optional[starlark.String](out[reasonKey])

	return &Stopped{Step: stepOf(label), RC: int(code), Output: reason.GoString()}
}

// stepOf is the name of a preflight step in a stop.
func stepOf(label string) string {
	if label == "" {
		return "preflight"
	}

	return "preflight:" + label
}
