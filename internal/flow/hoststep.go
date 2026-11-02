package flow

import (
	"context"

	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	"github.com/tobocop2/obee/internal/workflow"
)

// hostKeeper starts and stops the build host of a project. The host package owns the order of a stop: scan, bank, then stop.
// A host that cannot bank its work stays up, and Teardown returns the reason.
// Up launches the host when it is absent. started is true only for a host that this call launched: a host that was up, and a project with no host, give false.
type hostKeeper interface {
	Up(ctx context.Context, project string) (started bool, err error)
	Teardown(ctx context.Context, project string) error
}

var errNoHosts = &absentError{message: "this build cannot stop a host. Use a build that sets a host keeper", step: "stop the build host"}

// hostUp launches the build host of the project when it is absent, and tells the run whether this call launched it.
// The cluster deletes itself when idle (host.idle_minutes). A project with no host, a host that is already up and a build with no host keeper
// start nothing and give started false. A host that fails to start, or that is in a state other than absent or up, ends the step with the error.
// The result is recorded, so a replay returns the first answer and launches nothing. A replay after a crash between the launch and the record
// finds the host up and reports started false: that run then does not stop the host, and the idle watchdog deletes it.
func (s *script) hostUp(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	var label string

	if err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{nameKeyword: &label}); err != nil {
		return nil, err
	}

	value, err := s.durable("host_up", label, s.startHost)
	if err != nil {
		return nil, err
	}

	if outputs, ok := value.(*starlarkstruct.Struct); ok {
		started, attrErr := outputs.Attr("started")

		s.hostStarted = s.hostStarted || (attrErr == nil && started == starlark.True)
	}

	return value, nil
}

// startHost is the recorded start of the host.
func (s *script) startHost(ctx context.Context, _ string) (stepResult, error) {
	if s.e.deps.Hosts == nil {
		return stepResult{Outputs: upOutputs(false)}, nil
	}

	started, err := s.e.deps.Hosts.Up(ctx, s.in.Project)
	if err != nil {
		return stepResult{}, err
	}

	return stepResult{Outputs: upOutputs(started)}, nil
}

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

	if !s.hostStarted {
		return s.durable("host_teardown", label, func(context.Context, string) (stepResult, error) {
			return stepResult{Outputs: teardownOutputs(false, reasonNotStarted)}, nil
		})
	}

	return s.durable("host_teardown", label, s.stopHost)
}

// reasonNotStarted is the reason of a stop that does nothing: another run owns the host, or this run started none.
const reasonNotStarted = "this run did not start the host, so it does not stop it"

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
