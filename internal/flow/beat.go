package flow

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
)

// errClaimLost is returned when the engine stopped trusting the claim of a run.
var errClaimLost = errors.New("the claim on the item is lost")

// trustLimit is how long the engine trusts a claim after a good renewal began: the lease, less one tick.
const trustLimit = claimLease - tickInterval

// trustPoll is how often a phase looks again for the first trust of its run. trustGrace is how long a phase waits for a run that is not open.
const (
	trustPoll  = time.Second
	trustGrace = 3 * time.Second
)

// claimHold is one claim a run holds. renewed is when the last good renewal began, or when the claim was taken; it is zero until the first trust.
// failure is the text of the last renewal that failed since then. lost is why the engine stopped trusting the claim, or empty. stop ends the timer of the trust.
// ctx is the context of the claim: it ends when the claim is lost or dropped, and every agent of the run runs under it. cancel ends it.
type claimHold struct {
	project string
	owner   string
	renewed time.Time
	failure string
	lost    string
	stop    func()
	ctx     context.Context
	cancel  context.CancelFunc
}

// holds keeps the claim of each run this process executes, by run id, from Claim until Release.
// A phase asks it, by the id of its own run, whether that run holds a trusted claim now, and gets the context of the claim.
type holds struct {
	mu   sync.Mutex
	rows map[string]claimHold
}

// hold records the claim of the run in ctx. It returns the run id, empty outside a workflow, and the function that forgets the claim.
// The claim is not trusted until trust stamps it.
func (h *holds) hold(ctx dbos.Context, project, owner string) (string, func()) {
	id, err := dbos.GetWorkflowID(ctx)
	if err != nil {
		return "", func() {}
	}

	h.add(id, claimHold{project: project, owner: owner})

	return id, func() { h.drop(id) }
}

// add records the claim of a run and gives it its context. A claim that the run already holds ends: its context is done, so no agent keeps running under a row that no longer exists.
func (h *holds) add(id string, row claimHold) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.rows == nil {
		h.rows = map[string]claimHold{}
	}

	if old, found := h.rows[id]; found {
		old.cancel()
	}

	row.ctx, row.cancel = context.WithCancel(context.Background())
	h.rows[id] = row
}

// drop forgets the claim of a run, stops its timer and ends its context, so the agents of the run stop with it.
func (h *holds) drop(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.release(id)
}

// release stops the timer of a claim, ends its context and forgets it. The caller holds the lock.
func (h *holds) release(id string) {
	if row, found := h.rows[id]; found {
		if row.stop != nil {
			row.stop()
		}

		row.cancel()
	}

	delete(h.rows, id)
}

// clear drops every claim the engine still holds, so no timer of a trust fires after the engine ends.
func (h *holds) clear() {
	h.mu.Lock()
	defer h.mu.Unlock()

	for id := range h.rows {
		h.release(id)
	}
}

// snapshot lists the claims that still renew: a lost claim stays lost, and a claim with no first trust waits for it.
func (h *holds) snapshot() map[string]claimHold {
	h.mu.Lock()
	defer h.mu.Unlock()

	rows := make(map[string]claimHold, len(h.rows))

	for id, row := range h.rows {
		if row.lost == "" && !row.renewed.IsZero() {
			rows[id] = row
		}
	}

	return rows
}

// fail marks the claim of a run lost and ends its context. A start under the context after this call finds it done,
// and an agent that started before it dies with its process group. drop ends the same context when the run ends.
func (h *holds) fail(id, reason string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	row, found := h.rows[id]
	if !found {
		return
	}

	if row.stop != nil {
		row.stop()
	}

	row.cancel()

	row.lost = reason
	h.rows[id] = row
}

// renew records that a renewal which began at start worked, and swaps the timer of the trust for stop. It reports false, and keeps nothing, for a run that is gone or lost.
func (h *holds) renew(id string, start time.Time, stop func()) bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	row, found := h.rows[id]
	if !found || row.lost != "" {
		return false
	}

	if row.stop != nil {
		row.stop()
	}

	row.renewed, row.failure, row.stop = start, "", stop
	h.rows[id] = row

	return true
}

// note keeps the text of a renewal that failed, so the reason of a lost claim names it.
func (h *holds) note(id, failure string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if row, found := h.rows[id]; found {
		row.failure = failure
		h.rows[id] = row
	}
}

// expired returns the claim of the run when it is still trusted and its last good renewal began at stamp. A later renewal makes a timer of an earlier stamp void.
func (h *holds) expired(id string, stamp time.Time) (claimHold, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	row, found := h.rows[id]

	return row, found && row.lost == "" && row.renewed.Equal(stamp)
}

// distrust says why the run does not hold a trusted claim now, or returns an empty reason and the context of the claim when it does.
// The run holds no claim, or the claim is lost, or no renewal worked yet, or the last good renewal is older than the limit on either clock.
// early is true when the run has no first trust yet: it holds no claim row, or no renewal of the row worked.
func (h *holds) distrust(id string, clk clock) (claim context.Context, reason string, early bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	row, found := h.rows[id]

	switch {
	case !found:
		return nil, "the run that started this step holds no claim on the item", true
	case row.lost != "":
		return nil, row.lost, false
	case row.renewed.IsZero():
		return nil, "no renewal of the claim has worked yet", true
	case clk.age(row.renewed) >= trustLimit:
		return nil, lapse(row), false
	}

	return row.ctx, "", false
}

