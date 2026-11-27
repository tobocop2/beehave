package flow

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/dbos-inc/dbos-transact-golang/dbos"

	"github.com/tobocop2/beehest/internal/workflow"
)

// pinAttribute names the attribute of a run that holds the files it replays against after an adoption: the workflow file and each file it loads, as the author left them.
const pinAttribute = "pin"

// pinOf returns the files that a run replays against: the pin that an adoption wrote on its row, else the pin that the input recorded at the start.
// An attribute that is no pin is an error, never the input: the run would otherwise replay a file that its author did not adopt.
func pinOf(row dbos.WorkflowStatus, in ItemInput) ([]workflow.PinnedFile, error) {
	raw, set := row.Attributes[pinAttribute]
	if !set || raw == nil {
		return in.Files, nil
	}

	var pin []workflow.PinnedFile

	text, marshalErr := json.Marshal(raw)
	if err := errors.Join(marshalErr, json.Unmarshal(text, &pin)); err != nil {
		return nil, fmt.Errorf("read the pin of run %s: %w", row.ID, err)
	}

	return pin, nil
}

// pinInForce reads the files that the run of ctx replays against. The store holds the row that adoption wrote.
func (e *Engine) pinInForce(ctx dbos.Context, in ItemInput) ([]workflow.PinnedFile, error) {
	id := workflowID(ctx)

	rows, err := e.store().List(dbos.WithFilterWorkflowIDs(id))
	if err != nil || len(rows) == 0 {
		return nil, fmt.Errorf("read the pin of run %q: %w", id, cmp.Or(err, errNoRow))
	}

	return pinOf(rows[0], in)
}

// setPin writes the pin on the run and on every item run below it, so a child that a reopen replays reads the file that its parent adopted.
// A phase, a gate or a merge below holds no workflow file and gets none. No pin writes nothing. The caller holds probe.lock.
func (c *Client) setPin(last run, pin []workflow.PinnedFile) error {
	if pin == nil {
		return nil
	}

	below, err := c.rowsUnder(last.Status.ID)
	if err != nil {
		return err
	}

	below = slices.DeleteFunc(below, func(row dbos.WorkflowStatus) bool { return row.Name != itemName })

	for _, row := range append([]dbos.WorkflowStatus{last.Status}, below...) {
		if err := dbos.SetWorkflowAttributes(c.flow, row.ID, map[string]any{pinAttribute: pin}, dbos.WithMergeAttributes()); err != nil {
			return fmt.Errorf("pin the workflow file for run %s: %w", row.ID, err)
		}
	}

	return nil
}

// replayMismatch reports whether err says that a replay met a step that the file no longer makes: a recorded step of another name, or a step id recorded twice.
// The store raises it before the body of the step runs, so no step ran on the new file.
func replayMismatch(err error) bool {
	return errors.Is(err, &dbos.Error{Code: dbos.ErrorCodeUnexpectedStep}) || errors.Is(err, dbos.ErrStepNondeterminism)
}
