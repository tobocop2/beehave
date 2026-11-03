package flow

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	trackerstore "github.com/tobocop2/obee/internal/store"
)

// fakeWriter plays a tracker writer. Like an adapter, it files one item for each key.
type fakeWriter struct {
	mu       sync.Mutex
	keys     map[string]string
	created  []string
	statuses []string
	notes    []string
	// assigned holds each Assign call. assignees holds the assignee that each Create call carried, as key|assignee.
	assigned  []string
	assignees []string
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

	return id, true, nil
}

func (w *fakeWriter) Assign(_ context.Context, project, id, assignee string) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.assigned = append(w.assigned, project+"|"+id+"|"+assignee)

	return w.err
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

	eventually(t, func() bool { return client.Answer(testItem, "yes") == nil })
	require.NoError(t, (<-done).err)

	assert.Len(t, w.created, 1, "the create before the wait ran once")
	assert.Equal(t, []string{testProject + "|n-1|" + testRun + ":n1|yes"}, w.notes)
	assert.Equal(t, []string{"item_create:c1#1", "wait:ask#1", "item_comment:n1#1", "item_status:hold#1"}, stepNames(t, next))
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
