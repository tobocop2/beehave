package flow

import (
	"cmp"
	"errors"
	"fmt"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"

	"github.com/tobocop2/beehave/internal/model"
)

// errNoGate is the refusal of a gate command for an item that has no open gate.
var errNoGate = errors.New("no gate is open")

// errGateState is the refusal of a gate command for a gate that is in the wrong state for the command.
var errGateState = errors.New("the gate is in the wrong state")

// heldPoll is the time between two reads of a held build job, when the clock sets no poll.
const heldPoll = time.Second

// openGate is the open gate of an item and the build job of that gate, when it queued one.
type openGate struct {
	row dbos.WorkflowStatus
	in  ExclusiveInput
	job *dbos.WorkflowStatus
}

// SetGatePriority sets the place of the queued gate of an item to the place of level (P0 to P3). It changes the gate that waits on the queue of its project
// and the build job of a gate that waits for its host. A gate that runs keeps its place.
func (c *Client) SetGatePriority(item, level string) error {
	severity, err := model.ParseSeverity(level)
	if err != nil || severity == model.SeverityUnknown {
		return fmt.Errorf("%w: %q is not a level. Give P0, P1, P2 or P3", errGateState, level)
	}

	gate, err := c.openGateOf(item)
	if err != nil {
		return err
	}

	moved, err := c.setPriorities(placeOf(severity, false), gate.row.ID, gate.jobID())
	if err != nil {
		return fmt.Errorf("set the place of the gate of item %s: %w", item, err)
	}

	if !moved {
		return fmt.Errorf("%w: the gate of item %s runs, and a gate that runs keeps its place. beehave gate hold %s stops it", errGateState, item, item)
	}

	return nil
}

// HoldGate stops the build job of the running gate of an item at the next tick of the engine and frees its slot on the host. The gate waits, and beehave gate release puts the job back
// on the queue of its host with the place that it had.
func (c *Client) HoldGate(item string) error {
	gate, err := c.openGateOf(item)
	if err != nil {
		return err
	}

	switch {
	case gate.job == nil:
		return fmt.Errorf("%w: the gate of item %s has no build job on a host, so there is no slot to free", errGateState, item)
	case gate.job.Status == dbos.WorkflowStatusCancelled:
		return fmt.Errorf("%w: the gate of item %s is held already. beehave gate release %s puts it back on the queue", errGateState, item, item)
	case gate.job.Status != dbos.WorkflowStatusPending:
		return fmt.Errorf("%w: the gate of item %s waits for its host and does not run. beehave gate priority changes its place", errGateState, item)
	}

	return failed("hold the gate of item "+item, dbos.CancelWorkflow(c.flow, gate.job.ID))
}

// failed names the step of a gate command that failed. It returns nil for no error.
func failed(step string, err error) error {
	if err == nil {
		return nil
	}

	return fmt.Errorf("%s: %w", step, err)
}

// ReleaseGate puts the held build job of the gate of an item back on the queue of its host. The job keeps its place: the queue orders by priority and by the time of the first queue.
func (c *Client) ReleaseGate(item string) error {
	gate, err := c.openGateOf(item)
	if err != nil {
		return err
	}

	if gate.job == nil || gate.job.Status != dbos.WorkflowStatusCancelled {
		return fmt.Errorf("%w: the gate of item %s is not held", errGateState, item)
	}

	queue := slotQueueName(model.GateClass, cmp.Or(gate.in.SlotCount, 1))
	_, err = dbos.ResumeWorkflow[ExclusiveOutput](c.flow, gate.job.ID, dbos.WithResumeQueue(queue))

	return failed("release the gate of item "+item, err)
}

// jobID is the id of the build job of the gate, or empty when it has none.
func (g openGate) jobID() string {
	if g.job == nil {
		return ""
	}

	return g.job.ID
}

// openGateOf reads the one open gate of an item, with its build job.
func (c *Client) openGateOf(item string) (openGate, error) {
	found, ok, err := c.readGate(item)

	switch {
	case err != nil:
		return openGate{}, fmt.Errorf("read the gate of item %s: %w", item, err)
	case !ok:
		return openGate{}, fmt.Errorf("%w for item %s. beehave status lists the open runs", errNoGate, item)
	}

	return found, nil
}

// readGate reads the open gate of an item and its last build job, and says whether the item has an open gate.
func (c *Client) readGate(item string) (openGate, bool, error) {
	rows, err := c.store().Open("", dbos.WithFilterName(exclusiveName), dbos.WithFilterLoadInput(true))

	found, ok := openGate{}, false

	if err == nil {
		found, ok = gateOfItem(rows, item)
	}

	if err != nil || !ok {
		return found, ok, err //nolint:wrapcheck // openGateOf names the item
	}

	jobs, err := c.store().List(dbos.WithFilterName(hostJobName), dbos.WithFilterParentWorkflowID(found.row.ID))
	if len(jobs) > 0 {
		found.job = &jobs[len(jobs)-1]
	}

	return found, true, err //nolint:wrapcheck // openGateOf names the item
}

