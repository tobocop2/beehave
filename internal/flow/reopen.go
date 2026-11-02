package flow

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
)

// reopen puts a failed run back in the queue at its first failed step. The steps before it keep their results.
// A phase that failed is reopened first, so the rerun of the item joins it instead of reading its old error.
func (c *Client) reopen(id string, opts ...dbos.RewindWorkflowOption) error {
	steps, err := dbos.GetWorkflowSteps(c.flow, id, dbos.WithStepsLoadOutput(true)) // the output of the gather step names the runs that its raise canceled
	if err != nil {
		return fmt.Errorf("read the steps of %s: %w", id, err)
	}

	from, err := c.failedStep(steps, raised(steps), opts...)
	if err != nil {
		return err
	}

	return c.rewind(id, min(from, openStart(steps)), opts...)
}

// openStart returns the step of the first start mark of the open sequence, or the length of the history when there is none. The open sequence is
// the start marks after the last agent step. A run that stopped in the middle of the agent step, such as a canceled one, holds such a sequence, and
// a loss in it leaves marks and loss records. Every rewind goes back to the first mark, so the whole sequence goes and a resumed phase starts clean.
func openStart(steps []dbos.StepInfo) int {
	return firstOpenStart(steps, len(steps))
}

// firstOpenStart returns the step of the first start mark before the step limit that has no agent step between it and the limit, or limit when there is none.
func firstOpenStart(steps []dbos.StepInfo, limit int) int {
	open := limit

	for _, step := range steps {
		switch {
		case step.StepID >= limit:
		case step.StepName == agentStepName:
			open = limit
		case startNumber(step.StepName) == 1:
			open = min(open, step.StepID)
		}
	}

	return open
}

// raised returns the ids of the runs that a gather step of the run canceled when its raise fired. It reads the record before the rewind drops the step.
// The store keeps the output of a step as JSON text; a step that failed or that holds no record adds nothing.
func raised(steps []dbos.StepInfo) map[string]bool {
	ids := map[string]bool{}

	for _, step := range steps {
		text, ok := step.Output.(string)
		if !ok || step.Error != nil || !strings.HasPrefix(step.StepName, "gather") {
			continue
		}

		var done gathered
		if json.Unmarshal([]byte(text), &done) != nil {
			continue
		}

		for _, id := range done.Canceled {
			ids[id] = true
		}
	}

	return ids
}

// failedStep returns the first step that failed or that started a child that ended without success. A run with no such step replays from the end of its history.
// It reopens every such child, not only the first: a gather that raised canceled the children that were still open, and each of them must run again.
func (c *Client) failedStep(steps []dbos.StepInfo, canceled map[string]bool, opts ...dbos.RewindWorkflowOption) (int, error) {
	first := len(steps)

	for _, step := range steps {
		child, err := c.reopenChild(step.ChildWorkflowID, canceled, opts...)
		if err != nil {
			return 0, err
		}

		if !child && step.Error == nil {
			continue
		}

		first = min(first, rewindPoint(steps, step))
	}

	return first, nil
}

// rewindPoint returns the step that a rewind goes back to for a failed step. A failed agent step goes back to the first start mark of its sequence,
// so the rewind drops every mark and loss record with the step, and a rewound phase holds no start that has no end. Any other step goes back to itself.
func rewindPoint(steps []dbos.StepInfo, failed dbos.StepInfo) int {
	if failed.StepName != agentStepName {
		return failed.StepID
	}

	return firstOpenStart(steps, failed.StepID)
}

// reopenChild reopens the child workflow of a step when it failed, or when it is an item run that the raise of a gather canceled, and reports whether it did.
// A parent that raised after a gather canceled its open children, so a resume of the parent must reopen them with the failed one.
// A child that a user canceled by hand is not in canceled and stays canceled. An empty id is a step with no child.
// In a move to this build (opts holds the restamp) every canceled item run is reopened: the cancel of the move reached it.
func (c *Client) reopenChild(id string, canceled map[string]bool, opts ...dbos.RewindWorkflowOption) (bool, error) {
	if id == "" {
		return false, nil
	}

	row, err := c.row(id)
	if err != nil {
		return false, err
	}

	state := stateOf(row.Status)
	if state != runFailed && (state != runCancelled || row.Name != itemName || (!canceled[id] && len(opts) == 0)) {
		return false, nil
	}

	return true, c.reopenClaimed(row, opts...)
}

// reopenClaimed reopens a failed or canceled child with a fresh claim. The claim goes back when the child does not reopen.
func (c *Client) reopenClaimed(row dbos.WorkflowStatus, opts ...dbos.RewindWorkflowOption) error {
	project, item, owner, err := c.childClaim(row)
	if err != nil {
		return err
	}

	if err := c.reopenAgain(row, opts...); err != nil {
		return errors.Join(err, c.dropClaim(project, item, row.ID, owner))
	}

	return nil
}

