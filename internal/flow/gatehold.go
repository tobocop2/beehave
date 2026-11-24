package flow

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"

	"github.com/tobocop2/beehave/internal/model"
)

// errNoGate is the refusal of a gate command for an item that has no open gate.
var errNoGate = errors.New("no gate is open")

// errGateState is the refusal of a gate command for a gate that is in the wrong state for the command.
var errGateState = errors.New("the gate is in the wrong state")

// heldQueueName is the queue of a held gate. No engine registers it, so no engine starts a run that waits there, and the run holds no place on the queue of its project.
// The queue name in the store is the record of the hold: it lasts across a restart.
const heldQueueName = "gate_held"

// priorityQuery sets the place of a queued run. The workflow library has no call that changes the priority of a run that waits in a queue, so this is the one write.
// It names the status ENQUEUED: a run that started keeps the place that it started with.
const priorityQuery = `UPDATE workflow_status SET priority = ? WHERE workflow_uuid = ? AND status = 'ENQUEUED'`

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

// HoldGate parks the running gate of an item and stops its build job at the next tick of the engine. The gate leaves the queue of its project, so another gate of the project starts,
// and the job frees its slot on the host. beehave gate release puts the gate back on the queue of its project with the place that it had.
func (c *Client) HoldGate(item string) error {
	gate, err := c.openGateOf(item)
	if err != nil {
		return err
	}

	if err = gate.refuseHold(item); err != nil {
		return err
	}

	return c.parkAndStop(gate, item)
}

// parkAndStop parks the gate and then cancels its build job. The gate is parked first: a gate that sees its job canceled reads the park,
// and so it never takes the cancel for a job that a release reopens.
func (c *Client) parkAndStop(gate openGate, item string) error {
	if _, err := dbos.ResumeWorkflow[ExclusiveOutput](c.flow, gate.row.ID, dbos.WithResumeQueue(heldQueueName)); err != nil {
		return failed("park the gate of item "+item, err)
	}

	return failed("hold the gate of item "+item, dbos.CancelWorkflow(c.flow, gate.job.ID))
}

// refuseHold is the refusal of a hold for a gate that does not run a build job, or nil for a gate that does.
func (g openGate) refuseHold(item string) error {
	switch {
	case g.job == nil:
		return fmt.Errorf("%w: the gate of item %s has no build job on a host, so there is no slot to free", errGateState, item)
	case g.held():
		return fmt.Errorf("%w: the gate of item %s is held already. beehave gate release %s puts it back on the queue", errGateState, item, item)
	case g.job.Status == dbos.WorkflowStatusCancelled:
		return fmt.Errorf("%w: the gate of item %s waits for its place on the queue of its project and has no job that runs. beehave gate priority changes its place", errGateState, item)
	case g.job.Status != dbos.WorkflowStatusPending:
		return fmt.Errorf("%w: the gate of item %s waits for its host and does not run. beehave gate priority changes its place", errGateState, item)
	}

	return nil
}

// failed names the step of a gate command that failed. It returns nil for no error.
func failed(step string, err error) error {
	if err == nil {
		return nil
	}

	return fmt.Errorf("%s: %w", step, err)
}

// ReleaseGate puts the held gate of an item back on the queue of its project. The gate keeps its place: the queue orders by priority and by the time of the first queue.
// The gate reopens its own build job on the queue of its host when it starts again (see awaitHeld).
func (c *Client) ReleaseGate(item string) error {
	gate, err := c.openGateOf(item)
	if err != nil {
		return err
	}

	if !gate.held() {
		return fmt.Errorf("%w: the gate of item %s is not held", errGateState, item)
	}

	_, err = dbos.ResumeWorkflow[ExclusiveOutput](c.flow, gate.row.ID, dbos.WithResumeQueue(exclusiveQueueName))

	return failed("release the gate of item "+item, err)
}