// gateOfItem picks the first gate of the item among the open exclusive runs.
func gateOfItem(rows []dbos.WorkflowStatus, item string) (openGate, bool) {
	for _, row := range rows {
		if in, ok := gateInputOf(row.Input); ok && in.Item == item {
			return openGate{row: row, in: in}, true
		}
	}

	return openGate{}, false
}

// setPriorities sets the place of each queued run in ids and says whether one of them was queued. An empty id is skipped.
func (c *Client) setPriorities(place uint, ids ...string) (bool, error) {
	places := map[string]placeWrite{}

	for _, id := range ids {
		places[id] = placeWrite{place: place}
	}

	return writePlaces(c.dir, true, places)
}

// currentPlace is the place that the gate of ctx has in the store now. A person can change it while the gate waits, so the build job of the gate takes the current one.
// A read that fails gives the place that the gate was queued with.
func (e *Engine) currentPlace(ctx dbos.Context, queued uint) uint {
	id, err := dbos.GetWorkflowID(ctx)
	if err != nil {
		return queued
	}

	return e.storedPlace(id, queued)
}

// storedPlace is the place of the run id in the store, or queued when the store cannot say.
func (e *Engine) storedPlace(id string, queued uint) uint {
	rows, err := e.store().List(dbos.WithFilterWorkflowIDs(id))
	if err != nil || len(rows) == 0 {
		return queued
	}

	return uint(rows[0].Priority)
}

// holdOf reads whether the gate of ctx is still open and whether its build job is stopped. A person who holds a gate stops the job and leaves the gate open.
// A gate that was canceled cancels its job too: that is a stop, not a hold.
func (e *Engine) holdOf(ctx dbos.Context, job string) (gateOpen, jobStopped bool, err error) {
	id, err := dbos.GetWorkflowID(ctx)
	if err != nil {
		return false, false, fmt.Errorf("read the run id of the gate: %w", err)
	}

	return e.holdAt(id, job)
}

// holdAt is holdOf for the run id of the gate.
func (e *Engine) holdAt(id, job string) (gateOpen, jobStopped bool, err error) {
	rows, err := e.store().List(dbos.WithFilterWorkflowIDs(id, job))
	if err != nil {
		return false, false, fmt.Errorf("read the build job of the gate: %w", err)
	}

	for _, row := range rows {
		switch row.ID {
		case id:
			gateOpen = row.Status != dbos.WorkflowStatusCancelled
		case job:
			jobStopped = row.Status == dbos.WorkflowStatusCancelled
		}
	}

	return gateOpen, jobStopped, nil
}

// awaitHeld waits for the result of the build job of a gate. A job that a person held keeps the gate waiting until beehave gate release opens the job again,
// and then the gate waits for the job as before. A job that was canceled with its gate ends the wait with the cancel.
func (e *Engine) awaitHeld(ctx dbos.Context, handle dbos.WorkflowHandle[ExclusiveOutput]) (ExclusiveOutput, error) {
	for {
		done, err := awaitResult(ctx, e.ctx, handle, e.deps.Clock.resultOptions())
		if !errors.Is(err, dbos.ErrAwaitedWorkflowCancelled) {
			return done, err
		}

		if released, werr := e.waitWhileHeld(ctx, handle.GetWorkflowID()); !released || werr != nil {
			return ExclusiveOutput{}, cmp.Or(werr, err)
		}
	}
}

// stillHeld reports whether the job is held: stopped while its gate is open.
func (e *Engine) stillHeld(ctx dbos.Context, job string) (bool, error) {
	open, stopped, err := e.holdOf(ctx, job)

	return open && stopped, err
}

// waitWhileHeld waits while the job is held. It returns false at once for a job that is not held, and false with the error when the wait cannot go on.
// It returns true when the hold ended: the job is not stopped any more, or the gate ended.
func (e *Engine) waitWhileHeld(ctx dbos.Context, job string) (bool, error) {
	held, err := e.stillHeld(ctx, job)
	if err != nil || !held {
		return false, err
	}

	ticker := time.NewTicker(cmp.Or(e.deps.Clock.Poll, heldPoll))
	defer ticker.Stop()

	for held && err == nil {
		select {
		case <-ctx.Done():
			return false, stopError(ctx)
		case <-ticker.C:
		}

		held, err = e.stillHeld(ctx, job)
	}

	return err == nil, err
}
