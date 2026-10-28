package flow

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
)

const (
	questionKey  = "question"
	tickInterval = 2 * time.Second
)

// openRun is a run this process executes: the cancel function of its context, the channel that closes when its workflow function returns, and its input.
type openRun struct {
	id     string
	cancel context.CancelFunc
	ended  chan struct{}
	in     ItemInput
}

// cancels holds the runs this process executes, by run id.
type cancels struct {
	mu   sync.Mutex
	runs map[string]openRun
}

func (c *cancels) add(id string, open openRun) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.runs == nil {
		c.runs = map[string]openRun{}
	}

	c.runs[id] = open
}

// ids lists the runs this process executes.
func (c *cancels) ids() []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	ids := make([]string, 0, len(c.runs))
	for id := range c.runs {
		ids = append(ids, id)
	}

	return ids
}

// endedIDs lists the runs whose workflow function has returned.
func (c *cancels) endedIDs() []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	var ids []string

	for id, open := range c.runs {
		select {
		case <-open.ended:
			ids = append(ids, id)
		default:
		}
	}

	return ids
}

// forget drops the runs in ids.
func (c *cancels) forget(ids []string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, id := range ids {
		delete(c.runs, id)
	}
}

// cancel calls the cancel function of each run in ids that this process executes and forgets those runs.
// It returns them, so the caller releases each one once.
func (c *cancels) cancel(ids []string) []openRun {
	c.mu.Lock()
	defer c.mu.Unlock()

	var cut []openRun

	for _, id := range ids {
		if open, found := c.runs[id]; found {
			open.cancel()
			delete(c.runs, id)

			cut = append(cut, open)
		}
	}

	return cut
}

// track runs body with a context that the tick can cancel. The run stays in the list of local runs until a tick drops it.
func (e *Engine) track(parent dbos.Context, in ItemInput, body func(dbos.Context) (ItemResult, error)) (ItemResult, error) {
	runCtx, cancel := dbos.WithCancel(parent)
	defer cancel()

	if id, err := dbos.GetWorkflowID(parent); err == nil {
		ended := make(chan struct{})
		e.cancels.add(id, openRun{id: id, cancel: cancel, ended: ended, in: in})

		defer close(ended)
	}

	return body(runCtx)
}

// tick lists the canceled runs once for each interval and cancels the ones this process executes. It renews the held claims on the same beat.
// A cancel from another process marks the run in the store only; the tick wakes the owner wait of the local copy.
func (e *Engine) tick(interval time.Duration, stop <-chan struct{}) {
	ticks := e.deps.Clock.Tick
	if ticks == nil {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		ticks = ticker.C
	}

	for {
		select {
		case <-stop:
			return
		case <-ticks:
			e.cancelCancelled(stop)
			e.beatClaims()
		}
	}
}

// cancelCancelled cancels each local run that the store marks as canceled. The workflow cannot run its own release then,
// so the engine gives the item back once the run has ended and its agent groups are dead.
// A run stays listed after it ends: DBOS can end a canceled run before the tick lists it.
// The tick drops the runs that had ended before it listed, as the store marks a cancel before the run ends.
func (e *Engine) cancelCancelled(stop <-chan struct{}) {
	ended := e.cancels.endedIDs()

	ids, err := e.canceledIDs(e.cancels.ids())
	if err != nil {
		e.logger.Warn("cannot list the canceled runs", "error", err)

		return
	}

	cut := e.cancels.cancel(ids)

	e.cancels.forget(ended)

	// The give-back waits for the run to end and calls the tracker, so it runs beside the tick: a slow release delays no beat and no scan.
	for _, open := range cut {
		e.tickRun.Go(func() { e.giveBackWhenEnded(open, stop) })
	}
}

// canceledIDs lists which of the local runs the store marks as canceled. It reads no other run, so the cost of a tick does not grow with the history of the store.
func (e *Engine) canceledIDs(local []string) ([]string, error) {
	if len(local) == 0 {
		return nil, nil
	}

	rows, err := dbos.ListWorkflows(e.ctx, dbos.WithFilterStatus(dbos.WorkflowStatusCancelled), dbos.WithFilterWorkflowIDs(local...))
	if err != nil {
		return nil, err
	}

	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}

	return ids, nil
}

// giveBackWhenEnded waits until the workflow function of the run has returned, then gives the item back. A stop of the tick ends the wait.
// A run that is canceled and not released at the stop keeps its cancel marker, and the next start gives it back.
func (e *Engine) giveBackWhenEnded(open openRun, stop <-chan struct{}) {
	select {
	case <-open.ended:
		e.giveBack(context.WithoutCancel(e.ctx), open)
	case <-stop:
	}
}

// giveBack kills the agent groups of a canceled item, removes its worktree and releases its claim with the token the run holds now.
func (e *Engine) giveBack(ctx context.Context, open openRun) {
	if err := e.procs.releaseItem(open.in.Item); err != nil {
		e.logger.WarnContext(ctx, "cannot update the agent process file", "error", err)
	}

	e.giveBackClaim(ctx, open)
}

// giveBackClaim removes the worktree of a canceled item and releases its claim. A run that never claimed has nothing to give back.
func (e *Engine) giveBackClaim(ctx context.Context, open openRun) {
	owner, err := currentOwner(e.ctx, e.dir, open.id)
	if err == nil {
		err = ReleaseDeps{Lifecycle: e.deps.Lifecycle, Trees: e.deps.Trees, Clock: e.deps.Clock}.giveBack(ctx, e.logger, open.in.Project, open.in.Item, owner)
	}

	if err != nil && !errors.Is(err, errNoOwner) {
		e.logger.WarnContext(ctx, "cannot release the canceled item", "item", open.in.Item, "error", err)

		return
	}

	e.forget(open.id)
}
