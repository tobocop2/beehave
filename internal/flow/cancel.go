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
	othersKey    = "others"
	othersStep   = "others"
	mergeKey     = "merge"
	tickInterval = 2 * time.Second
)

// attempt is one execution of a run. A resumed run reuses the run id and gets a new attempt, so the pointer, not the id, names the attempt.
// cancel ends its context, and ended closes when its workflow function returns. owner is the token of its claim: the item body sets it after the take
// and a give-back reads it after ended closes, and an attempt made from a cancel marker carries the token of the marker.
type attempt struct {
	id     string
	in     ItemInput
	cancel context.CancelFunc
	ended  chan struct{}
	// gate is true for a gate of the exclusive queue: a cancel stops its context, and there is no claim or worktree to give back.
	gate  bool
	owner string
}

// cancels holds the attempts this process executes, by run id.
type cancels struct {
	mu   sync.Mutex
	runs map[string]*attempt
	// releasing holds the attempts whose give-back is under way, so no second give-back starts for one run.
	releasing map[*attempt]bool
	// stopped holds the attempts that a cut stopped and whose workflow function may still run, so a reopen of the run can wait for their end.
	stopped map[*attempt]bool
}

// begin marks the give-back of a canceled run that this process neither executes nor releases, and reports whether the caller must release it.
// a is the attempt that the caller made for the give-back: the run is not an attempt of this process.
func (c *cancels) begin(a *attempt) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, local := c.runs[a.id]; local || c.releases(a.id) {
		return false
	}

	c.markReleasing(a)

	return true
}

// releases reports whether a give-back of any attempt of the run is under way. The caller holds the lock.
func (c *cancels) releases(id string) bool {
	for a := range c.releasing {
		if a.id == id {
			return true
		}
	}

	return false
}

// markReleasing records a give-back under way. The caller holds the lock.
func (c *cancels) markReleasing(a *attempt) {
	if c.releasing == nil {
		c.releasing = map[*attempt]bool{}
	}

	c.releasing[a] = true
}

// finish ends the record of the give-back of an attempt and leaves the record of any other.
func (c *cancels) finish(a *attempt) {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.releasing, a)
}

// add lists an attempt under its run id, in place of an earlier attempt of the same run.
func (c *cancels) add(a *attempt) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.runs == nil {
		c.runs = map[string]*attempt{}
	}

	c.runs[a.id] = a

	c.running("")
}

// stop cuts the attempt that this process lists under the run id and returns it with every earlier cut attempt of that run whose workflow function has not returned.
func (c *cancels) stop(id string) []*attempt {
	c.mu.Lock()
	defer c.mu.Unlock()

	if a, listed := c.runs[id]; listed {
		c.cut(a)
	}

	return c.running(id)
}

// running drops the cut attempts whose workflow function has returned and lists the others of the run id. The caller holds the lock.
func (c *cancels) running(id string) []*attempt {
	var list []*attempt

	for a := range c.stopped {
		select {
		case <-a.ended:
			delete(c.stopped, a)
		default:
			if a.id == id {
				list = append(list, a)
			}
		}
	}

	return list
}

// attempts lists the attempts this process executes.
func (c *cancels) attempts() []*attempt {
	c.mu.Lock()
	defer c.mu.Unlock()

	list := make([]*attempt, 0, len(c.runs))
	for _, a := range c.runs {
		list = append(list, a)
	}

	return list
}

// endedAttempts lists the attempts whose workflow function has returned.
func (c *cancels) endedAttempts() []*attempt {
	c.mu.Lock()
	defer c.mu.Unlock()

	var list []*attempt

	for _, a := range c.runs {
		select {
		case <-a.ended:
			list = append(list, a)
		default:
		}
	}

	return list
}

// forget drops each attempt in list that is still the one listed under its run id. A resumed run that replaced an attempt stays listed.
func (c *cancels) forget(list []*attempt) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, a := range list {
		if c.runs[a.id] == a {
			delete(c.runs, a.id)
		}
	}
}

// cancel cuts each attempt in list that is still the one this process lists under its run id. An attempt that a resumed run replaced is left alone.
// It returns the attempts it cut, so the caller releases each one once.
func (c *cancels) cancel(list []*attempt) []*attempt {
	c.mu.Lock()
	defer c.mu.Unlock()

	var cut []*attempt

	for _, a := range list {
		if c.runs[a.id] == a {
			c.cut(a)
			cut = append(cut, a)
		}
	}

	return cut
}

// cut calls the cancel function of the attempt and forgets it. The caller holds the lock.
func (c *cancels) cut(a *attempt) {
	a.cancel()
	delete(c.runs, a.id)

	if c.stopped == nil {
		c.stopped = map[*attempt]bool{}
	}

	c.stopped[a] = true

	if !a.gate {
		c.markReleasing(a)
	}
}

