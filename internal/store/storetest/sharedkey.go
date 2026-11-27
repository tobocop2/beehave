package storetest

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/tobocop2/beehest/internal/model"
	"github.com/tobocop2/beehest/internal/proc"
	"github.com/tobocop2/beehest/internal/store"
)

// CreateWait is the wait that the tracker tests give CreateGate. Under the key lock the second writer looks the key up only after the first one filed, so the first create waits this long and then runs.
// With no lock it is the time the second writer has to reach its lookups, so a slow host does not let the first create run early.
const CreateWait = time.Second

// CreateGate is a runner over the fake tool of a tracker. It runs one call at a time, and it holds each create until `lookups` lookups ended or `wait` passed.
// Without a lock, two writers of one key then both look the key up before either files: the schedule that files two items.
// verb is the position of the subcommand in the arguments: 1 for gh issue create, 0 for bd create.
type CreateGate struct {
	inner   proc.Runner
	verb    int
	lookups int
	wait    time.Duration

	mu   sync.Mutex
	seen int
	open chan struct{}
}

// NewCreateGate returns a gate over inner that holds a create for at most `wait` until `lookups` lookups (calls of the subcommand list) ended.
func NewCreateGate(inner proc.Runner, verb, lookups int, wait time.Duration) *CreateGate {
	return &CreateGate{inner: inner, verb: verb, lookups: lookups, wait: wait, open: make(chan struct{})}
}

// Run runs one call of the fake tool.
func (g *CreateGate) Run(ctx context.Context, args []string) model.CommandResult {
	if g.is(args, "create") {
		select {
		case <-g.open:
		case <-time.After(g.wait):
		}
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	result := g.inner.Run(ctx, args)

	if g.is(args, "list") {
		g.seen++
		if g.seen == g.lookups {
			close(g.open)
		}
	}

	return result
}

func (g *CreateGate) is(args []string, verb string) bool {
	return len(args) > g.verb && args[g.verb] == verb
}

// CreateTogether runs Create with one key from both writers at the same time. Each writer holds store.LockKey of the key in dir around its Create when locked is true.
// It returns the id that each writer got.
func CreateTogether(r Reporter, dir string, writers [2]store.Writer, key string, locked bool) [2]string {
	r.Helper()

	var (
		wg   sync.WaitGroup
		ids  [2]string
		errs [2]error
	)

	for i, writer := range writers {
		wg.Go(func() { ids[i], errs[i] = createOne(dir, writer, key, locked) })
	}

	wg.Wait()

	if err := errors.Join(errs[:]...); err != nil {
		r.Errorf("Create of the key %q: %v", key, err)
	}

	return ids
}

func createOne(dir string, writer store.Writer, key string, locked bool) (id string, err error) {
	ctx := context.Background()
	item := store.NewItem{Title: "follow-up", Body: "findings"}

	if !locked {
		id, _, err = writer.Create(ctx, key, item)

		return id, err
	}

	unlock, err := store.LockKey(ctx, dir, key)
	if err != nil {
		return "", err
	}

	defer func() { err = errors.Join(err, unlock()) }()

	id, _, err = writer.Create(ctx, key, item)

	return id, err
}
