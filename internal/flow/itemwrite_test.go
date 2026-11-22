package flow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	trackerstore "github.com/tobocop2/beehave/internal/store"
)

// fakeWriter plays a tracker writer. Like an adapter, it files one item for each key.
type fakeWriter struct {
	mu       sync.Mutex
	keys     map[string]string
	created  []string
	statuses []string
	notes    []string
	// assignees holds the assignee that each Create call carried, as key|assignee.
	assignees []string
	// severities holds the severity that each Create call carried, as key|severity.
	severities []string
	// operators holds the operator flag that each Create call carried, as key|flag.
	operators []string
	err       error
}

func newFakeWriter() *fakeWriter { return &fakeWriter{keys: map[string]string{}} }

func (w *fakeWriter) Create(_ context.Context, project, key string, item trackerstore.NewItem) (string, bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.err != nil {
		return "", false, w.err
	}

	if id, found := w.keys[key]; found {
		return id, false, nil
	}

	id := "n-" + strconv.Itoa(len(w.keys)+1)
	w.keys[key] = id
	w.created = append(w.created, project+"|"+key+"|"+item.Title+"|"+item.Body+"|"+item.Parent)
	w.assignees = append(w.assignees, key+"|"+item.Assignee)
	w.severities = append(w.severities, key+"|"+item.Severity)
	w.operators = append(w.operators, key+"|"+strconv.FormatBool(item.Operator))

	return id, true, nil
}

func (w *fakeWriter) SetStatus(_ context.Context, project, id string, status trackerstore.Status) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.statuses = append(w.statuses, project+"|"+id+"|"+string(status))

	return w.err
}

func (w *fakeWriter) Comment(_ context.Context, project, id, key, text string) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.notes = append(w.notes, project+"|"+id+"|"+key+"|"+text)

	return w.err
}

func TestItemWrite_CreateStatusAndCommentCallTheWriterWithTheRunScopedKey(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	w := newFakeWriter()
	h.writer = w

	_, err := walkBody(t, h, `made = item_create(title = "t", key = "plan", body = "b", parent = "p-1", name = "plan")
again = item_create(title = "t", key = "plan", name = "again")
item_comment(id = made.id, text = "hello", key = "note", name = "note")
closed = item_status(id = made.id, status = "closed", name = "close")
command(run = [made.id, str(made.created), str(again.created), again.id, closed.status])
`)
	require.NoError(t, err)

	assert.Equal(t, []string{testProject + "|" + testRun + ":plan|t|b|p-1"}, w.created)
	assert.Equal(t, []string{testProject + "|n-1|" + testRun + ":note|hello"}, w.notes)
	assert.Equal(t, []string{testProject + "|n-1|closed"}, w.statuses)
	assert.Equal(t, []string{"n-1 True False n-1 closed"}, h.commands.ran())
}

func TestItemWrite_ASharedKeyIsTheKeyOfTheCallInEveryRun(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	w := newFakeWriter()
	h.writer = w
	engine := h.engine(t)
	file := writeFlow(t, "def main(item):\n    made = item_create(title = \"t\", key = \"o/r:41\", shared = True)\n    command(run = [made.id, str(made.created)])\n")

	for range 2 {
		_, err := engine.RunFile(testItem, testProject, file)
		require.NoError(t, err)
	}

	assert.Equal(t, []string{testProject + "|o/r:41|t||"}, w.created, "two runs give the same key, so the tracker files one item")
	assert.Equal(t, []string{"n-1 True", "n-1 False"}, h.commands.ran(), "the second run gets the first item")
}

// lockProbeWriter is a fakeWriter that, inside each Create, tries the key lock of the project and the key in the state folder dir.
// held records whether another holder had the lock, as project|key|held.
type lockProbeWriter struct {
	*fakeWriter

	dir  string
	held []string
}

func (w *lockProbeWriter) Create(ctx context.Context, project, key string, item trackerstore.NewItem) (string, bool, error) {
	short, cancel := context.WithTimeout(ctx, 60*time.Millisecond)
	defer cancel()

	unlock, err := trackerstore.LockKey(short, w.dir, project+"\n"+key)
	if err != nil {
		w.held = append(w.held, project+"|"+key+"|held")

		return w.fakeWriter.Create(ctx, project, key, item)
	}

	if err := unlock(); err != nil {
		return "", false, err
	}

	return w.fakeWriter.Create(ctx, project, key, item)
}

func TestItemWrite_ASharedCreateHoldsTheLockOfItsKeyAndARunScopedCreateDoesNot(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	w := &lockProbeWriter{fakeWriter: newFakeWriter()}
	h.writer = w
	engine := h.engine(t)
	w.dir = engine.dir

	_, err := engine.RunFile(testItem, testProject, writeFlow(t, "def main(item):\n    item_create(title = \"t\", key = \"o/r:41\", shared = True)\n    item_create(title = \"t\", key = \"plan\")\n"))
	require.NoError(t, err)

	assert.Equal(t, []string{testProject + "|o/r:41|held"}, w.held, "only the shared create holds the lock of its key")
	assert.Len(t, w.created, 2)
}

