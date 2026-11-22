package flow

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// waitingParent is a parent that spawns one child and waits for it.
const waitingParent = `def worker(item):
    command(name = "w", run = ["w"])

def main(item):
    gather(spawn(items = ["21"], entry = "worker", name = "fan"), name = "fan")
`

// stepsWhileChildRuns runs the parent until its child runs, and returns the steps view of the parent at that time.
func stepsWhileChildRuns(t *testing.T) RunSteps {
	t.Helper()

	var childRuns atomic.Bool

	h := newHarness(t)
	h.commands.before = func(ctx context.Context, request CommandRequest) error {
		if request.Item == testItem {
			return nil
		}

		childRuns.Store(true)
		receive(t, ctx.Done())

		return ctx.Err()
	}

	engine := h.engine(t)
	client := h.client(t, testVersion)
	done := make(chan error, 1)
	file := writeFlow(t, waitingParent)

	go func() {
		_, err := engine.RunFile(testItem, testProject, file, modeOff())
		done <- err
	}()

	waitOn(t, waiting[struct{}]{what: "the child to run", run: runOfEngine(t, engine), read: func() (struct{}, bool, error) {
		return struct{}{}, childRuns.Load(), nil
	}})

	view, err := client.Steps(testRun)
	require.NoError(t, err)
	require.NoError(t, engine.Shutdown())
	require.Error(t, receive(t, done), "the shutdown interrupts the run that waits for its child")

	return view
}

func TestClient_StepsOfAParentThatWaitsForAChildShowNoZeroTimeAndNoOverflowedDuration(t *testing.T) {
	t.Parallel()

	view := stepsWhileChildRuns(t)
	require.NotEmpty(t, view.Steps)

	for _, line := range view.Steps {
		if line.Started.IsZero() || (line.Ended.IsZero() && line.State == "done") {
			assert.Nil(t, line.Seconds, "%s has no start or no end, so it has no duration", line.Name)
		} else {
			require.NotNil(t, line.Seconds, line.Name)
			assert.InDelta(t, 0.0, *line.Seconds, float64(24*time.Hour/time.Second), "%s has a duration that is not a time span", line.Name)
		}
	}

	last := view.Steps[len(view.Steps)-1]
	assert.Equal(t, unrecordedStep, last.Name)
	assert.True(t, last.Started.IsZero())
	assert.Equal(t, []string{"item-21-r1"}, last.WaitsOn, "the step in progress names the child that it waits for")
}
