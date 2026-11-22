package flow

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
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

// openStart returns the first look at the caps or start mark after the last agent step, or the length of the history when there is none.
func openStart(steps []dbos.StepInfo) int {
	return firstOpenStart(steps, len(steps))
}

// firstOpenStart returns the first look at the caps or start mark before the step limit that has no agent step between it and the limit, or limit when there is none.
func firstOpenStart(steps []dbos.StepInfo, limit int) int {
	open := limit

	for _, step := range steps {
		switch {
		case step.StepID >= limit:
		case step.StepName == agentStepName:
			open = limit
		case step.StepName == capLookStepName || startNumber(step.StepName) == 1:
			open = min(open, step.StepID)
		}
	}

	return open
}

// raised returns the ids of the runs that a gather step of the run canceled when its raise fired. It reads the record before the rewind drops the step.
// The store keeps the output of a step as JSON text; a step that failed or that holds no record adds nothing, and so do the deadline step and the
// deadline cancel step of a gather.
func raised(steps []dbos.StepInfo) map[string]bool {
	ids := map[string]bool{}

	for _, step := range steps {
		text, ok := step.Output.(string)
		if !ok || step.Error != nil || !isGatherStep(step.StepName) {
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

// isGatherStep reports whether a step is a wait step of a gather call: gather:<label>#<n>, or the wait gather:<label>#<n>.<k> after a retry. The deadline step
// and the deadline cancel step recorded beside it are no wait.
func isGatherStep(name string) bool {
	kind, _ := kindOfStep(name)

	return kind == gatherKind && !strings.HasSuffix(name, deadlineSuffix) && !strings.HasSuffix(name, cancelSuffix)
}

// failedStep returns the first step that failed or that started a child that ended without success. A run with no such step replays from the end of its history.
// It reopens every such child, not only the first: a gather that raised canceled the children that were still open, and each of them must run again.
func (c *Client) failedStep(steps []dbos.StepInfo, canceled map[string]bool, opts ...dbos.RewindWorkflowOption) (int, error) {
	first := len(steps)
	replaced := superseded(steps)

	for _, step := range steps {
		child, err := c.reopenChild(reopenable(step, replaced), canceled, opts...)
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

// superseded returns the item runs that the run started and then replaced with a later attempt of the same item, as gather does for a child with retries.
// A resume reopens only the last attempt of each item, so no replaced attempt runs beside it.
func superseded(steps []dbos.StepInfo) map[string]bool {
	last := map[string]string{}

	for _, step := range steps {
		if item, ok := itemOfRun(step.ChildWorkflowID); ok {
			last[item] = step.ChildWorkflowID
		}
	}

	replaced := map[string]bool{}

	for _, step := range steps {
		if item, ok := itemOfRun(step.ChildWorkflowID); ok && last[item] != step.ChildWorkflowID {
			replaced[step.ChildWorkflowID] = true
		}
	}

	return replaced
}

// reopenable returns the child of the step that a resume may reopen. An attempt that a later attempt of its item replaced gives none.
func reopenable(step dbos.StepInfo, replaced map[string]bool) string {
	if replaced[step.ChildWorkflowID] {
		return ""
	}

	return step.ChildWorkflowID
}

// itemOfRun returns the item of an item run id. ok is false for an id that runID did not make, such as the id of a phase.
func itemOfRun(id string) (item string, ok bool) {
	rest, found := strings.CutPrefix(id, runPrefix)
	at := strings.LastIndex(rest, attemptMarker)

	if !found || at < 0 {
		return "", false
	}

	_, ok = attemptOf(rest[at:], attemptMarker)

	return rest[:at], ok
}

// rewindPoint returns the step that a rewind goes back to: the look at the caps that opens the phase of a failed agent step, the deadline step of the wait
// of a failed receive, else the failed step.
func rewindPoint(steps []dbos.StepInfo, failed dbos.StepInfo) int {
	switch failed.StepName {
	case agentStepName:
		return firstOpenStart(steps, failed.StepID)
	case receiveStepName:
		return waitStart(steps, failed.StepID)
	default:
		return failed.StepID
	}
}

// waitStart returns the deadline step of the wait that the receive at step failed belongs to, so a resumed wait records a fresh deadline.
// Only receives and their sleeps may lie between the two; else the receive keeps its own step.
func waitStart(steps []dbos.StepInfo, failed int) int {
	at := slices.IndexFunc(steps, func(step dbos.StepInfo) bool { return step.StepID == failed }) - 1
	for at >= 0 && (steps[at].StepName == receiveStepName || steps[at].StepName == sleepStep) {
		at--
	}

	if at >= 0 && steps[at].StepName == deadlineStep {
		return steps[at].StepID
	}

	return failed
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
	in, owner, err := c.childClaim(row)
	if err != nil {
		return err
	}

	// Every run that a step started is a child, at depth 1 or more. A child that an older build started records no depth and ran at depth 1.
	if err := c.reopenAgain(row, itemsQueueOf(max(in.Depth, 1)), opts...); err != nil {
		return errors.Join(err, c.dropClaim(in.Project, in.Item, row.ID, owner))
	}

	return nil
}

// reopenAgain puts one child back to work. DBOS resumes a canceled run, on queue, the items queue of its depth, so that the parent can start it again by the same id, and rewinds a failed one.
// A move to this build (opts holds the restamp) rewinds a canceled run too, because only a rewind restamps a run.
func (c *Client) reopenAgain(row dbos.WorkflowStatus, queue string, opts ...dbos.RewindWorkflowOption) error {
	if row.Status != dbos.WorkflowStatusCancelled {
		return c.reopen(row.ID, opts...)
	}

	if err := c.reopenCanceledPhases(row.ID, opts...); err != nil {
		return err
	}

	if len(opts) > 0 {
		// A canceled row has lost its queue name, so the rewind names the items queue itself.
		return c.reopen(row.ID, append([]dbos.RewindWorkflowOption{dbos.WithRewindQueue(queue)}, opts...)...)
	}

	_, err := dbos.ResumeWorkflow[ItemResult](c.flow, row.ID, dbos.WithResumeQueue(queue))

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
	if err != nil || (phase.Name != phaseName && phase.Name != exclusiveName && phase.Name != hostJobName) || stateOf(phase.Status) != runCancelled {
		return err
	}

	// A gate that runs on a build host waits for a build job. The job goes back to work first, so the gate that replays finds it queued.
	if phase.Name == exclusiveName {
		if err := c.reopenCanceledPhases(id, opts...); err != nil {
			return err
		}
	}

	return c.reopen(id, opts...)
}

// childClaim takes a fresh claim for a failed item run that the parent started, as Resume does for the run it reopens: the child replays its recorded claim step,
// whose claim was released. It returns the input of the child and the owner of the claim. A child that is not an item run takes none, and its owner is empty.
func (c *Client) childClaim(row dbos.WorkflowStatus) (ItemInput, string, error) {
	if row.Name != itemName {
		return ItemInput{}, "", nil
	}

	child := run{Status: row}

	in, err := child.input()
	if err != nil {
		return ItemInput{}, "", err
	}

	_, owner, err := c.takeClaim(in.Item, child)

	return in, owner, err
}

// rewind drops the history of the run from one step on and queues the run again. A phase goes back to the queue of its kind, in the partition of its login.
// The rewind keeps every message: a message that a dropped step took is unread again, and an unread message stays.
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
	_, err = dbos.RewindWorkflow[any](c.flow, id, append(append(queued, dbos.WithRewindStartStep(uint(from)), dbos.WithRewindKeepMessages()), opts...)...)

	return err
}

// rewindQueue names the queue that a rewound run goes back to. A phase goes to the queue of its kind, in the partition of its login.
// A run that spawn started goes back to the items queue it ran on, the queue of its depth: the parent starts it again by the same id, and the library refuses an id that moved to another queue.
func rewindQueue(row dbos.WorkflowStatus) ([]dbos.RewindWorkflowOption, error) {
	switch {
	case row.Name == phaseName:
		queue, partition, err := phaseRewindTarget(row)
		if err != nil {
			return nil, err
		}

		return []dbos.RewindWorkflowOption{dbos.WithRewindQueue(queue), dbos.WithRewindQueuePartitionKey(partition)}, nil
	case row.Name == exclusiveName || row.Name == hostJobName:
		queue, partition, err := gateRewindTarget(row)
		if err != nil {
			return nil, err
		}

		return []dbos.RewindWorkflowOption{dbos.WithRewindQueue(queue), dbos.WithRewindQueuePartitionKey(partition)}, nil
	case row.Name == itemName && row.QueueName != "":
		return []dbos.RewindWorkflowOption{dbos.WithRewindQueue(row.QueueName)}, nil
	}

	return nil, nil
}

// gateRewindTarget returns the queue and the partition that a rewound gate, merge or build job goes back to. A gate or a merge goes to the exclusive queue,
// in the partition of its project. A build job goes to the host queue, in the partition that it recorded in its input.
// The host job of a command call with a slot class goes to the queue of its class, in the same partition.
func gateRewindTarget(row dbos.WorkflowStatus) (queue, partition string, err error) {
	var in ExclusiveInput
	if err := json.Unmarshal([]byte(fmt.Sprint(row.Input)), &in); err != nil {
		return "", "", fmt.Errorf("read the input of %s: %w", row.ID, err)
	}

	if row.Name == hostJobName && in.Kind == kindCommand {
		return slotQueuePrefix + in.Request.SlotClass, in.HostKey, nil
	}

	if row.Name == hostJobName {
		return hostQueueName, in.HostKey, nil
	}

	return exclusiveQueueName, in.Project, nil
}

// phaseRewindTarget returns the queue and the partition key that a rewound phase goes back to: the queue of its step and the partition of its login.
func phaseRewindTarget(row dbos.WorkflowStatus) (queue, partition string, err error) {
	var in PhaseInput
	if err := json.Unmarshal([]byte(fmt.Sprint(row.Input)), &in); err != nil {
		return "", "", fmt.Errorf("read the input of %s: %w", row.ID, err)
	}

	return agentQueueName(in.NoEdits), in.Login, nil
}

// errNoRow is returned when a workflow the store listed is gone.
var errNoRow = errors.New("the workflow is not in the store")

// row reads one workflow with its input, as the store holds it: a reopen needs the true state of the run, whether a mover marked it or not.
func (c *Client) row(id string) (dbos.WorkflowStatus, error) {
	rows, err := c.store().Exact(dbos.WithFilterWorkflowIDs(id), dbos.WithFilterLoadInput(true))
	if err != nil {
		return dbos.WorkflowStatus{}, fmt.Errorf("read workflow %s: %w", id, err)
	}

	if len(rows) == 0 {
		return dbos.WorkflowStatus{}, fmt.Errorf("%w: %s", errNoRow, id)
	}

	return rows[0], nil
}
