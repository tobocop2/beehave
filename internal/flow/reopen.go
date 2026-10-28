package flow

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
)

// reopen puts a failed run back in the queue at its first failed step. The steps before it keep their results.
// A phase that failed is reopened first, so the rerun of the item joins it instead of reading its old error.
func (c *Client) reopen(id string) error {
	steps, err := dbos.GetWorkflowSteps(c.flow, id)
	if err != nil {
		return fmt.Errorf("read the steps of %s: %w", id, err)
	}

	from, err := c.failedStep(steps)
	if err != nil {
		return err
	}

	return c.rewind(id, from)
}

// failedStep returns the first step that failed or that started a phase that failed. A run with no such step replays from the end of its history.
func (c *Client) failedStep(steps []dbos.StepInfo) (int, error) {
	for _, step := range steps {
		child, err := c.reopenChild(step.ChildWorkflowID)
		if err != nil {
			return 0, err
		}

		if child || step.Error != nil {
			return step.StepID, nil
		}
	}

	return len(steps), nil
}

// reopenChild reopens the child workflow of a step when it failed, and reports whether it did. An empty id is a step with no child.
func (c *Client) reopenChild(id string) (bool, error) {
	if id == "" {
		return false, nil
	}

	row, err := c.row(id)
	if err != nil {
		return false, err
	}

	if stateOf(row.Status) != RunFailed {
		return false, nil
	}

	return true, c.reopen(id)
}

// rewind drops the history of the run from one step on and queues the run again. A phase goes back to the agents queue, in the partition of its login.
func (c *Client) rewind(id string, from int) error {
	row, err := c.row(id)
	if err != nil {
		return err
	}

	options := []dbos.RewindWorkflowOption{dbos.WithRewindStartStep(uint(from))}

	if row.Name == phaseName {
		var in PhaseInput
		if err := json.Unmarshal([]byte(fmt.Sprint(row.Input)), &in); err != nil {
			return fmt.Errorf("read the input of %s: %w", id, err)
		}

		options = append(options, dbos.WithRewindQueue(queueName), dbos.WithRewindQueuePartitionKey(in.Login))
	}

	_, err = dbos.RewindWorkflow[any](c.flow, id, options...)

	return err
}

// errNoRow is returned when a workflow the store listed is gone.
var errNoRow = errors.New("the workflow is not in the store")

// row reads one workflow with its input.
func (c *Client) row(id string) (dbos.WorkflowStatus, error) {
	rows, err := dbos.ListWorkflows(c.flow, dbos.WithFilterWorkflowIDs(id), dbos.WithFilterLoadInput(true))
	if err != nil {
		return dbos.WorkflowStatus{}, fmt.Errorf("read workflow %s: %w", id, err)
	}

	if len(rows) == 0 {
		return dbos.WorkflowStatus{}, fmt.Errorf("%w: %s", errNoRow, id)
	}

	return rows[0], nil
}
