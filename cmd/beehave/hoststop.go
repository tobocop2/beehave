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
// waiting counts the gates that wait or run on a cluster. A provider with a size table reads it for the cluster when it starts a host, and no other provider does.
func (s hostStop) Up(ctx context.Context, project, cluster string, waiting func(cluster string) (int, error)) (started bool, chosen string, err error) {
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

	started, err = launchWhenAbsent(ctx, h, project, func() (int, error) { return waiting(chosen) })

	return started, chosen, err
}

// Sized says whether the provider of the cluster has a size table. It builds the host and calls nothing on it. A project with no host block gives false.
func (s hostStop) Sized(project, cluster string) (bool, error) {
	found, err := s.sizedHostOf(project, cluster)

	return found.host != nil, err
}

// sizedFound is the host of a cluster that has a size table, and the cluster that the call used. The host is nil for a project with no host block and for a provider with no size table.
type sizedFound struct {
	host    host.Host
	cluster string
}

// sizedHostOf builds the host of the cluster and keeps it only when the provider has a size table.
func (s hostStop) sizedHostOf(project, cluster string) (sizedFound, error) {
	chosen, err := s.programs.resolve(project, cluster)
	if err != nil {
		return sizedFound{}, err
	}

	h, err := s.programs.hosts(project, chosen)
	if errors.Is(err, errNoHost) {
		return sizedFound{cluster: chosen}, nil
	}

	if err != nil {
		return sizedFound{}, err
	}

	if _, sized := h.(sizedHost); !sized {
		return sizedFound{cluster: chosen}, nil
	}

	return sizedFound{host: h, cluster: chosen}, nil
}

// Defer says whether host_up leaves the cluster for the first gate: the provider has a size table and the host is absent.
// A host that is up, or in another state, is left to Up, which keeps it or refuses it by name.
func (s hostStop) Defer(ctx context.Context, project, cluster string) (deferred bool, chosen string, err error) {
	found, err := s.sizedHostOf(project, cluster)
	if found.host == nil || err != nil {
		return false, found.cluster, err
	}

	status, err := found.host.Status(ctx)
	if err != nil {
		return false, "", fmt.Errorf("project %s: read the host before the start: %w", project, err)
	}

	return status.State == model.HostAbsent, found.cluster, nil
}

// launchWhenAbsent reads the state of the host and launches it only when it is absent.
func launchWhenAbsent(ctx context.Context, h host.Host, project string, waiting func() (int, error)) (bool, error) {
	status, err := h.Status(ctx)
	if err != nil {
		return false, fmt.Errorf("project %s: read the host before the start: %w", project, err)
	}

	return launchIfAbsent(ctx, h, project, status.State, waiting)
}

// sizedHost is a host whose size follows the queue: the provider has a size table.
type sizedHost interface {
	UpQueued(ctx context.Context, queued int) error
}

// start launches the host. A host with a size table gets the queue length first.
func start(ctx context.Context, h host.Host, waiting func() (int, error)) error {
	sized, ok := h.(sizedHost)
	if !ok {
		return h.Up(ctx)
	}

	queued, err := waiting()
	if err != nil {
		return fmt.Errorf("read the gates that wait for the host: %w", err)
	}

	return sized.UpQueued(ctx, queued)
}

// resolve gives the declared cluster for the choice of a run. Without a declared function the choice stands as it is.
func (p programs) resolve(project, cluster string) (string, error) {
	if p.declared == nil {
		return cluster, nil
	}

	return p.declared(project, cluster)
}

// launchIfAbsent launches the host only in the state absent. It leaves a host that is up alone and refuses every other state by name.
func launchIfAbsent(ctx context.Context, h host.Host, project string, state model.HostState, waiting func() (int, error)) (bool, error) {
	switch {
	case state == model.HostUp:
		return false, nil
	case state != model.HostAbsent:
		return false, fmt.Errorf("project %s: %w: the host is %q. Stop it or wait until it is up, then run again", project, errHostState, string(state))
	}

	if err := start(ctx, h, waiting); err != nil {
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

// Ready says whether the cluster takes a new job: it is up and its disk holds the floor of the project. A project with no host block has no host that takes a job.
// A host that cannot be read gives an error that names the host and the fix.
func (s hostStop) Ready(ctx context.Context, project, cluster string) (bool, error) {
	chosen, err := s.programs.resolve(project, cluster)
	if err != nil {
		return false, err
	}

	h, err := s.programs.hosts(project, chosen)
	if errors.Is(err, errNoHost) {
		return false, nil
	}

	if err != nil {
		return false, err
	}

	ready, err := host.Ready(ctx, h)
	if err != nil {
		return false, fmt.Errorf("cannot read host %s of project %s: check that it answers: %w", chosen, project, err)
	}

	return ready, nil
}

// LostProof stops the job of a gate whose wait ended as lost, on the cluster that the job ran on, and reads whether it can no longer run.
// It gets the host as Teardown does, and asks the provider. A project with no host block has no job that can run: no proof and no error.
func (s hostStop) LostProof(ctx context.Context, project, cluster, job string) (model.LostProof, error) {
	chosen, err := s.programs.resolve(project, cluster)
	if err != nil {
		return model.LostProof{}, err
	}

	h, err := s.programs.hosts(project, chosen)
	if errors.Is(err, errNoHost) {
		return model.LostProof{Note: fmt.Sprintf("project %s has no host, so it has no host job", project)}, nil
	}

	if err != nil {
		return model.LostProof{}, err
	}

	return h.LostProof(ctx, model.JobID(job))
}