// reopenAgain puts one child back to work. DBOS resumes a canceled run, on the items queue so that the parent can start it again by the same id, and rewinds a failed one.
// A move to this build (opts holds the restamp) rewinds a canceled run too, because only a rewind restamps a run.
func (c *Client) reopenAgain(row dbos.WorkflowStatus, opts ...dbos.RewindWorkflowOption) error {
	if row.Status != dbos.WorkflowStatusCancelled {
		return c.reopen(row.ID, opts...)
	}

	if err := c.reopenCanceledPhases(row.ID, opts...); err != nil {
		return err
	}

	if len(opts) > 0 {
		// A canceled row has lost its queue name, so the rewind names the items queue itself.
		return c.reopen(row.ID, append([]dbos.RewindWorkflowOption{dbos.WithRewindQueue(itemsQueueName)}, opts...)...)
	}

	_, err := dbos.ResumeWorkflow[ItemResult](c.flow, row.ID, dbos.WithResumeQueue(itemsQueueName))

	return err
}

// reopenCanceledPhases puts back to work each phase of a canceled item that the cancel reached. The item replays its steps, and a phase that stays canceled would fail it again.
func (c *Client) reopenCanceledPhases(id string, opts ...dbos.RewindWorkflowOption) error {
	steps, err := dbos.GetWorkflowSteps(c.flow, id)
	if err != nil {
		return fmt.Errorf("read the steps of %s: %w", id, err)
	}

	for _, step := range steps {
		if step.ChildWorkflowID == "" {
			continue
		}

		if err := c.reopenCanceledPhase(step.ChildWorkflowID, opts...); err != nil {
			return err
		}
	}

	return nil
}

// reopenCanceledPhase puts one child back to work when it is a phase, or a gate or a merge, that was canceled.
func (c *Client) reopenCanceledPhase(id string, opts ...dbos.RewindWorkflowOption) error {
	phase, err := c.row(id)
	if err != nil || (phase.Name != phaseName && phase.Name != exclusiveName) || stateOf(phase.Status) != runCancelled {
		return err
	}

	return c.reopen(id, opts...)
}

// childClaim takes a fresh claim for a failed item run that the parent started, as Resume does for the run it reopens: the child replays its recorded claim step,
// whose claim was released. A child that is not an item run takes none, and its owner is empty.
func (c *Client) childClaim(row dbos.WorkflowStatus) (project, item, owner string, err error) {
	if row.Name != itemName {
		return "", "", "", nil
	}

	child := run{Status: row}

	in, err := child.input()
	if err != nil {
		return "", "", "", err
	}

	project, owner, err = c.takeClaim(in.Item, child)

	return project, in.Item, owner, err
}

// rewind drops the history of the run from one step on and queues the run again. A phase goes back to the queue of its kind, in the partition of its login.
func (c *Client) rewind(id string, from int, opts ...dbos.RewindWorkflowOption) error {
	row, err := c.row(id)
	if err != nil {
		return err
	}

	queued, err := rewindQueue(row)
	if err != nil {
		return err
	}

	// A rewind keeps the application version of the run. Only the caller that passed the pin check adds the option that restamps the run.
	_, err = dbos.RewindWorkflow[any](c.flow, id, append(append(queued, dbos.WithRewindStartStep(uint(from))), opts...)...)

	return err
}

// rewindQueue names the queue that a rewound run goes back to. A phase goes to the queue of its kind, in the partition of its login.
// A run that spawn started goes back to the items queue: the parent starts it again by the same id, and the library refuses an id that moved to another queue.
func rewindQueue(row dbos.WorkflowStatus) ([]dbos.RewindWorkflowOption, error) {
	switch {
	case row.Name == phaseName:
		queue, partition, err := phaseRewindTarget(row)
		if err != nil {
			return nil, err
		}

		return []dbos.RewindWorkflowOption{dbos.WithRewindQueue(queue), dbos.WithRewindQueuePartitionKey(partition)}, nil
	case row.Name == exclusiveName:
		var in ExclusiveInput
		if err := json.Unmarshal([]byte(fmt.Sprint(row.Input)), &in); err != nil {
			return nil, fmt.Errorf("read the input of %s: %w", row.ID, err)
		}

		return []dbos.RewindWorkflowOption{dbos.WithRewindQueue(exclusiveQueueName), dbos.WithRewindQueuePartitionKey(in.Project)}, nil
	case row.QueueName == itemsQueueName:
		return []dbos.RewindWorkflowOption{dbos.WithRewindQueue(itemsQueueName)}, nil
	}

	return nil, nil
}

// phaseRewindTarget returns the queue and the partition key that a rewound phase goes back to: the queue of its kind and the partition of its login.
func phaseRewindTarget(row dbos.WorkflowStatus) (queue, partition string, err error) {
	var in PhaseInput
	if err := json.Unmarshal([]byte(fmt.Sprint(row.Input)), &in); err != nil {
		return "", "", fmt.Errorf("read the input of %s: %w", row.ID, err)
	}

	return agentQueueName(in.Kind), in.Login, nil
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
