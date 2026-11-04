package flow

import (
	"cmp"
	"context"

	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	"github.com/tobocop2/beehave/internal/workflow"
)

// hostKeeper starts and stops the build host of a project. The host package owns the order of a stop: scan, bank, then stop.
// A host that cannot bank its work stays up, and Teardown returns the reason.
// For a machine or a cluster that the owner provides (the providers ssh and slurm), Teardown banks the work and leaves the machine up: beehave never deletes it.
// Up launches the host when it is absent. started is true only for a host that this call launched: a host that was up, and a project with no host, give false.
// cluster names one build host of the project. An empty cluster means the build host of the project, and Up returns the cluster it used.
// A cluster that the project file does not declare is an error.
type hostKeeper interface {
	Up(ctx context.Context, project, cluster string) (started bool, chosen string, err error)
	Teardown(ctx context.Context, project, cluster string) error
}

var errNoHosts = &absentError{message: "this build cannot stop a host. Use a build that sets a host keeper", step: "stop the build host"}

// hostUp launches the build host of the project when it is absent, and tells the run whether this call launched it.
// The cluster deletes itself when idle (host.idle_minutes). A project with no host, a host that is already up and a build with no host keeper
// start nothing and give started false. A host that fails to start, or that is in a state other than absent or up, ends the step with the error.
// The result is recorded, so a replay returns the first answer and launches nothing. A replay after a crash between the launch and the record
// finds the host up and reports started false: that run then does not stop the host, and the idle watchdog deletes it.
func (s *script) hostUp(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	var label, cluster string

	if err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{nameKeyword: &label, hostKeyword: &cluster}); err != nil {
		return nil, err
	}

	value, err := s.durable("host_up", label, func(ctx context.Context, _ string) (stepResult, error) { return s.startHost(ctx, cluster) })
	if err != nil {
		return nil, err
	}

	if outputs, ok := value.(*starlarkstruct.Struct); ok {
		s.noteHost(outputs)
	}

	return value, nil
}

// noteHost keeps what the run needs from the result of host_up: whether this run launched the host, and the cluster it chose.
// A result that an older run recorded has no host field, and the cluster stays empty.
func (s *script) noteHost(outputs *starlarkstruct.Struct) {
	started, err := outputs.Attr("started")

	s.hostStarted = s.hostStarted || (err == nil && started == starlark.True)

	if chosen, err := outputs.Attr(hostKeyword); err == nil {
		if text, isText := chosen.(starlark.String); isText {
			s.hostChosen = string(text)
		}
	}
}

// startHost is the recorded start of the host. The record holds the cluster that the call resolved, so a replay and host_teardown read the same choice.
func (s *script) startHost(ctx context.Context, cluster string) (stepResult, error) {
	if s.e.deps.Hosts == nil {
		return stepResult{Outputs: upOutputs(false, cluster)}, nil
	}

	started, chosen, err := s.e.deps.Hosts.Up(ctx, s.in.Project, cluster)
	if err != nil {
		return stepResult{}, err
	}

	return stepResult{Outputs: upOutputs(started, chosen)}, nil
}

// hostTeardown stops the build host of the project: it banks the work first. The output down is true when the host is released, which for the providers
// ssh and slurm means banked and not in use by beehave; the machine stays up. A host that stays up is a result, not an error: down is false and reason says why,
// so the workflow can report it. A build with no host stopper fails the call, and so does a run that was canceled.
func (s *script) hostTeardown(b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) { //nolint:ireturn // the builtin contract of starlark-go
	var label, cluster string

	if err := workflow.Unpack(b.Name(), args, kwargs, map[string]any{nameKeyword: &label, hostKeyword: &cluster}); err != nil {
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

	return s.durable("host_teardown", label, func(ctx context.Context, _ string) (stepResult, error) {
		return s.stopHost(ctx, cmp.Or(cluster, s.hostChosen))
	})
}

// reasonNotStarted is the reason of a stop that does nothing: another run owns the host, or this run started none.
const reasonNotStarted = "this run did not start the host, so it does not stop it"

// stopHost is the recorded stop of the host. A canceled run records nothing, so a resume stops the host again.
func (s *script) stopHost(ctx context.Context, cluster string) (stepResult, error) {
	err := s.e.deps.Hosts.Teardown(ctx, s.in.Project, cluster)

	switch {
	case err != nil && ctx.Err() != nil:
		return stepResult{}, err
	case err != nil:
		return stepResult{Outputs: teardownOutputs(false, err.Error())}, nil
	}

	return stepResult{Outputs: teardownOutputs(true, "")}, nil
}
