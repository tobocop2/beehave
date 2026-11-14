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

// placed is the skypilot block of a test, with the keys that the project gives. An empty field means the project leaves the key out.
type placed struct{ cloud, region, instanceType string }

// block returns the skypilot block of the placement, with an entry for each key that it sets.
func (p placed) block() *model.Block {
	var entries []model.BlockEntry

	for key, value := range map[string]string{BlockCloud: p.cloud, BlockRegion: p.region, BlockInstanceType: p.instanceType} {
		if value != "" {
			entries = append(entries, model.TextEntry(key, value))
		}
	}

	return model.NewBlock("test", entries)
}

// hostConfig is a valid host with the given placement. A nil placement means the project gives no block and leaves the choice to SkyPilot.
func hostConfig(placement *placed) model.HostConfig {
	cfg := model.HostConfig{Provider: Name, IdleMinutes: 45, MaxRun: 12 * time.Hour}
	if placement != nil {
		cfg.Block = placement.block()
	}

	return cfg
}

func placement() *placed {
	return &placed{cloud: "gcp", region: "us-central1", instanceType: "n2-standard-8"}
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
