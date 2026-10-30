package flow

import (
	"context"
	"errors"
	"strings"

	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	"github.com/tobocop2/obee/internal/preflight"
	"github.com/tobocop2/obee/internal/workflow"
)

// preflighter judges the pull request of an item before an agent works on it. The caller finds the pull request: Request.PR is zero.
// An item with no pull request yet has nothing that can have changed, so it gives Unchanged.
type preflighter interface {
	Preflight(ctx context.Context, project, item string, req preflight.Request, hold string) (preflight.Result, error)
}

var errNoPreflight = errors.New("this build cannot read the forge before a step. Use a build that sets a preflight reader")

// preflightOutputs are the fields of a preflight call. A stop is a result, not an error: ok is false, and code and stop say why.
// The reason is empty when ok is true.
func preflightOutputs(result preflight.Result) map[string]any {
	reason := ""
	if !result.OK() {
		reason = strings.Join(result.Lines, "; ")
	}

	return map[string]any{"ok": result.OK(), "stop": result.Stop.String(), "code": result.Stop.Code(), reasonKey: reason}
}

// preflight reads the pull request of the item and returns the first stop in the fixed order of the preflight package.
// A read that fails ends the step with an error, because the call then knows nothing.
func (s *script) preflight(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	var head, base, hold, label string

	if err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{headKeyword: &head, baseKeyword: &base, holdKeyword: &hold, nameKeyword: &label}); err != nil {
		return nil, err
	}

	if s.e.deps.Preflight == nil {
		return nil, errNoPreflight
	}

	value, err := s.durable("preflight", label, s.ask(preflight.Request{ExpectHead: head, ExpectBase: base}, hold))
	if stop := stopOf(label, value); err == nil && stop != nil {
		s.stopped = stop
	}

	return value, err
}

// ask is the recorded read of one preflight call.
func (s *script) ask(req preflight.Request, hold string) func(context.Context, string) (stepResult, error) {
	return func(ctx context.Context, _ string) (stepResult, error) {
		result, err := s.e.deps.Preflight.Preflight(ctx, s.in.Project, s.in.Item, req, hold)

		return stepResult{Outputs: preflightOutputs(result)}, err
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

	number, _ := optional[starlark.Int](out["code"])
	code, _ := number.Int64()
	reason, _ := optional[starlark.String](out[reasonKey])

	return &Stopped{Step: stepOf(label), RC: int(code), Output: reason.GoString()}
}

// stepOf is the name of a preflight step in a stop.
func stepOf(label string) string {
	if label == "" {
		return "preflight"
	}

	return "preflight:" + label
}
