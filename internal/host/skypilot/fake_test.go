package skypilot

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/host/link"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc/proctest"
)

const (
	clusterName = "lane-host"
	loginFolder = "/home/lane"
)

// fakeQueue is a Queue that answers from fields. It stands in for the job queue on the head node.
// err fails Readings. readErr fails the reads of a job: Submit, Wait, WorkRoot and Log.
type fakeQueue struct {
	status  model.HostStatus
	err     error
	readErr error
	slot    Slot
}

func (q *fakeQueue) Readings(context.Context) (model.HostStatus, error) { return q.status, q.err }

func (q *fakeQueue) Submit(context.Context, model.JobSpec) (model.JobID, error) {
	if q.readErr != nil {
		return "", q.readErr
	}

	return "1", nil
}

func (q *fakeQueue) Wait(context.Context, model.JobID) (model.JobState, error) {
	if q.readErr != nil {
		return "", q.readErr
	}

	return model.JobSucceeded, nil
}

func (q *fakeQueue) Cancel(context.Context, model.JobID) error { return nil }

func (q *fakeQueue) WorkRoot(context.Context) (string, error) {
	if q.readErr != nil {
		return "", q.readErr
	}

	return loginFolder, nil
}

func (q *fakeQueue) Log(context.Context, model.JobID) (string, error) { return "", q.readErr }

func (q *fakeQueue) Drain(context.Context) error { return nil }

func (q *fakeQueue) Resume(context.Context) error { return nil }

// scripts are the scripted sky and ssh command lines of a host.
type scripts struct{ sky, ssh *proctest.Scripted }

// hostConfig is a valid host with the given placement. A nil placement leaves the choice to SkyPilot.
func hostConfig(placed *model.SkyPilotConfig) model.HostConfig {
	cfg := model.HostConfig{Provider: model.HostSkyPilot, IdleMinutes: 45, MaxRun: 12 * time.Hour}
	if placed != nil {
		cfg.SkyPilot = *placed
	}

	return cfg
}

func placement() *model.SkyPilotConfig {
	return &model.SkyPilotConfig{Cloud: "gcp", Region: "us-central1", InstanceType: "n2-standard-8"}
}

// newHost returns a host over scripted sky and ssh runners and the fake queue.
func newHost(t *testing.T, cfg model.HostConfig) (*Host, *fakeQueue, *proctest.Scripted, *proctest.Scripted) {
	t.Helper()

	queue, sky, ssh := &fakeQueue{}, &proctest.Scripted{}, &proctest.Scripted{}

	h, err := New(clusterName, cfg, sky, ssh, func(_ *link.Link, _ model.HostConfig, slot Slot) Queue {
		queue.slot = slot

		return queue
	})
	require.NoError(t, err)

	return h, queue, sky, ssh
}
