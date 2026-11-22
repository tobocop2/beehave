package flow

import (
	"context"
	"testing"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// failFlow calls now twice and waits for no one.
const failFlow = "def main(item):\n    now()\n    now()\n"

// oldProbeFailingSecond is the old build whose second call fails, so the run ends as failed after one recorded step.
func oldProbeFailingSecond() func(*script) (string, error) {
	calls := 0

	return func(s *script) (string, error) {
		calls++
		if calls == 2 {
			return countedStep(s, "now", "", func(context.Context) (string, error) { return "", errFake })
		}

		return oldProbe(s)
	}
}

func TestPatch_AnOpenRunOfAnotherReleaseBuildIsStillRefusedByRunWhenNoModuleChanged(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	seen := &probes{}

	h.opts.Modules = probeModules(oldProbe, seen)
	file := writeFlow(t, probeFlow)
	interrupted(t, h, file)
	expireLease(h)

	h.opts.Modules = probeModules(newProbeBehindPatch, seen)
	h.opts.Version = otherBuild
	engine := h.engine(t)

	_, err := runFileWithin(t, engine, file)

	require.ErrorIs(t, err, errStranded, "join compares the whole build string, so the patch alone does not let beehave run finish the old run")
	require.ErrorContains(t, err, "--on-this-build")
	assert.Equal(t, []string{"old"}, seen.all(), "no call ran on the new build")
}

func TestPatch_ARunThatHoldsAMarkerMovesToAnotherReleaseBuild(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	seen := &probes{}

	h.opts.Modules = probeModules(newProbeBehindPatch, seen)
	file := writeFlow(t, probeFlow)
	interrupted(t, h, file)
	expireLease(h)

	h.opts.Version = otherBuild
	engine := h.engine(t)
	client := h.client(t, otherBuild)

	last, found, err := latestRun(engine.ctx, testItem)
	require.NoError(t, err)
	require.True(t, found)

	names, err := client.stepNames(last.Status.ID)
	require.NoError(t, err)
	t.Logf("steps before the move: %v", names)
	require.Equal(t, 1, markers(names), "the run holds a marker before the move")

	require.NoError(t, client.ResumeOnThisBuild(testItem), "a step with the DBOS prefix is a step of the engine, so the marker does not block the move")

	handle, err := dbos.RetrieveWorkflow[ItemResult](engine.ctx, last.Status.ID)
	require.NoError(t, err)

	waitOn(t, waiting[struct{}]{what: "the moved run to take the answer", run: runOfClient(t, client), read: func() (struct{}, bool, error) {
		return struct{}{}, client.Reply(testItem, Reply{Text: "yes"}) == nil, nil
	}})

	_, err = resultOfHandle(t, handle)

	require.NoError(t, err)
	assert.Equal(t, []string{"new", "new", "new"}, seen.all())
}

func TestPatch_AFailedRunRewoundByResumeAfterTheChangeKeepsItsOldCallAndTakesTheNewNext(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	seen := &probes{}

	h.opts.Modules = probeModules(oldProbeFailingSecond(), seen)
	file := writeFlow(t, failFlow)
	first := h.engine(t)

	_, err := first.RunFile(testItem, testProject, file)
	require.Error(t, err, "the second call of the old build fails")
	require.NoError(t, first.Shutdown())

	h.opts.Modules = probeModules(newProbeBehindPatch, seen)
	next := h.engine(t)

	_, err = resumeAndJoin(t, next, h.client(t, testVersion))

	require.NoError(t, err)
	assert.Equal(t, []string{"old", "old", "new"}, seen.all(), "call 1 replays its old step, and the failed call 2 runs again as new code")
}