// track runs body with a context that the tick can cancel, and gives body its attempt. The attempt stays in the list of local runs until a tick drops it.
func (e *Engine) track(parent dbos.Context, in ItemInput, body func(dbos.Context, *attempt) (ItemResult, error)) (ItemResult, error) {
	runCtx, cancel := dbos.WithCancel(parent)
	defer cancel()

	a := &attempt{in: in, cancel: cancel, ended: make(chan struct{})}

	if id, err := dbos.GetWorkflowID(parent); err == nil {
		a.id = id
		e.cancels.add(a)

		defer close(a.ended)
	}

	return body(runCtx, a)
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
	ended := e.cancels.endedAttempts()

	canceled, err := e.canceledAttempts(e.cancels.attempts())
	if err != nil {
		e.logger.Warn("cannot list the canceled runs", "error", err)

		return
	}

	cut := e.cancels.cancel(canceled)

	e.cancels.forget(ended)

	e.giveBackCut(cut, stop)
	e.giveBackStrayCancels()
}

// giveBackCut starts the give-back of each canceled run that has one. The give-back waits for the run to end and calls the tracker,
// so it runs beside the tick: a slow release delays no beat and no scan.
func (e *Engine) giveBackCut(cut []*attempt, stop <-chan struct{}) {
	for _, a := range cut {
		if a.gate {
			continue
		}

		e.tickRun.Go(func() { e.giveBackWhenEnded(a, stop) })
	}
}

// giveBackStrayCancels gives back each canceled run that no run of this process executes: a run that beehave resume reopened for another build, or one that still waits in the queue.
// Its cancel marker and the claim of its resume stay until an engine gives them back, and the tick is the engine that runs now.
// A run that this process executes or releases is skipped. The release runs beside the tick, as the release of a local run does.
func (e *Engine) giveBackStrayCancels() {
	pending, err := pendingCancels(e.dir, e.logger)
	if err != nil {
		e.logger.Warn("cannot read the canceled runs", "error", err)

		return
	}

	for id, mark := range pending {
		if a := mark.attempt(id); e.cancels.begin(a) {
			e.tickRun.Go(func() { e.giveBackStray(a) })
		}
	}
}

// giveBackStray gives back one canceled run. It leaves the marker of a run that is open and not canceled yet, because beehave cancel writes the marker first.
// It drops the marker of a run that ended without a cancel or has a newer run.
func (e *Engine) giveBackStray(a *attempt) {
	defer e.cancels.finish(a)

	ctx, cancel := ReleaseDeps{Clock: e.deps.Clock}.limit(context.WithoutCancel(e.ctx))
	defer cancel()

	e.sweepOne(ctx, a, false)
}

// canceledAttempts lists which of the local attempts the store marks as canceled. It reads no other run, so the cost of a tick does not grow with the history of the store.
// The store row belongs to the attempt that the caller listed before the read, not to a resume that lists the same run id after it.
func (e *Engine) canceledAttempts(local []*attempt) ([]*attempt, error) {
	if len(local) == 0 {
		return nil, nil
	}

	marked, err := e.canceledRows(local)
	if err != nil {
		return nil, err
	}

	var canceled []*attempt

	for _, a := range local {
		if marked[a.id] {
			canceled = append(canceled, a)
		}
	}

	return canceled, nil
}

// canceledRows reads the run ids among the local attempts that the store marks as canceled.
func (e *Engine) canceledRows(local []*attempt) (map[string]bool, error) {
	ids := make([]string, 0, len(local))
	for _, a := range local {
		ids = append(ids, a.id)
	}

	rows, err := e.store().List(dbos.WithFilterStatus(dbos.WorkflowStatusCancelled), dbos.WithFilterWorkflowIDs(ids...))
	if err != nil {
		return nil, err
	}

	marked := make(map[string]bool, len(rows))
	for _, row := range rows {
		marked[row.ID] = true
	}

	return marked, nil
}

// giveBackWhenEnded waits until the workflow function of the run has returned, then gives the item back. A stop of the tick ends the wait.
// A run that is canceled and not released at the stop keeps its cancel marker, and the next start gives it back.
func (e *Engine) giveBackWhenEnded(a *attempt, stop <-chan struct{}) {
	defer e.cancels.finish(a)

	select {
	case <-a.ended:
		e.giveBack(context.WithoutCancel(e.ctx), a)
	case <-stop:
	}
}

// giveBack forgets the agent groups of a canceled attempt, removes its worktree and releases its claim with the token of the attempt.
func (e *Engine) giveBack(ctx context.Context, a *attempt) {
	e.endGroups(a)
	e.giveBackClaim(ctx, a)
}

// giveBackClaim removes the worktree of a canceled item and releases the claim of the attempt. A run that never claimed has nothing to give back.
// The files of the run go only while they name the token of the attempt: a resume can take the item once the release frees it.
func (e *Engine) giveBackClaim(ctx context.Context, a *attempt) {
	owner, err := releaseOwner(e.ctx, a)
	if err == nil {
		err = e.asClient().giveOwned(ctx, a, owner)
	}

	if err != nil && !errors.Is(err, errNoOwner) {
		e.logger.WarnContext(ctx, "cannot release the canceled item", "item", a.in.Item, "error", err)

		return
	}

	e.forgetAttempt(a.id, owner)
}
