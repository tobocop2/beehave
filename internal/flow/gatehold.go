package flow

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"

	"github.com/tobocop2/beehest/internal/model"
)

// errNoGate is the refusal of a gate command for an item that has no open gate.
var errNoGate = errors.New("no gate is open")

// errGateState is the refusal of a gate command for a gate that is in the wrong state for the command.
var errGateState = errors.New("the gate is in the wrong state")

// heldQueueName is the queue of a held gate, which no engine serves; the queue name in the store is the record of the hold.
const heldQueueName = "gate_held"

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
		return fmt.Errorf("%w: the gate of item %s runs, and a gate that runs keeps its place. beehest gate hold %s stops it", errGateState, item, item)
	}

	return nil
}

// HoldGate parks the running gate of an item and stops its build job at the next tick of the engine. The gate leaves the queue of its project, so another gate of the project starts,
// and the job frees its slot on the host. beehest gate release puts the gate back on the queue of its project with the place that it had.
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

// parkAndStop parks the gate and then cancels its build job, so the run of the gate has lost its row before it can see the cancel.
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
		return fmt.Errorf("%w: the gate of item %s is held already. beehest gate release %s puts it back on the queue", errGateState, item, item)
	case g.job.Status == dbos.WorkflowStatusCancelled && g.row.Status == dbos.WorkflowStatusPending:
		return fmt.Errorf("%w: the gate of item %s started again and has not reopened its job yet. Run beehest gate hold %s when the job runs", errGateState, item, item)
	case g.job.Status == dbos.WorkflowStatusCancelled:
		return fmt.Errorf("%w: the gate of item %s waits for its place on the queue of its project and has no job that runs. beehest gate priority changes its place", errGateState, item)
	case g.job.Status != dbos.WorkflowStatusPending:
		return fmt.Errorf("%w: the gate of item %s waits for its host and does not run. beehest gate priority changes its place", errGateState, item)
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

// ReleaseGate puts the held gate of an item back on the queue of its project with its place; the gate reopens its own build job when it starts again.
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
		return openGate{}, fmt.Errorf("%w for item %s. beehest status lists the open runs", errNoGate, item)
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

// errGateLeft ends the run of a gate that lost the gate before it reopened its build job: a person canceled or held the gate, or a release started it again under a new run.
var errGateLeft = errors.New("the gate was canceled, held or started again before this run reopened its build job, so this run ends and leaves the job to the gate")

// jobStopStepName is the recorded step that waits for the end of the earlier run of a canceled build job before the gate reopens the job.
const jobStopStepName = "gate:stop job"

// awaitHeld waits for the result of the build job of a gate and reopens the job on queue each time that the wait records the job as canceled.
func (e *Engine) awaitHeld(ctx dbos.Context, handle dbos.WorkflowHandle[ExclusiveOutput], queue string) (ExclusiveOutput, error) {
	for {
		done, err := awaitResult(ctx, e.ctx, handle, e.deps.Clock.resultOptions())
		if !errors.Is(err, dbos.ErrAwaitedWorkflowCancelled) {
			return done, err
		}

		if err := e.reopenJob(ctx, handle.GetWorkflowID(), queue); err != nil {
			return ExclusiveOutput{}, err
		}
	}
}

// reopenJob waits for the end of the earlier run of the canceled build job and puts the job back on queue, both in recorded steps of the run of the gate.
func (e *Engine) reopenJob(ctx dbos.Context, job, queue string) error {
	_, err := dbos.RunAsStep(ctx, func(step context.Context) (struct{}, error) { return struct{}{}, e.stopJob(step, job) }, dbos.WithStepName(jobStopStepName))
	if err != nil {
		return ownedGate(err)
	}

	if e.jobStopped != nil {
		e.jobStopped(ctx)
	}

	return resumeJob(ctx, job, queue)
}

// resumeJob puts the canceled build job back on queue in a recorded step of the run of ctx. The store refuses the step of a run that no longer owns its row.
func resumeJob(ctx dbos.Context, job, queue string) error {
	_, err := dbos.ResumeWorkflow[ExclusiveOutput](ctx, job, dbos.WithResumeQueue(queue))

	return failed("reopen the build job of the gate", ownedGate(err))
}

// ownedGate turns the refusal of a step of a run that no longer has its gate into errGateLeft. It returns any other error as it is.
func ownedGate(err error) error {
	if errors.Is(err, dbos.ErrConflictingWorkflowID) {
		return errGateLeft
	}

	return err
}

// jobStopLimit is the longest time that a reopen waits for the earlier run of the build job to end.
const jobStopLimit = 5 * time.Minute

// stopJob cuts the earlier run of the build job on this engine and waits for its end, for at most the stop limit or until ctx ends.
func (e *Engine) stopJob(ctx context.Context, job string) error {
	limit := cmp.Or(e.deps.Clock.JobStop, jobStopLimit)

	wait, cancel := context.WithTimeout(ctx, limit)
	defer cancel()

	err := waitEnded(wait, e.cancels.stop(job))
	if err != nil && ctx.Err() == nil {
		return fmt.Errorf("the earlier run of the build job %s did not stop within %s, so the gate does not reopen it. Stop the program of the job on the host, then run beehest resume for the item", job, limit)
	}

	return err
}

// waitEnded waits until the workflow function of each attempt has returned, or ctx ends.
func waitEnded(ctx context.Context, list []*attempt) error {
	for _, a := range list {
		select {
		case <-a.ended:
		case <-ctx.Done():
			return stopError(ctx)
		}
	}

	return nil
}
