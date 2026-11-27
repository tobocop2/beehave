package storetest_test

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/model"
	"github.com/tobocop2/beehest/internal/store"
	"github.com/tobocop2/beehest/internal/store/storetest"
)

// toolTracker is a tracker tool with two subcommands: `list <key>` prints the id that holds the key, and `create <key>` files an item and prints its id.
// It is not safe for concurrent calls; the gate runs one call at a time.
type toolTracker struct {
	keys  map[string]string
	items int
}

func (f *toolTracker) Run(_ context.Context, args []string) model.CommandResult {
	if args[0] == "create" {
		f.items++
		f.keys[args[1]] = strconv.Itoa(f.items)
	}

	return model.CommandResult{Stdout: f.keys[args[1]]}
}

// toolWriter looks the key up with list and files with create, in two calls, as the github and beads writers do.
type toolWriter struct {
	store.Writer

	run *storetest.CreateGate
}

func (w toolWriter) Create(ctx context.Context, key string, _ store.NewItem) (string, bool, error) {
	if id := w.run.Run(ctx, []string{"list", key}).Stdout; id != "" {
		return id, false, nil
	}

	return w.run.Run(ctx, []string{"create", key}).Stdout, true, nil
}

func twoToolWriters() (*toolTracker, [2]store.Writer) {
	tool := &toolTracker{keys: map[string]string{}}
	gate := storetest.NewCreateGate(tool, 0, 2, storetest.CreateWait)

	return tool, [2]store.Writer{toolWriter{run: gate}, toolWriter{run: gate}}
}

// runCreate runs a create through the gate and fails the test if the create is still held after a few seconds, far below the wait of the gate.
func runCreate(t *testing.T, gate *storetest.CreateGate, args []string) {
	t.Helper()

	done := make(chan struct{})

	go func() {
		gate.Run(t.Context(), args)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the gate held the create after the lookups it waits for")
	}
}

func TestCreateGate_ACreateRunsAtOnceWhenTheLookupsEnded(t *testing.T) {
	t.Parallel()

	tool := &toolTracker{keys: map[string]string{}}
	gate := storetest.NewCreateGate(tool, 0, 2, time.Hour)

	gate.Run(t.Context(), []string{"list", "k"})
	gate.Run(t.Context(), []string{"list", "k"})
	runCreate(t, gate, []string{"create", "k"})

	assert.Equal(t, 1, tool.items)
}

func TestCreateGate_ACallShorterThanTheSubcommandPositionPassesToTheTool(t *testing.T) {
	t.Parallel()

	var calls [][]string

	inner := runnerFunc(func(args []string) { calls = append(calls, args) })
	gate := storetest.NewCreateGate(inner, 1, 1, time.Hour)

	gate.Run(t.Context(), []string{"issue"})
	gate.Run(t.Context(), []string{"list"})
	gate.Run(t.Context(), []string{"issue", "list"})
	runCreate(t, gate, []string{"issue", "create"})

	assert.Len(t, calls, 4, "the gate passes every call to the tool")
}

// runnerFunc is a runner that records each call and prints nothing.
type runnerFunc func(args []string)

func (f runnerFunc) Run(_ context.Context, args []string) model.CommandResult {
	f(args)

	return model.CommandResult{}
}

func TestCreateTogether_TheGateFilesTwoItemsWithNoLockAndOneUnderTheKeyLock(t *testing.T) {
	t.Parallel()

	tool, writers := twoToolWriters()
	storetest.CreateTogether(t, t.TempDir(), writers, "k", false)
	assert.Equal(t, 2, tool.items, "both lookups end before either create")

	tool, writers = twoToolWriters()
	ids := storetest.CreateTogether(t, t.TempDir(), writers, "k", true)
	assert.Equal(t, 1, tool.items, "the second lookup runs after the first create")
	assert.Equal(t, [2]string{"1", "1"}, ids)
}

func TestCreateTogether_ALockThatCannotBeTakenIsReportedAndFilesNothing(t *testing.T) {
	t.Parallel()

	file := filepath.Join(t.TempDir(), "not-a-folder")
	require.NoError(t, os.WriteFile(file, nil, 0o600))

	tool, writers := twoToolWriters()
	r := &recorder{}
	storetest.CreateTogether(r, file, writers, "k", true)

	require.Len(t, r.failures, 1)
	assert.Contains(t, r.failures[0], `Create of the key "k"`)
	assert.Zero(t, tool.items)
}
