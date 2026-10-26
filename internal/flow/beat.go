package flow

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
)

// errClaimLost is returned when the tracker refuses to renew the claim of a run.
var errClaimLost = errors.New("the claim on the item is lost")

// claimHold is one claim a run holds. lost is the reason the last renewal failed, or empty.
type claimHold struct {
	project string
	owner   string
	lost    string
}

// holds keeps the claim of each run this process executes, by run id, from Claim until Release.
type holds struct {
	mu   sync.Mutex
	rows map[string]claimHold
}

// hold records the claim of the run in ctx. The returned function forgets it.
func (h *holds) hold(ctx dbos.Context, project, owner string) func() {
	id, err := dbos.GetWorkflowID(ctx)
	if err != nil {
		return func() {}
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	if h.rows == nil {
		h.rows = map[string]claimHold{}
	}

	h.rows[id] = claimHold{project: project, owner: owner}

	return func() {
		h.mu.Lock()
		defer h.mu.Unlock()

		delete(h.rows, id)
	}
}

// snapshot lists the claims that still renew: a lost claim stays lost.
func (h *holds) snapshot() map[string]claimHold {
	h.mu.Lock()
	defer h.mu.Unlock()

	rows := make(map[string]claimHold, len(h.rows))

	for id, row := range h.rows {
		if row.lost == "" {
			rows[id] = row
		}
	}

	return rows
}

func (h *holds) fail(id, reason string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if row, found := h.rows[id]; found {
		row.lost = reason
		h.rows[id] = row
	}
}

// lostReason returns why the claim of the run in ctx failed to renew, or an empty string.
func (h *holds) lostReason(ctx dbos.Context) string {
	id, err := dbos.GetWorkflowID(ctx)
	if err != nil {
		return ""
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	return h.rows[id].lost
}

// beatClaims renews every claim this process holds. A failed renewal marks the run, and the run stops before its next agent step.
func (e *Engine) beatClaims() {
	for id, row := range e.holds.snapshot() {
		text, err := e.beatOne(row)

		switch {
		case err != nil:
			e.holds.fail(id, err.Error())
		case text != "":
			e.holds.fail(id, text)
		}
	}
}

// beatOne renews one claim under its own timeout, so a tracker call that hangs delays no other claim and no cancel scan for longer than the timeout.
func (e *Engine) beatOne(row claimHold) (string, error) {
	ctx, cancel := context.WithTimeout(e.ctx, cmp.Or(e.deps.Clock.Beat, beatLimit))
	defer cancel()

	return e.deps.Beats.Beat(ctx, row.project, row.owner)
}

// claimCheck returns errClaimLost, with the reason, when the claim of the run in ctx failed its last renewal.
// DBOS stores the error of the run, so the reason passes through the redactor.
func (e *Engine) claimCheck(ctx dbos.Context, item string) error {
	if reason := e.holds.lostReason(ctx); reason != "" {
		return fmt.Errorf("%w: item %s: %s. Run obee run %s again to claim it", errClaimLost, item, e.redact(reason), item)
	}

	return nil
}
