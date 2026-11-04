package flow

import (
	"cmp"
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeReader plays a tracker reader: the comments and the parent of each item.
type fakeReader struct {
	mu       sync.Mutex
	comments map[string][]string
	parents  map[string]string
	calls    []string
	// err fails both reads. commentsErr and parentErr fail one of them.
	err         error
	commentsErr error
	parentErr   error
}

func (r *fakeReader) Comments(_ context.Context, project, id string) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.calls = append(r.calls, "comments|"+project+"|"+id)

	return r.comments[id], cmp.Or(r.err, r.commentsErr)
}

func (r *fakeReader) Parent(_ context.Context, project, id string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.calls = append(r.calls, "parent|"+project+"|"+id)

	return r.parents[id], cmp.Or(r.err, r.parentErr)
}

// reads counts the calls of one kind of read, "comments" or "parent".
func (r *fakeReader) reads(kind string) int {
	r.mu.Lock()
	defer r.mu.Unlock()

	count := 0

	for _, call := range r.calls {
		if strings.HasPrefix(call, kind+"|") {
			count++
		}
	}

	return count
}

func (r *fakeReader) say(id string, comments ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.comments[id] = comments
}

func readerHarness(t *testing.T) (*harness, *fakeReader) {
	t.Helper()

	h := newHarness(t)
	r := &fakeReader{
		comments: map[string][]string{testItem: {"first", "second"}, "p-1": {"epic note"}},
		parents:  map[string]string{testItem: "p-1"},
	}
	h.reader = r
	h.items.byItem = map[string]map[string]string{"p-1": {"id": "p-1", "title": "the epic", "body": "epic body"}}

	return h, r
}

func TestItemRead_ReadsTheFieldsTheCommentsAndTheParentOfTheOwnItem(t *testing.T) {
	t.Parallel()

	h, _ := readerHarness(t)

	_, err := walkBody(t, h, `r = item_read(name = "own")
command(run = [r.id, r.fields["title"], r.comments[1], r.parent, str(r.found), str(len(r.comments))])
`)
	require.NoError(t, err)
	assert.Equal(t, []string{testItem + " a title second p-1 True 2"}, h.commands.ran())
}

func TestItemRead_OfParentReadsTheParentItemAndItsOwnParent(t *testing.T) {
	t.Parallel()

	h, _ := readerHarness(t)

	_, err := walkBody(t, h, `r = item_read(of = "parent", name = "up")
command(run = [r.id, r.fields["title"], r.fields["body"], r.comments[0], "[" + r.parent + "]", str(r.found)])
`)
	require.NoError(t, err)
	assert.Equal(t, []string{"p-1 the epic epic body epic note [] True"}, h.commands.ran())
}

func TestItemRead_AnItemWithNoParentGivesNotFoundAndEmptyFields(t *testing.T) {
	t.Parallel()

	h, r := readerHarness(t)
	delete(r.parents, testItem)

	_, err := walkBody(t, h, `r = item_read(of = "parent", name = "up")
command(run = [str(r.found), "[" + r.id + "]", str(len(r.fields)), str(len(r.comments))])
`)
	require.NoError(t, err)
	assert.Equal(t, []string{"False [] 0 0"}, h.commands.ran())
}

func TestItemRead_ATrackerWithNoReaderRefusesTheStepAndSaysSo(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	_, err := walkBody(t, h, `item_read(name = "own")
`)
	require.ErrorContains(t, err, "cannot read the comments or the parent of an item")
	require.ErrorContains(t, err, "Use a tracker that has a reader")
}

func TestItemRead_AFailedReadFailsTheStep(t *testing.T) {
	t.Parallel()

	h, r := readerHarness(t)
	r.err = errFake

	_, err := walkBody(t, h, `item_read(name = "own")
`)
	require.ErrorIs(t, err, errFake)
}

func TestItemRead_AFailureOfEachReadNamesTheItemAndTheRead(t *testing.T) {
	t.Parallel()

	h, _ := readerHarness(t)
	h.items.failFor = "p-1"

	_, err := walkBody(t, h, `item_read(of = "parent")
`)
	require.ErrorContains(t, err, "read the fields of item p-1")

	h, r := readerHarness(t)
	r.parentErr = errFake

	_, err = walkBody(t, h, `item_read()
`)
	require.ErrorContains(t, err, "read the parent of item "+testItem)

	h, r = readerHarness(t)
	r.commentsErr = errFake

	_, err = walkBody(t, h, `item_read()
`)
	require.ErrorContains(t, err, "read the comments of item "+testItem)
}

func TestItemRead_ABadTargetIsRefusedBeforeTheStep(t *testing.T) {
	t.Parallel()

	h, r := readerHarness(t)

	_, err := walkBody(t, h, `item_read(of = "sibling")
`)
	require.ErrorContains(t, err, "Use one of: self, parent")
	assert.Empty(t, r.calls)
}

func TestItemRead_ATargetTheCheckCannotReadIsRefusedWhenTheStepStarts(t *testing.T) {
	t.Parallel()

	h, r := readerHarness(t)

	_, err := walkBody(t, h, `item_read(of = "sib" + "ling")
`)
	require.ErrorContains(t, err, "`of` is \"sibling\". Use one of: self, parent", "a value the check cannot read is refused when the step starts")
	assert.Empty(t, r.calls)
}

func TestItemRead_AReplayReadsTheRecordedAnswerAndNotTheTrackerAgain(t *testing.T) {
	t.Parallel()

	const replayFlow = `def main(item):
    before = item_read(name = "before")
    wait(name = "ask", question = "go?")
    command(run = [before.comments[0]], name = "show")
`

	h, r := readerHarness(t)
	file := writeFlow(t, replayFlow)
	interrupted(t, h, file)
	require.Equal(t, 1, r.reads("comments"), "the first start reads the tracker once")

	r.say(testItem, "changed after the first start")

	next := h.engine(t)
	client := h.client(t, testVersion)
	done := make(chan runOutcome, 1)

	go func() {
		result, err := next.RunFile(testItem, testProject, file)
		done <- runOutcome{result: result, err: err}
	}()

	eventually(t, func() bool { return client.Answer(testItem, "yes") == nil })
	require.NoError(t, (<-done).err)

	assert.Equal(t, 1, r.reads("comments"), "the replay does not read the comments again")
	assert.Equal(t, 1, r.reads("parent"), "the replay does not read the parent again")
	assert.Equal(t, []string{"first"}, h.commands.ran(), "the replay keeps the comment that the first start read")
	assert.Equal(t, []string{"item_read:before#1", "wait:ask#1", "command:show#1"}, stepNames(t, next))
}
