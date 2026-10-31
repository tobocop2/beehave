package flow

import (
	"context"

	"go.starlark.net/starlark"

	"github.com/tobocop2/obee/internal/workflow"
)

// hostStopper stops the build host of a project after it banks the work on it. The host package owns the order: scan, bank, then stop.
// A host that cannot bank its work stays up, and Teardown returns the reason.
type hostStopper interface {
	Teardown(ctx context.Context, project string) error
}

var errNoHosts = &absentError{message: "this build cannot stop a host. Use a build that sets a host stopper", step: "stop the build host"}

// hostTeardown stops the build host of the project. A host that stays up is a result, not an error: down is false and reason says why,
// so the workflow can report it. A build with no host stopper fails the call, and so does a run that was canceled.
func (s *script) hostTeardown(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	var label string

	if err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{nameKeyword: &label}); err != nil {
		return nil, err
	}

	if s.e.deps.Hosts == nil {
		return nil, errNoHosts
	}

	return s.durable("host_teardown", label, s.stopHost)
}

// stopHost is the recorded stop of the host. A canceled run records nothing, so a resume stops the host again.
func (s *script) stopHost(ctx context.Context, _ string) (stepResult, error) {
	err := s.e.deps.Hosts.Teardown(ctx, s.in.Project)

	switch {
	case err != nil && ctx.Err() != nil:
		return stepResult{}, err
	case err != nil:
		return stepResult{Outputs: teardownOutputs(false, err.Error())}, nil
	}

	return stepResult{Outputs: teardownOutputs(true, "")}, nil
}
