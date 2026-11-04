package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/tobocop2/beehave/internal/host"
	"github.com/tobocop2/beehave/internal/model"
)

var errHostState = errors.New("the host is not absent or up")

var errNoTeardown = errors.New("the host of the project cannot bank its work and stop. Use a host provider that has a teardown")

// bankingHost is a host that banks its work and then stops. A provider that has no way to run git and find on its machine does not have it.
type bankingHost interface {
	Teardown(ctx context.Context) error
}

// hostStop starts and stops the build host of a project for the host_up and host_teardown builtins.
// On a stop the provider banks the work first and keeps the host up when it cannot.
type hostStop struct {
	programs programs
}

// Up launches the host of the project when it is absent, and reports started. A host that is up is left alone (started is false).
// Any other state is refused with its name, because a launch over a host in a state that beehave does not know could replace work.
// A project with no host block has nothing to start: started is false and there is no error.
// cluster is the host that the run chose. An empty one gives the build host of the project, and a cluster that the project does not declare is refused
// before any host is built. chosen is the cluster that the call used.
func (s hostStop) Up(ctx context.Context, project, cluster string) (started bool, chosen string, err error) {
	chosen, err = s.programs.resolve(project, cluster)
	if err != nil {
		return false, "", err
	}

	h, err := s.programs.hosts(project, chosen)
	if errors.Is(err, errNoHost) {
		return false, chosen, nil
	}

	if err != nil {
		return false, "", err
	}

	started, err = launchWhenAbsent(ctx, h, project)

	return started, chosen, err
}

// launchWhenAbsent reads the state of the host and launches it only when it is absent.
func launchWhenAbsent(ctx context.Context, h host.Host, project string) (bool, error) {
	status, err := h.Status(ctx)
	if err != nil {
		return false, fmt.Errorf("project %s: read the host before the start: %w", project, err)
	}

	return launchIfAbsent(ctx, h, project, status.State)
}

// resolve gives the declared cluster for the choice of a run. Without a declared function the choice stands as it is.
func (p programs) resolve(project, cluster string) (string, error) {
	if p.declared == nil {
		return cluster, nil
	}

	return p.declared(project, cluster)
}

// launchIfAbsent launches the host only in the state absent. It leaves a host that is up alone and refuses every other state by name.
func launchIfAbsent(ctx context.Context, h host.Host, project string, state model.HostState) (bool, error) {
	switch {
	case state == model.HostUp:
		return false, nil
	case state != model.HostAbsent:
		return false, fmt.Errorf("project %s: %w: the host is %q. Stop it or wait until it is up, then run again", project, errHostState, string(state))
	}

	if err := h.Up(ctx); err != nil {
		return false, err
	}

	return true, nil
}

// Teardown stops the host of the project. A project with no host block, or a provider with no teardown, is an error.
func (s hostStop) Teardown(ctx context.Context, project, cluster string) error {
	chosen, err := s.programs.resolve(project, cluster)
	if err != nil {
		return err
	}

	h, err := s.programs.hosts(project, chosen)
	if errors.Is(err, errNoHost) {
		return fmt.Errorf("project %s has no host, so there is no host to stop", project)
	}

	if err != nil {
		return err
	}

	banking, ok := h.(bankingHost)
	if !ok {
		return fmt.Errorf("project %s: %w", project, errNoTeardown)
	}

	return banking.Teardown(ctx)
}
