package flow

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// holdItems makes the children in held wait on the returned channel, or on the end of their run. Closing the channel lets them go on.
func holdItems(h *harness, held ...string) chan struct{} {
	release := make(chan struct{})

	h.commands.before = func(ctx context.Context, request CommandRequest) error {
		for _, item := range held {
			if request.Item != item {
				continue
			}

			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		return nil
	}

	return release
}

func TestGatherUntil_AnyReturnsWhenOneOfThreeEndedAndListsOnlyThatRun(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	release := holdItems(h, "21", "23")

	defer close(release)

	engine := h.engine(t)
	_, err := engine.RunFile(testItem, testProject, writeFlow(t, swarmFile(`until = "any", `)))

	require.NoError(t, err)
	assert.Equal(t, []string{"after 22:ok"}, h.commands.ranBy(testItem), "only the run that ended is listed")
	assert.JSONEq(t, `{"outcomes":[{"item":"22","ok":true}]}`, gatherJSON(t, engine))
}

func TestGatherUntil_AnyWithEveryRunEndedReturnsEveryRun(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	file := `def worker(item):
    command(name = "w", run = ["w"])

def main(item):
    runs = spawn(items = ["21", "22", "23"], entry = "worker", name = "fan")
    gather(runs, name = "all")
    done = gather(runs, until = "any", name = "fan")
    command(name = "after", run = ["after"] + [r.item + ":" + ("ok" if r.ok else r.error) for r in done.results])
`
	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, file))

	require.NoError(t, err)
	assert.Equal(t, []string{"after 21:ok 22:ok 23:ok"}, h.commands.ranBy(testItem))
}

func TestGatherUntil_TheDefaultAndAllWaitForEveryRun(t *testing.T) {
	t.Parallel()

	for name, args := range map[string]string{"default": "", "all": `until = "all", `} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			release := holdItems(h, "23")
			engine := h.engine(t)
			done := make(chan error, 1)
			file := writeFlow(t, swarmFile(args))

			go func() {
				_, err := engine.RunFile(testItem, testProject, file)
				done <- err
			}()

			require.True(t, waitUntil(func() bool { return itemEnded(engine, "21") && itemEnded(engine, "22") }), "the free runs end")
			time.Sleep(10 * tickInterval)
			assert.Empty(t, h.commands.ranBy(testItem), "the parent still waits for run 23")
			close(release)
			require.NoError(t, <-done)
			assert.Equal(t, []string{"after 21:ok 22:ok 23:ok"}, h.commands.ranBy(testItem))
		})
	}
}

// interruptedAtTheQuestion runs the parent until it asks its question, lets the held runs go and end, and then stops the engine. It differs from interrupted in that the status lists the child runs too.
func interruptedAtTheQuestion(t *testing.T, h *harness, file string, release chan struct{}) {
	t.Helper()

	stopCtx, stop := context.WithCancel(t.Context())
	engine, err := NewEngine(stopCtx, h.opts, h.deps())
	require.NoError(t, err)
	require.NoError(t, engine.Launch())

	client := h.client(t, testVersion)

	ended := make(chan error, 1)

	go func() {
		_, runErr := engine.RunFile(testItem, testProject, file)
		ended <- runErr
	}()

	eventually(t, func() bool {
		rows, statusErr := client.Status()

		return statusErr == nil && slices.ContainsFunc(rows, func(r RunInfo) bool { return r.Question != "" })
	})

	close(release)
	require.True(t, waitUntil(func() bool { return itemEnded(engine, "21") && itemEnded(engine, "23") }), "the held runs end after the parent asked")

	stop()
	require.NoError(t, engine.Shutdown())
}

func TestGatherUntil_AResumeOfTheParentReturnsTheRecordedOutcomesNotAFreshRead(t *testing.T) {
	t.Parallel()

	file := writeFlow(t, `def worker(item):
    command(name = "w", run = ["w"])

def main(item):
    runs = spawn(items = ["21", "22", "23"], entry = "worker", name = "fan")
    done = gather(runs, until = "any", name = "fan")
    wait(name = "ask", question = "go?")
    command(name = "after", run = ["after"] + [r.item + ":" + ("ok" if r.ok else r.error) for r in done.results])
`)
	h := newHarness(t)
	release := holdItems(h, "21", "23")

	interruptedAtTheQuestion(t, h, file, release)

	next := h.engine(t)
	client := h.client(t, testVersion)
	done := make(chan runOutcome, 1)

	go func() {
		result, err := next.RunFile(testItem, testProject, file)
		done <- runOutcome{result: result, err: err}
	}()

	eventually(t, func() bool { return client.Answer(testItem, "yes") == nil })

	require.NoError(t, (<-done).err)
	assert.Equal(t, []string{"after 22:ok"}, h.commands.ranBy(testItem), "the outcomes recorded before the stop come back, whatever the runs say now")
}

func TestGatherUntil_AnUnknownValueIsRefusedAndNamesTheChoices(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, bodyFlow("mode = \"soon\" + \"\"\ngather([], until = mode)")))

	require.ErrorContains(t, err, "`until` is \"soon\". Use \"all\" or \"any\"")
}
