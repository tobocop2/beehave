package flow

import (
	"errors"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/merge"
)

var errNoGateHere = errors.New("the gate does not run on this machine")

func TestGateRefusal_AProjectGateIsRefusedBeforeTheKeyTheEarlierPassAndAnyGitRead(t *testing.T) {
	t.Parallel()

	h := reuseHarness(t, []GateResult{{Passed: true, Head: gatedHead, Tree: "tree-1", Steps: stepsPass}}, []GateKey{gateKey("k", gatedHead), gateKey("k", rewrittenHead)})
	h.refuseGate = func(string, bool) error { return errNoGateHere }

	_, err := walkText(t, h, bodyFlow(twoGates))

	require.ErrorContains(t, err, errNoGateHere.Error())
	assert.Zero(t, h.gate.keyReads, "no gate key is read")
	assert.Zero(t, h.gate.calls, "no gate runs")
	assert.Zero(t, h.gate.submits.Load(), "no host job starts")
	assert.Empty(t, h.commands.ran())
}

func TestGateRefusal_AProgramGateIsRefusedAndRunsNoProgram(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.refuseGate = func(_ string, program bool) error {
		if !program {
			return nil
		}

		return errNoGateHere
	}

	_, err := walkBody(t, h, "gate(run = [\"true\"], name = \"links\")\n")

	require.ErrorContains(t, err, errNoGateHere.Error())
	assert.Empty(t, h.commands.ran())
}

func TestGateRefusal_AProgramGateOnAHostIsNotRefused(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.refuseGate = func(string, bool) error { return errNoGateHere }

	_, err := walkBody(t, h, "gate(run = [\"true\"], host = \"cluster\", name = \"links\")\n")

	require.NoError(t, err)
	assert.Equal(t, []string{"true"}, h.commands.ran())
}

func TestGateRefusal_ABatchMergeIsRefusedWithAProjectGateAndWithAGateRun(t *testing.T) {
	t.Parallel()

	for name, extra := range map[string]string{"project gate": "", "gate_run": `gate_run = ["check-all"], `} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig := newBatchRig(t)
			rig.h.refuseGate = func(string, bool) error { return errNoGateHere }

			_, err := rig.walk(t, rig.call("1", extra))

			require.ErrorContains(t, err, errNoGateHere.Error())
			assert.Empty(t, rig.h.commands.ran())
			assert.Empty(t, rig.merger.landed())
		})
	}
}

func TestGateRefusal_ABatchMergeIsRefusedBeforeAnyGitCommandWorktreeOrProgram(t *testing.T) {
	t.Parallel()

	for name, extra := range map[string]string{"project gate": "", "gate_run": `gate_run = ["check-all"], `} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig := newBatchRig(t)
			rig.h.trees.late = &lateFolder{dir: t.TempDir()}
			rig.h.refuseGate = func(string, bool) error { return errNoGateHere }

			_, err := rig.walk(t, rig.call("1", extra))

			require.ErrorContains(t, err, errNoGateHere.Error())
			assert.Empty(t, rig.host.gitCalls(), "no git command runs")
			assert.Empty(t, rig.h.trees.late.path(), "no worktree is made")
			assert.Zero(t, rig.host.tipCalls, "the tip of the base is not read")
			assert.Empty(t, rig.host.checkouts)
			assert.Empty(t, rig.h.commands.ran(), "no program runs")
			assert.Empty(t, rig.gate.ran(), "no gate runs")
		})
	}
}

func TestGateRefusal_AGateRunsWhenNoGateIsRefused(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})
	h.gate.results = []GateResult{{Passed: true, Head: gatedHead, Tree: "tree-1", Steps: stepsPass}}
	h.refuseGate = func(string, bool) error { return nil }

	_, err := walkBody(t, h, "gate(name = \"first\")\n")

	require.NoError(t, err)
	assert.Equal(t, 1, h.gate.calls)
}

func TestGateRefusal_AReplayOfARecordedGateReturnsTheRecordedPassAfterTheSandboxTurnsOn(t *testing.T) {
	t.Parallel()

	for name, body := range map[string]string{
		"program gate": "gate(run = [\"true\"], name = \"links\")\n",
		"project gate": "gate(name = \"first\")\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var sandboxOn atomic.Bool

			h := mergeHarness(t, merge.Result{Stop: merge.Done})
			h.gate.results = []GateResult{{Passed: true, Head: gatedHead, Tree: "tree-1", Steps: stepsPass}}
			h.refuseGate = func(string, bool) error {
				if sandboxOn.Load() {
					return errNoGateHere
				}

				return nil
			}
			h.life.failing["release"] = true
			file := writeFlow(t, bodyFlow(body))
			engine := h.engine(t)

			// The release after the gate fails, so the run fails with the gate pass in its history.
			_, err := engine.RunFile(testItem, testProject, file)
			require.ErrorContains(t, err, "release")

			sandboxOn.Store(true)
			h.life.mu.Lock()
			h.life.failing["release"] = false
			h.life.mu.Unlock()

			_, err = resumeAndJoin(t, engine, h.client(t, testVersion))

			require.NoError(t, err, "the replay returns the recorded pass and is not refused")
		})
	}
}