// lapse says why the engine stops trusting a claim that no renewal kept alive.
func lapse(row claimHold) string {
	reason := fmt.Sprintf("no renewal of the claim worked for %s, and its lease can end", trustLimit)
	if row.failure != "" {
		reason += ": the last renewal failed with: " + row.failure
	}

	return reason
}

// beatClaims renews every claim this process holds.
// A tracker that says the claim is gone ends the run at once. A renewal that fails for any other reason is tried again on the next tick,
// until one tick before the lease of the last good renewal can end. Each tick counts that time on the monotonic and on the wall clock, so a sleep of the machine ends the trust.
func (e *Engine) beatClaims() {
	for id, row := range e.holds.snapshot() {
		now := e.deps.Clock.now()

		left := trustLimit - e.deps.Clock.age(row.renewed)
		if left <= 0 {
			e.lose(id, lapse(row))

			continue
		}

		text, err := e.beatOne(e.ctx, row, left)
		e.settle(id, now, text, err)
	}
}

// settle applies the answer of a renewal that began at start: the tracker says the claim is gone, the renewal worked, or it failed and the next tick tries again.
func (e *Engine) settle(id string, start time.Time, text string, err error) {
	switch {
	case text != "":
		e.lose(id, text)
	case err == nil:
		e.trust(id, start)
	default:
		e.holds.note(id, err.Error())
		e.logger.Warn("cannot renew the claim; the next tick tries again", "run", id, "error", err)
	}
}

// trust stamps the claim of a run with the start of a good renewal and arms the timer that ends the trust.
// The timer fires whatever a renewal in flight does, so a tracker call that hangs cannot hold the cancel past the lease.
func (e *Engine) trust(id string, start time.Time) {
	stop := e.deps.Clock.after(trustLimit-e.deps.Clock.age(start), func() { e.deadline(id, start) })
	if !e.holds.renew(id, start, stop) {
		stop()
	}
}

// deadline ends the trust in a claim whose last good renewal began at stamp, when no later renewal worked.
func (e *Engine) deadline(id string, stamp time.Time) {
	if row, expired := e.holds.expired(id, stamp); expired {
		e.lose(id, lapse(row))
	}
}

// lose marks the claim of a run lost and cancels the run. Ending the claim ends the context of every agent of the run, so the agent step in flight stops.
// The worktree and the claim stay as they are: another run may hold the item now.
func (e *Engine) lose(id, reason string) {
	e.holds.fail(id, reason)
	e.cancels.stop(id)
}

// beatOne renews one claim under its own timeout, so a tracker call that hangs delays no other claim and no cancel scan for longer than the timeout.
// The timeout ends no later than the time left to trust the claim.
func (e *Engine) beatOne(parent context.Context, row claimHold, left time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(parent, min(cmp.Or(e.deps.Clock.Beat, beatLimit), left))
	defer cancel()

	return e.deps.Beats.Beat(ctx, row.project, row.owner)
}

// claimCheck returns errClaimLost, with the reason, unless the run holds a trusted claim now. It asks by the id of the run, never by item:
// another run may hold the item, and its claim is not the claim of this one.
// DBOS stores the error of the run, so the reason passes through the redactor.
func (e *Engine) claimCheck(run, item string) error {
	_, reason, _ := e.holds.distrust(run, e.deps.Clock)

	return e.lostError(item, reason)
}

// lostError returns errClaimLost with the reason, or nil for an empty reason.
func (e *Engine) lostError(item, reason string) error {
	if reason == "" {
		return nil
	}

	return fmt.Errorf("%w: item %s: %s. Run obee run %s again to claim it", errClaimLost, item, e.redact(reason), item)
}

// awaitClaim returns the context of the claim once the run that queued the phase holds a trusted claim. A run that has no first trust yet is waited for while it is open,
// and for a grace time after it ended: obee resume reopens a phase before it reopens its run, so the phase can reach the queue first.
// Any other reason stops the phase. A phase of a run that ended for good stops when the grace time is over.
func (e *Engine) awaitClaim(ctx context.Context, in PhaseInput) (context.Context, error) {
	graceEnd := e.deps.Clock.now().Add(cmp.Or(e.deps.Clock.Grace, trustGrace))

	for {
		claim, reason, early := e.holds.distrust(in.Run, e.deps.Clock)
		if reason == "" {
			return claim, nil
		}

		if err := e.waitOrStop(in, reason, early, graceEnd); err != nil {
			return nil, err
		}

		if err := pause(ctx, cmp.Or(e.deps.Clock.Poll, trustPoll)); err != nil {
			return nil, err
		}
	}
}

// pause waits for d, or returns the cause of the end of ctx.
func pause(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-timer.C:
		return nil
	}
}

// waitOrStop returns nil when a phase keeps waiting for the first trust of its run: the run has none yet, and it is open or the grace time is not over.
// Otherwise it returns the error that stops the phase.
func (e *Engine) waitOrStop(in PhaseInput, reason string, early bool, graceEnd time.Time) error {
	if early {
		open, err := e.runOpen(in.Run)
		if err != nil || open || e.deps.Clock.now().Before(graceEnd) {
			return err
		}
	}

	return e.lostError(in.Item, reason)
}

// runOpen reports whether the run with this id is in the store and has not ended.
func (e *Engine) runOpen(id string) (bool, error) {
	rows, err := dbos.ListWorkflows(e.ctx, dbos.WithFilterWorkflowIDs(id))
	if err != nil {
		return false, fmt.Errorf("read run %s: %w", id, err)
	}

	return len(rows) == 1 && !(run{Status: rows[0]}).terminal(), nil
}