// held reports whether a person parked the gate: its run waits on the queue of the held gates, which no engine serves, and holds no place on the queue of its project.
func (g openGate) held() bool {
	return g.row.QueueName == heldQueueName
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
	moved := false

	db, err := sql.Open("sqlite", filepath.Join(c.dir, databaseFile)+"?_pragma=busy_timeout(30000)")
	if err == nil {
		defer db.Close()

		for _, id := range slices.DeleteFunc(slices.Clone(ids), func(id string) bool { return id == "" }) {
			var result sql.Result

			if result, err = db.ExecContext(context.Background(), priorityQuery, place, id); err != nil {
				break
			}

			if n, counted := result.RowsAffected(); counted == nil && n > 0 {
				moved = true
			}
		}
	}

	return moved, failed("write the place of the gate", err)
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

// errReplaced ends the run of a gate that is no longer the run of the gate: a person parked the gate, or released it and the queue started it again under a new run.
// The store drops the end of such a run, because the gate is queued again or runs under the new run, and that run starts from the record.
var errReplaced = errors.New("the run of the gate was replaced")

// awaitHeld waits for the result of the build job of a gate. A job that was canceled with its gate ends the wait with the cancel.
// A job that a person held is canceled while the gate is parked: the run that sees the cancel then ends at once, and the gate that beehave gate release
// queued again reopens the job on the queue of its host and waits for it as before.
// The run reads its start before it waits. A release that comes before the old run sees the cancel starts a second run of the gate, and the old run, which the
// start tells apart from the new one, ends and leaves the reopen of the job to the new run.
func (e *Engine) awaitHeld(ctx dbos.Context, handle dbos.WorkflowHandle[ExclusiveOutput], queue string) (ExclusiveOutput, error) {
	id, began, err := e.runOf(ctx)
	if err != nil {
		return ExclusiveOutput{}, err
	}

	for {
		done, err := awaitResult(ctx, e.ctx, handle, e.deps.Clock.resultOptions())
		if !errors.Is(err, dbos.ErrAwaitedWorkflowCancelled) {
			return done, err
		}

		if reopened, rerr := e.reopenGate(id, handle.GetWorkflowID(), queue, began); !reopened || rerr != nil {
			return ExclusiveOutput{}, cmp.Or(rerr, err)
		}
	}
}

// runOf is the id of the run of ctx and the time that it started at.
func (e *Engine) runOf(ctx dbos.Context) (string, time.Time, error) {
	id, err := dbos.GetWorkflowID(ctx)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("read the run id of the gate: %w", err)
	}

	began, err := e.runStartedAt(id)

	return id, began, err
}

// runStartedAt is the time that the run id started at, or the zero time for a run that has not started.
func (e *Engine) runStartedAt(id string) (time.Time, error) {
	rows, err := e.store().List(dbos.WithFilterWorkflowIDs(id))
	if err != nil {
		return time.Time{}, fmt.Errorf("read the start of the gate: %w", err)
	}

	row, _ := rowWithID(rows, id)

	return row.StartedAt, nil
}

// reopenGate reads the gate id and its build job job after the job was canceled. It reopens the job on queue and returns true when a release queued the gate again
// and began is the start of this run. It returns errReplaced when the gate is parked or another run has the gate, and false when the gate was canceled with its job, which is a stop.
// A job that is not canceled any more is open already, so the run waits for it and reopens nothing. A cancel clears the queue of a run, so the caller names the queue of the job.
func (e *Engine) reopenGate(id, job, queue string, began time.Time) (bool, error) {
	rows, err := e.store().List(dbos.WithFilterWorkflowIDs(id, job))
	if err != nil {
		return false, fmt.Errorf("read the build job of the gate: %w", err)
	}

	gate, found := rowWithID(rows, id)

	switch {
	case !found || gate.Name != exclusiveName || gate.Status == dbos.WorkflowStatusCancelled:
		return false, nil
	case gate.QueueName == heldQueueName || gate.Status != dbos.WorkflowStatusPending || !gate.StartedAt.Equal(began):
		return false, errReplaced
	}

	if open, _ := rowWithID(rows, job); open.Status != dbos.WorkflowStatusCancelled {
		return true, nil
	}

	return e.resumeJob(job, queue)
}

// resumeJob puts the canceled build job back on queue and returns true. It runs on the context of the engine, not on the run:
// a call inside a run is a recorded step, and the steps of a gate stay as they were.
func (e *Engine) resumeJob(job, queue string) (bool, error) {
	_, err := dbos.ResumeWorkflow[ExclusiveOutput](e.ctx, job, dbos.WithResumeQueue(queue))

	return err == nil, failed("reopen the build job of the gate", err)
}

// rowWithID picks the row with the id.
func rowWithID(rows []dbos.WorkflowStatus, id string) (dbos.WorkflowStatus, bool) {
	i := slices.IndexFunc(rows, func(row dbos.WorkflowStatus) bool { return row.ID == id })
	if i < 0 {
		return dbos.WorkflowStatus{}, false
	}

	return rows[i], true
}