func TestItemWrite_ASharedCreateWhoseLockCannotBeTakenFailsAndFilesNothing(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	w := newFakeWriter()
	h.writer = w
	engine := h.engine(t)

	sum := sha256.Sum256([]byte(testProject + "\no/r:41"))
	require.NoError(t, os.Mkdir(filepath.Join(engine.dir, "item-"+hex.EncodeToString(sum[:16])+".lock"), 0o700), "a folder in place of the lock file")

	_, err := engine.RunFile(testItem, testProject, writeFlow(t, "def main(item):\n    item_create(title = \"t\", key = \"o/r:41\", shared = True)\n"))

	require.ErrorContains(t, err, "take the lock of a shared item key")
	assert.Empty(t, w.created)
}

func TestItemWrite_AReplayWritesNothingTwiceAndKeepsTheStepNames(t *testing.T) {
	t.Parallel()

	const replayFlow = `def main(item):
    made = item_create(title = "child", key = "c1", name = "c1")
    answer = wait(name = "ask", question = "go?").answer
    item_comment(id = made.id, text = answer, key = "n1", name = "n1")
    item_status(id = made.id, status = "hold", name = "hold")
`

	h := newHarness(t)
	w := newFakeWriter()
	h.writer = w
	file := writeFlow(t, replayFlow)
	interrupted(t, h, file)

	next := h.engine(t)
	client := h.client(t, testVersion)
	done := make(chan runOutcome, 1)

	go func() {
		result, err := next.RunFile(testItem, testProject, file)
		done <- runOutcome{result: result, err: err}
	}()

	waitOn(t, waiting[struct{}]{what: "the run to take the answer", run: runOfClient(t, client), read: func() (struct{}, bool, error) {
		return struct{}{}, client.Reply(testItem, Reply{Text: "yes"}) == nil, nil
	}})
	require.NoError(t, receive(t, done).err)

	assert.Len(t, w.created, 1, "the create before the wait ran once")
	assert.Equal(t, []string{testProject + "|n-1|" + testRun + ":n1|yes"}, w.notes)
	assert.Equal(t, []string{"item_create:c1#1", "wait:ask#1", "item_comment:n1#1", "item_status:hold#1"}, stepNames(t, next))
}

func TestItemWrite_ACrashBetweenEachWriteAndItsRecordRepeatsTheCallWithTheSameKey(t *testing.T) {
	t.Parallel()

	tests := map[string]struct{ call, method, want string }{
		"item_create":  {`item_create(title = "t", key = "k", name = "c")`, "create", "create|" + testProject + "|" + testRun + ":k|t"},
		"item_comment": {`item_comment(id = "n-1", text = "hi", key = "k", name = "c")`, "comment", "comment|" + testProject + "|n-1|" + testRun + ":k|hi"},
		"item_status":  {`item_status(id = "n-1", status = "closed", name = "c")`, "status", "status|" + testProject + "|n-1|closed"},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			w := &crashWriter{writer: newFakeWriter(), crash: newCrash(t, h), on: tt.method}
			h.writer = w

			crashAndResume(t, h, h.deps(), tt.call+"\n")

			assert.Equal(t, []string{tt.want, tt.want}, w.calls, "the rerun reached the tracker with the key and the arguments of the first call")
		})
	}
}

func TestItemWrite_ABadStatusFailsBeforeAnyWrite(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	w := newFakeWriter()
	h.writer = w

	_, err := walkBody(t, h, "item_status(id = \"1\", status = \"in_\" + \"progress\")\n")

	require.ErrorContains(t, err, `status "in_progress" is not known. Use "open", "closed" or "hold"`)
	assert.Empty(t, w.statuses)
}

func TestItemWrite_ARefusedArgumentFailsWithThePositionAndWritesNothing(t *testing.T) {
	t.Parallel()

	for name, call := range map[string]string{
		"create without a title":         `item_create(title = "", key = "k")`,
		"create without a key":           `item_create(title = "t", key = "")`,
		"status without an id":           `item_status(id = "", status = "open")`,
		"comment without a key":          `item_comment(id = "1", text = "x", key = "")`,
		"comment without a text":         `item_comment(id = "1", text = "", key = "k")`,
		"unknown keyword":                `item_create(title = "t", key = "k", limit = 3)`,
		"status without status":          `item_status(id = "1")`,
		"comment with no arguments left": `item_comment(id = "1")`,
	} {
		h := newHarness(t)
		w := newFakeWriter()
		h.writer = w

		_, err := walkBody(t, h, call+"\n")
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), "workflow.star:", name)
		assert.Empty(t, w.created, name)
		assert.Empty(t, w.notes, name)
		assert.Empty(t, w.statuses, name)
	}
}

func TestItemWrite_ABuildWithNoWriterFailsAndNamesTheFix(t *testing.T) {
	t.Parallel()

	for _, call := range []string{`item_create(title = "t", key = "k")`, `item_status(id = "1", status = "open")`, `item_comment(id = "1", text = "x", key = "k")`} {
		_, err := walkBody(t, newHarness(t), call+"\n")
		require.ErrorContains(t, err, "this build cannot write to a tracker", call)
	}
}

func TestItemWrite_AWriterErrorFailsTheCallAndPassesTheRedactor(t *testing.T) {
	t.Parallel()

	for _, call := range []string{`item_create(title = "t", key = "k")`, `item_status(id = "1", status = "open")`, `item_comment(id = "1", text = "x", key = "k")`} {
		h := newHarness(t)
		w := newFakeWriter()
		w.err = errors.New("the tracker beads cannot write: boom")
		h.writer = w

		_, err := walkBody(t, h, call+"\n")

		require.ErrorContains(t, err, "the tracker beads cannot write: boom", call)
	}
}
