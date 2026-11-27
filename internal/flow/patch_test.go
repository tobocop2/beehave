package flow

import (
	"context"
	"slices"
	"sync"
	"testing"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.starlark.net/starlark"

	"github.com/tobocop2/beehest/internal/step"
)

// probeFlow calls now, waits for the owner, then calls now again. The tests change the builtin now between two builds.
const probeFlow = "def main(item):\n    now()\n    wait(name = \"ask\", question = \"What now?\")\n    now()\n"

// probes collects the value that each call of now returned, in call order.
type probes struct {
	mu     sync.Mutex
	values []string
}

func (p *probes) add(value string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.values = append(p.values, value)
}

func (p *probes) all() []string {
	p.mu.Lock()
	defer p.mu.Unlock()

	return append([]string(nil), p.values...)
}

// probeModules are the modules of one build: the engine module with the builtin now replaced by call.
func probeModules(call func(s *script) (string, error), seen *probes) []step.Module {
	engine := StepModule()
	engine.Builtins = slices.DeleteFunc(engine.Builtins, func(b step.Builtin) bool { return b.Name == "now" })
	engine.Builtins = append(engine.Builtins, step.Builtin{
		Name: "now",
		Call: func(c step.Call, _ *starlark.Builtin, _ starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
			s, ok := c.(*script)
			if !ok {
				return nil, errFake
			}

			value, err := call(s)
			if err == nil {
				seen.add(value)
			}

			return starlark.String(value), err
		},
	})

	return []step.Module{engine}
}

// oldProbe is the build before the change: now records one step and returns old.
func oldProbe(s *script) (string, error) {
	return countedStep(s, "now", "", func(context.Context) (string, error) { return "old", nil })
}

// newProbeWithNoPatch is the changed build without a patch: now records a second step before its own and returns new.
func newProbeWithNoPatch(s *script) (string, error) {
	return changedProbe(s)
}

func changedProbe(s *script) (string, error) {
	if _, err := countedStep(s, "now", "check", func(context.Context) (string, error) { return "check", nil }); err != nil {
		return "", err
	}

	return countedStep(s, "now", "", func(context.Context) (string, error) { return "new", nil })
}

// newProbeBehindPatch is the changed build behind a DBOS patch: a run that already passed this point keeps the old steps.
func newProbeBehindPatch(s *script) (string, error) {
	patched, err := dbos.Patch(s.ctx, "now-checks-first")
	if err != nil {
		return "", err
	}

	if !patched {
		return oldProbe(s)
	}

	return changedProbe(s)
}

// continueOnBuild stops the first engine of the harness at the question of the owner, starts an engine with the modules of the next build
// over the same store and the same application version, answers the question when answer is true, and returns the outcome of the run.
// A run that the replay refuses ends before it asks again, so its test passes answer false.
func continueOnBuild(t *testing.T, h *harness, first, next []step.Module, answer bool) runOutcome {
	t.Helper()

	h.opts.Modules = first
	file := writeFlow(t, probeFlow)
	interrupted(t, h, file)

	h.opts.Modules = next
	engine := h.engine(t)
	client := h.client(t, testVersion)
	done := make(chan runOutcome, 1)

	go func() {
		result, err := engine.RunFile(testItem, testProject, file, modeOff())
		done <- runOutcome{result: result, err: err}
	}()

	if answer {
		waitOn(t, waiting[struct{}]{what: "the open run to take the answer", run: runOfClient(t, client), read: func() (struct{}, bool, error) {
			return struct{}{}, client.Reply(testItem, Reply{Text: "yes"}) == nil, nil
		}})
	}

	return receive(t, done)
}

func TestPatch_ARunThatPassedTheChangedBuiltinKeepsTheOldStepsAndALaterCallTakesTheNewOnes(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	seen := &probes{}

	outcome := continueOnBuild(t, h, probeModules(oldProbe, seen), probeModules(newProbeBehindPatch, seen), true)

	require.NoError(t, outcome.err, "the run that started on the old build finishes on the new build with the same application version")
	assert.Equal(t, []string{"old", "old", "new"}, seen.all(),
		"call 1 ran on the old build, its replay on the new build answers old from the record, and call 2 is new code")
}

func TestPatch_ARunThatStartsOnTheNewBuildTakesTheNewStepsAtEveryCall(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	seen := &probes{}

	outcome := continueOnBuild(t, h, probeModules(newProbeBehindPatch, seen), probeModules(newProbeBehindPatch, seen), true)

	require.NoError(t, outcome.err)
	assert.Equal(t, []string{"new", "new", "new"}, seen.all(), "call 1 is new, its replay answers new from the patch record, call 2 is new")

	names, err := h.client(t, testVersion).stepNames(testRun)
	require.NoError(t, err)
	assert.Equal(t, 2, markers(names), "each of the two calls recorded one marker, and the replay of call 1 recorded no third")
	assert.Equal(t, slices.Index(names, "now:check#1")-1, slices.Index(names, patchMarker), "the marker of call 1 is the step right before the first step of the call")
}

// patchMarker is the step that DBOS records for the patch of newProbeBehindPatch.
const patchMarker = "DBOS.patch-now-checks-first"

// markers counts the steps of the patch marker in the recorded step names.
func markers(names []string) int {
	return len(names) - len(slices.DeleteFunc(slices.Clone(names), func(name string) bool { return name == patchMarker }))
}

func TestPatch_AChangedBuiltinWithNoPatchRefusesTheReplayOfARunThatPassedIt(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	seen := &probes{}

	outcome := continueOnBuild(t, h, probeModules(oldProbe, seen), probeModules(newProbeWithNoPatch, seen), false)

	require.Error(t, outcome.err, "the replay of call 1 expects the extra step first and finds the old one")
	t.Logf("the refusal of the base: %v", outcome.err)
	assert.Equal(t, []string{"old"}, seen.all(), "the new build never finishes a call")
}

func TestPatch_ARunMovesToANewReleaseBuildWithNoModuleBumpAndKeepsTheOldSteps(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	seen := &probes{}

	h.opts.Modules = probeModules(oldProbe, seen)
	file := writeFlow(t, probeFlow)
	interrupted(t, h, file)
	expireLease(h) // the holder of the run is gone: its lease in the store has ended

	h.opts.Modules = probeModules(newProbeBehindPatch, seen)
	h.opts.Version = otherBuild
	engine := h.engine(t)
	client := h.client(t, otherBuild)

	last, found, err := latestRun(viewOf(engine.ctx, engine.dir), testItem)
	require.NoError(t, err)
	require.True(t, found)
	require.NotEqual(t, client.version, last.Status.ApplicationVersion, "the run is stored under the old release build")

	require.NoError(t, client.ResumeOnThisBuild(testItem), "the module versions are equal, so the move is allowed, and the patch marker of a new run is a step of the engine")

	handle, err := dbos.RetrieveWorkflow[ItemResult](engine.ctx, last.Status.ID)
	require.NoError(t, err)

	waitOn(t, waiting[struct{}]{what: "the moved run to take the answer", run: runOfClient(t, client), read: func() (struct{}, bool, error) {
		return struct{}{}, client.Reply(testItem, Reply{Text: "yes"}) == nil, nil
	}})

	_, err = resultOfHandle(t, handle)

	require.NoError(t, err)
	assert.Equal(t, []string{"old", "old", "new"}, seen.all())
}
