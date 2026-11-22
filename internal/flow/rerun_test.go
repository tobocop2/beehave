package flow

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	trackerstore "github.com/tobocop2/beehave/internal/store"
)

// crashOnce stops the run of the test item the first time after is called. An effect calls it right after it applied itself and before its step is recorded,
// which is where a crash of the engine leaves a run.
type crashOnce struct{ after func() }

// newCrash makes a crash that cancels the run of the test item and waits for the tick that ends it.
func newCrash(t *testing.T, h *harness) *crashOnce {
	t.Helper()

	return &crashOnce{after: sync.OnceFunc(func() {
		if err := h.client(t, testVersion).Cancel(testItem); err != nil {
			t.Error(err)
		}

		h.tick(t)
	})}
}

// crashAndResume runs the body until the crash ends the run, then resumes the run and waits for it to finish. The step that the crash cut off has no record, so the resume runs it again.
func crashAndResume(t *testing.T, h *harness, deps Deps, body string) ItemResult {
	t.Helper()

	engine, err := NewEngine(t.Context(), h.opts, deps)
	require.NoError(t, err)
	require.NoError(t, engine.Launch())
	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	_, err = engine.RunFile(testItem, testProject, writeFlow(t, bodyFlow(body)), modeOff())
	require.Error(t, err, "the crash ends the first run")

	result, err := resumeAndJoin(t, engine, h.client(t, testVersion))
	require.NoError(t, err)

	return result
}

// spawnFlow starts one child run of item 21 and waits for it. The child runs one command.
const spawnFlow = `def worker(item):
    command(name = "w", run = ["w"])

def main(item):
    gather(spawn(items = ["21"], entry = "worker", name = "fan"), name = "fan")
`

// The parent stops while it waits in gather, after spawn returned: the first command of the child cancels the parent. The resume replays spawn, and the
// library returns the recorded child for its run id, so no second run of the child starts. No crash inside the start call is tested.
func TestSpawn_ACrashWhileTheParentWaitsInGatherStartsNoSecondChild(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	crash := newCrash(t, h)
	h.commands.before = func(_ context.Context, request CommandRequest) error {
		if request.Item == "21" {
			crash.after()
		}

		return nil
	}

	engine := h.engine(t)

	_, err := engine.RunFile(testItem, testProject, writeFlow(t, spawnFlow), modeOff())
	require.Error(t, err, "the crash ends the parent")

	before, found, err := latestRun(engine.ctx, "21")
	require.NoError(t, err)
	require.True(t, found, "the child started before the crash")

	_, err = resumeAndJoin(t, engine, h.client(t, testVersion))
	require.NoError(t, err)

	after, found, err := latestRun(engine.ctx, "21")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, before.Status.ID, after.Status.ID, "the resume found the child by its run id")
	assert.Equal(t, 1, after.Attempt, "no second attempt of the child started")
	assert.Equal(t, []string{"w"}, h.commands.ran(), "the child ran its command once")
}

// crashWriter logs each call to the tracker writer as method|arguments, and crashes the run after the first call of the method in on.
// The fake writer under it keeps its own record of what the tracker holds.
type crashWriter struct {
	writer

	crash *crashOnce
	on    string

	mu    sync.Mutex
	calls []string
}

func (w *crashWriter) note(method, call string) {
	w.mu.Lock()
	w.calls = append(w.calls, method+"|"+call)
	w.mu.Unlock()

	if method == w.on {
		w.crash.after()
	}
}

func (w *crashWriter) Create(ctx context.Context, project, key string, item trackerstore.NewItem) (string, bool, error) {
	id, created, err := w.writer.Create(ctx, project, key, item)
	w.note("create", project+"|"+key+"|"+item.Title)

	return id, created, err
}

func (w *crashWriter) SetStatus(ctx context.Context, project, id string, status trackerstore.Status) error {
	err := w.writer.SetStatus(ctx, project, id, status)
	w.note("status", project+"|"+id+"|"+string(status))

	return err
}

func (w *crashWriter) Comment(ctx context.Context, project, id, key, text string) error {
	err := w.writer.Comment(ctx, project, id, key, text)
	w.note("comment", project+"|"+id+"|"+key+"|"+text)

	return err
}

// crashForge crashes the run after the first call of the forge method in on. The fake forge under it keeps the log of each call.
type crashForge struct {
	forge

	crash *crashOnce
	on    string
}

func (f *crashForge) Comment(ctx context.Context, project, item, key, text string) (int, error) {
	number, err := f.forge.Comment(ctx, project, item, key, text)
	f.stop("comment")

	return number, err
}

func (f *crashForge) Open(ctx context.Context, project, item string, req OpenRequest) (OpenResult, error) {
	opened, err := f.forge.Open(ctx, project, item, req)
	f.stop("open")

	return opened, err
}

func (f *crashForge) Push(ctx context.Context, project, item, branch, head, expect string) error {
	err := f.forge.Push(ctx, project, item, branch, head, expect)
	f.stop("push")

	return err
}

func (f *crashForge) stop(method string) {
	if method == f.on {
		f.crash.after()
	}
}
