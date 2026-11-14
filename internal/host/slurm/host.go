package slurm

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/tobocop2/beehave/internal/host/jobwait"
	"github.com/tobocop2/beehave/internal/host/link"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc"
)

// Name is the name the Slurm host provider is chosen by. The host is a Slurm cluster whose head node beehave reaches over ssh.
const Name model.HostProvider = "slurm"

// Host is a Slurm cluster that the owner provides and beehave reaches over ssh, at the head node. It creates and deletes nothing.
//
// Up checks that the head node answers and runs Slurm, and changes nothing. Down sends nothing. A caller releases the host with Teardown,
// which banks the work and leaves the cluster up. No slot holds the cluster for a job, because nothing deletes an idle cluster.
// The run limit `host.max_run` goes to sbatch as `--time`, and Slurm ends a job at the limit as timed out.
type Host struct {
	*Queue
	link *link.Link
}

// NewHost returns the host for the cluster. target is the ssh alias of the head node, or a label when `host.slurm.address` names it.
// It runs nothing, so a check of the config needs no network. A setting that could change a command line is refused.
// Jobs run in the login folder of the head node, so `host.slurm.work_root` is refused: a folder that Slurm does not use would hide the logs and the work.
func NewHost(target string, config model.HostConfig, ssh proc.Runner, opts ...Option) (*Host, error) {
	values, err := checkConfig(config)
	if err != nil {
		return nil, err
	}

	l, err := link.ForMachine(target, link.Options{
		Address: values.address, User: values.user, Identity: values.identity, KnownHosts: values.knownHosts, Port: values.port,
	}, ssh)
	if err != nil {
		return nil, err
	}

	opts = append([]Option{func(s *settings) { s.limit = config.MaxRun }}, opts...)

	return &Host{Queue: New(l, config, noSlot{}, opts...), link: l}, nil
}

// checkConfig reads the block of the config and returns it. It refuses a run limit under a second and a work root.
func checkConfig(config model.HostConfig) (block, error) {
	values, err := decodeBlock(config.Block)
	if err != nil {
		return block{}, err
	}

	switch {
	case config.MaxRun < time.Second:
		return block{}, fmt.Errorf("%w: `host.max_run` must be at least one second", link.ErrConfig)
	case values.workRoot != "":
		return block{}, fmt.Errorf("%w: `host.slurm.work_root` is not used with the slurm provider. Jobs run in the login folder of the head node. Remove the key", link.ErrConfig)
	}

	return values, nil
}

// Up checks that the head node answers and that Slurm lists its nodes. It is the first call. It is idempotent.
// The disk is not read here: Status reports it, and the guards refuse a disk that cannot be read.
func (h *Host) Up(ctx context.Context) error {
	if _, err := h.draining(ctx); err != nil {
		return fmt.Errorf("cannot use the head node. Check that `ssh %s` works from this computer and that Slurm runs there: %w", h.link.Target(), err)
	}

	return nil
}

// Status reports the host absent when ssh cannot connect, draining when Slurm drains its nodes, and its free disk space.
// A df answer that does not parse is an error, which the guards refuse.
func (h *Host) Status(ctx context.Context) (model.HostStatus, error) {
	status, err := h.Readings(ctx)
	if errors.Is(err, link.ErrDropped) {
		return model.HostStatus{State: model.HostAbsent}, nil
	}

	return status, err
}

// Down sends nothing and stops no job. beehave did not create the cluster, so it never deletes it.
func (*Host) Down(context.Context) error { return nil }

// Teardown banks the work of the head node to `host.private_remote` and leaves the cluster up. It is the release of a host that beehave does not own:
// host.Teardown cannot be used, as it ends with an audit that the host is gone.
func (h *Host) Teardown(ctx context.Context) error {
	if err := h.bank(ctx); err != nil {
		return fmt.Errorf("host keeps its work: %w", err)
	}

	return nil
}

// noSlot is the slot of a cluster that nothing deletes. Holding and releasing are nothing to do.
type noSlot struct{}

func (noSlot) Hold(context.Context, jobwait.Job) error { return nil }

func (noSlot) Release(context.Context, jobwait.Job) error { return nil }
