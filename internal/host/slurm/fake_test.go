package slurm

import (
	"context"

	"github.com/tobocop2/obee/internal/host/jobwait"
	"github.com/tobocop2/obee/internal/host/link"
	"github.com/tobocop2/obee/internal/host/slurmtest"
	"github.com/tobocop2/obee/internal/model"
)

// sshFailed is the exit status of ssh for a link that dropped.
const sshFailed = 255

// fakeSlot is the slot of a test: Hold records the marker of a job and Release writes it into the login folder, as the SkyPilot slot does.
type fakeSlot struct{ f *slurmtest.Cluster }

func markerOf(job jobwait.Job) string { return ".obee-banked-" + slurmtest.Target + "-" + job.String() }

func (s fakeSlot) Hold(_ context.Context, job jobwait.Job) error {
	s.f.Mu.Lock()
	defer s.f.Mu.Unlock()

	s.f.Slots = append(s.f.Slots, markerOf(job))

	return nil
}

func (s fakeSlot) Release(_ context.Context, job jobwait.Job) error {
	s.f.Mu.Lock()
	defer s.f.Mu.Unlock()

	s.f.Files[markerOf(job)] = true

	return nil
}

// newTestQueue returns the queue of a host that is up, with the slot of the fake. The test replaces its clock and its bank.
func newTestQueue(f *slurmtest.Cluster, config model.HostConfig, opts ...Option) *Queue {
	f.Up = true

	return New(link.New(slurmtest.Target, f.SSH()), config, fakeSlot{f}, opts...)
}
