package flow

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
)

const (
	// moveLockName names the lock that a mover holds from before the cancel to after the rewind of a move. A held lock means the mover lives.
	moveLockName = "move.lock"
	// moveAttribute names the attribute of a run that a mover sets before it cancels the run.
	moveAttribute = "move"
)

// moveMark is the value of the move attribute: where the run goes, and the queue that the cancel takes from it.
type moveMark struct {
	To    string `json:"to"`
	Queue string `json:"queue,omitempty"`
}

// moveMarkOf decodes the move attribute of a row. A row with no mark, or a cleared one, returns false.
func moveMarkOf(row dbos.WorkflowStatus) (moveMark, bool) {
	raw, found := row.Attributes[moveAttribute]
	if !found || raw == nil {
		return moveMark{}, false
	}

	text, err := json.Marshal(raw)
	if err != nil {
		return moveMark{}, false
	}

	var mark moveMark

	return mark, json.Unmarshal(text, &mark) == nil && mark.To != ""
}

// storeView is the one reader of the workflow store. A run that a live mover has canceled and not yet rewound reads as open on the version it moves to, so no reader takes the cancel of a move for the end of the run.
// Every read of runs goes through it (see docs/design/durable-loop.md). dir is the state folder that holds move.lock.
type storeView struct {
	flow dbos.Client
	dir  string
}

// viewOf returns the view of the store that client reads, for the state folder dir.
func viewOf(client dbos.Client, dir string) storeView {
	return storeView{flow: client, dir: dir}
}

// store returns the view of the store that the client reads.
func (c *Client) store() storeView {
	return viewOf(c.flow, c.dir)
}

// store returns the view of the store that the engine reads. It wraps the context of the engine for reads only: the library runs a workflow only on its own context.
func (e *Engine) store() storeView {
	return viewOf(e.ctx, e.dir)
}

// List lists the runs that match opts, with a moving run read as open.
func (v storeView) List(opts ...dbos.ListWorkflowsOption) ([]dbos.WorkflowStatus, error) {
	rows, err := dbos.ListWorkflows(v.flow, opts...)
	if err != nil {
		return nil, err //nolint:wrapcheck // the caller names the read
	}

	return v.settled(rows)
}

// Exact lists the runs that match opts as the store holds them. Only a mover reads this way: it needs the true rows of its own move.
func (v storeView) Exact(opts ...dbos.ListWorkflowsOption) ([]dbos.WorkflowStatus, error) {
	return dbos.ListWorkflows(v.flow, opts...) //nolint:wrapcheck // the caller names the read
}

// Open lists the open runs that match opts: the pending and the enqueued ones, in queue when queue is not empty, and the runs that a live mover has canceled and not yet rewound.
// The status filter of the store cannot see the mark, so a second read asks for the canceled runs that match opts and keeps the moving ones. A canceled run has lost its queue, so the mark holds it.
func (v storeView) Open(queue string, opts ...dbos.ListWorkflowsOption) ([]dbos.WorkflowStatus, error) {
	first := append(slices.Clone(opts), dbos.WithFilterStatus(dbos.WorkflowStatusPending, dbos.WorkflowStatusEnqueued))
	if queue != "" {
		first = append(first, dbos.WithFilterQueueName(queue))
	}

	open, err := dbos.ListWorkflows(v.flow, first...)
	if err != nil {
		return nil, err //nolint:wrapcheck // the caller names the read
	}

	return v.addMoving(open, queue, opts)
}

// addMoving adds to open the runs that match opts and that a live mover has canceled and not yet rewound, in queue when queue is not empty. A run that open lists stays once.
func (v storeView) addMoving(open []dbos.WorkflowStatus, queue string, opts []dbos.ListWorkflowsOption) ([]dbos.WorkflowStatus, error) {
	canceled, err := dbos.ListWorkflows(v.flow, append(slices.Clone(opts), dbos.WithFilterStatus(dbos.WorkflowStatusCancelled))...)
	if err == nil {
		canceled, err = v.settled(canceled)
	}

	if err != nil {
		return nil, err //nolint:wrapcheck // the caller names the read
	}

	for _, row := range canceled {
		if isOpen(row) && (queue == "" || row.QueueName == queue) && !slices.ContainsFunc(open, func(have dbos.WorkflowStatus) bool { return have.ID == row.ID }) {
			open = append(open, row)
		}
	}

	return open, nil
}

// isOpen reports whether the row is pending or enqueued.
func isOpen(row dbos.WorkflowStatus) bool {
	return row.Status == dbos.WorkflowStatusPending || row.Status == dbos.WorkflowStatusEnqueued
}

// settled reads each canceled row that carries a mark as the mover leaves it. A row with no mark costs a map lookup.
func (v storeView) settled(rows []dbos.WorkflowStatus) ([]dbos.WorkflowStatus, error) {
	out := slices.Clone(rows)

	for i, row := range out {
		mark, marked := moveMarkOf(row)
		if row.Status != dbos.WorkflowStatusCancelled || !marked {
			continue
		}

		read, err := v.moving(row, mark)
		if err != nil {
			return nil, err
		}

		out[i] = read
	}

	return out, nil
}

// moving reads a canceled row with a mark. A held move.lock means the mover lives, and the row reads as the run that the rewind makes of it.
// A free lock means the mover ended or died: the view reads the row again under the lock, so a finished move shows its open row and a dead mover shows the cancel.
func (v storeView) moving(row dbos.WorkflowStatus, mark moveMark) (dbos.WorkflowStatus, error) {
	lock := lockFile(v.dir, moveLockName)

	taken, err := lock.TryLock()
	if err != nil {
		return row, fmt.Errorf("lock %s: %w", moveLockName, err)
	}

	if !taken {
		row.Status = dbos.WorkflowStatusPending
		row.ApplicationVersion = mark.To
		row.QueueName = mark.Queue

		return row, nil
	}

	again, err := v.reread(row)

	return again, errors.Join(err, lock.Unlock())
}

// reread reads one row again, with its input and output. It returns the row it was given when the store no longer lists it.
func (v storeView) reread(row dbos.WorkflowStatus) (dbos.WorkflowStatus, error) {
	again, err := dbos.ListWorkflows(v.flow, dbos.WithFilterWorkflowIDs(row.ID), dbos.WithFilterLoadInput(true), dbos.WithFilterLoadOutput(true))
	if err != nil || len(again) == 0 {
		return row, err //nolint:wrapcheck // the caller names the read
	}

	return again[0], nil
}

// settledStatus reads the status of the run of a handle, with a moving run read as open.
func settledStatus[R any](v storeView, handle dbos.WorkflowHandle[R]) (dbos.WorkflowStatus, error) {
	status, err := handle.GetStatus()
	if err != nil {
		return status, err //nolint:wrapcheck // the caller names the read
	}

	rows, err := v.settled([]dbos.WorkflowStatus{status})
	if err != nil {
		return status, err
	}

	return rows[0], nil
}

// retrieve returns the handle of a run. Read its status with settledStatus.
func retrieve[R any](v storeView, id string) (dbos.WorkflowHandle[R], error) {
	return dbos.RetrieveWorkflow[R](v.flow, id) //nolint:wrapcheck // the caller names the read
}
