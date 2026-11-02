package flow

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestClient_ResumeOnThisBuildNamesARunWithNoStepList(t *testing.T) {
	t.Parallel()

	h, _, _ := strandedHarness(t)
	_, err := sqlOf(t, h).ExecContext(t.Context(), "UPDATE workflow_status SET application_version = ? WHERE workflow_uuid = ?", testVersion, testRun)
	require.NoError(t, err)
	expireLease(h)

	err = h.client(t, testVersion).ResumeOnThisBuild(testItem)
	require.ErrorIs(t, err, errStranded)
	require.Contains(t, err.Error(), "recorded under no step list and this build has step list ")
}

func TestClient_ResumeOnThisBuildFinishesARunWithSwarmChildren(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.flowIs(t, swarmFile(`fail = "raise", `))

	var healed atomic.Bool

	started := make(chan struct{})

	var once atomic.Bool

	h.commands.before = func(ctx context.Context, request CommandRequest) error {
		if healed.Load() {
			return nil
		}

		if request.Item == "22" {
			<-started

			return errFake
		}

		if request.Item == "21" || request.Item == "23" {
			if once.CompareAndSwap(false, true) {
				close(started)
			}

			<-ctx.Done()

			return ctx.Err()
		}

		return nil
	}

	engine := h.engine(t)
	_, err := engine.Run(testItem, testProject)
	require.Error(t, err)
	require.NoError(t, engine.Shutdown())
	healed.Store(true)

	other := h.client(t, otherBuild)
	require.NoError(t, other.ResumeOnThisBuild(testItem))

	h.opts.Version = otherBuild
	done := runInBackground(h.engine(t), testItem)

	select {
	case out := <-done:
		require.NoError(t, out.err)
	case <-time.After(45 * time.Second):
		t.Fatal("the run did not finish on the new build")
	}
}
