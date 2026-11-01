package flow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/dbos-inc/dbos-transact-golang/dbos"

	"github.com/tobocop2/obee/internal/batch"
	forgepkg "github.com/tobocop2/obee/internal/forge"
	"github.com/tobocop2/obee/internal/merge"
	"github.com/tobocop2/obee/internal/review"
)

const (
	exclusiveName      = "exclusive"
	exclusiveQueueName = "exclusive"
	// gateTipName is the step that reads the standing of the pull request before a gate is queued. The per-run view names a waiting gate from it.
	gateTipName = "gate:tip"

	kindGate      = "gate"
	kindMerge     = "merge"
	kindBatchGate = "batch_gate"
)

// ExclusiveInput is one gate or one merge of a repository. Kind is "gate", "merge" or "batch_gate".
// Request and Head belong to a gate. Head and Settings belong to a merge. Commit and GateRun belong to a batch gate.
type ExclusiveInput struct {
	Kind    string `json:"kind"`
	Project string `json:"project"`
	Item    string `json:"item"`
	Head    string `json:"head,omitempty"`
	// Tip is the head of the pull request that a gate judges, set when the gate is queued. A gate of a newer head stops a gate whose Tip is another head.
	Tip      string         `json:"tip,omitempty"`
	Commit   string         `json:"commit,omitempty"`
	GateRun  []string       `json:"gate_run,omitempty"`
	Request  CommandRequest `json:"request"`
	Settings MergeSettings  `json:"settings"`
}

// ExclusiveOutput is what a gate or a merge returned. Outputs is the record of a gate. Merge is the result of a merge.
// Batch is the outcome of a batch gate. Failure is set when the call failed: the run itself ends with no error, see Failure.
type ExclusiveOutput struct {
	Outputs map[string]any    `json:"outputs,omitempty"`
	Merge   merge.Result      `json:"merge"`
	Batch   batch.GateOutcome `json:"batch"`
	Failure *Failure          `json:"failure,omitempty"`
	// Key names the gate of a gate run that passed on every step; a later gate with the same key reuses the pass. Empty for every other run.
	Key string `json:"key,omitempty"`
	// Project is the project of a run that holds a Key; the lookup reads a pass only from a run of the same project.
	Project string `json:"project,omitempty"`
}

// exclusive runs one gate or one merge. The exclusive queue holds one run for each repository at a time,
// so a merge never starts while a gate of the same repository runs, and the other way round.
// The step inside runs once: a failed step is recorded as the failure of the call, and the call does not run again.
func (e *Engine) exclusive(ctx dbos.Context, in ExclusiveInput) (ExclusiveOutput, error) {
	var out ExclusiveOutput

	run := ctx

	if in.Kind == kindGate {
		var stop func()

		run, stop = e.trackGate(ctx)
		defer stop()
	}

	out.Failure = e.failureOf(e.exclusiveCall(run, in, &out))

	return out, nil
}

// trackGate lists a running gate with the runs that the tick cancels. DBOS does not interrupt a step that runs, so a gate that a newer head canceled
// stops when the tick cancels its context. The returned function ends the gate: it cancels the context and marks the run as ended.
func (e *Engine) trackGate(parent dbos.Context) (dbos.Context, func()) { //nolint:ireturn // the context of a run is the interface of the workflow library
	run, cancel := dbos.WithCancel(parent)

	ended := make(chan struct{})

	if id, err := dbos.GetWorkflowID(parent); err == nil {
		e.cancels.add(id, openRun{id: id, cancel: cancel, ended: ended, gate: true})
	}

	return run, func() {
		cancel()
		close(ended)
	}
}

// exclusiveCall runs the call of one kind as a step and stores its result in out.
func (e *Engine) exclusiveCall(ctx dbos.Context, in ExclusiveInput, out *ExclusiveOutput) (err error) {
	switch in.Kind {
	case kindGate:
		out.Outputs, err = runStep(e, ctx, kindGate, func(c context.Context) (map[string]any, error) { return e.keyedGate(c, in, out) })
	case kindMerge:
		out.Merge, err = runStep(e, ctx, mergeStep, func(c context.Context) (merge.Result, error) {
			return e.deps.Merge.Merge(c, in.Project, in.Item, in.Head, in.Settings)
		})
	case kindBatchGate:
		out.Batch, err = runStep(e, ctx, kindBatchGate, func(c context.Context) (batch.GateOutcome, error) {
			return e.batchGateOutcome(c, in)
		})
	default:
		err = fmt.Errorf("an exclusive run has no kind %q", in.Kind) //nolint:err113 // a fault of the engine, not a state that a caller tests
	}

	return err
}

// keyedGate runs the gate and stores its key and project in out. The key is set only when the step runs here:
// a replay reads the recorded outputs and sets no key, so it can only reuse less.
func (e *Engine) keyedGate(ctx context.Context, in ExclusiveInput, out *ExclusiveOutput) (map[string]any, error) {
	outputs, key, err := e.gateOutputs(ctx, in.Request, in.Head)
	out.Key = key

	if key != "" {
		out.Project = in.Request.Project
	}

	return outputs, err
}

// queueExclusive runs a gate or a merge as a child workflow on the exclusive queue, keyed by the repository, and waits for it.
// The error is the error that the call returned, rebuilt from the failure that the child recorded.
// The batch package passes the context of the run through unchanged, so a context that is not a run is refused: a child run starts only from a run.
func (e *Engine) queueExclusive(ctx context.Context, in ExclusiveInput) (ExclusiveOutput, error) {
	run, ok := ctx.(dbos.Context)
	if !ok {
		return ExclusiveOutput{}, errNotRunContext
	}

	handle, in, err := e.enqueueExclusive(run, in)
	if err != nil {
		return ExclusiveOutput{}, err
	}

	return e.awaitExclusive(run, handle, in) //nolint:contextcheck // the wait takes the context of the run; only the generic wait helper trips the check
}

// enqueueExclusive queues the child and stops the old gates of a gate. It returns the input with the tip that the child got.
func (e *Engine) enqueueExclusive(run dbos.Context, in ExclusiveInput) (dbos.WorkflowHandle[ExclusiveOutput], ExclusiveInput, error) {
	plan, err := e.planOf(run, in)
	if err != nil {
		return nil, in, err
	}

	in.Tip = plan.Tip

	handle, err := dbos.RunWorkflow(run, e.exclusive, in, dbos.WithQueue(e.exclusiveQueue), dbos.WithQueuePartitionKey(in.Project), dbos.WithPriority(plan.Priority))
	if err != nil {
		return nil, in, fmt.Errorf("queue the %s of item %s: %w", in.Kind, in.Item, err)
	}

	return handle, in, e.stopOlderGates(run, in)
}

// awaitExclusive waits for the queued run. The error of a gate that was canceled names the head that replaced it.
func (e *Engine) awaitExclusive(run dbos.Context, handle dbos.WorkflowHandle[ExclusiveOutput], in ExclusiveInput) (ExclusiveOutput, error) {
	out, err := awaitResult(run, handle, e.deps.Clock.resultOptions())
	if errors.Is(err, dbos.ErrAwaitedWorkflowCancelled) && in.Kind == kindGate {
		return ExclusiveOutput{}, e.supersededError(run, in, err)
	}

	if err != nil {
		return ExclusiveOutput{}, err
	}

	return out, out.Failure.err(e.knownErrors())
}

// errSuperseded is the cause of a gate that stopped because a newer head of its pull request has a gate. It is not a result of the code of the old head.
var errSuperseded = errors.New("superseded")

// tipOf is the head that a gate judges: the head the call names, else the head of the pull request when the gate is queued.
// It reads no gate key, so the order of the key reads that gate reuse depends on does not change. A gate with neither has no tip.
func tipOf(in ExclusiveInput, standing review.Standing, read bool) string {
	switch {
	case in.Kind != kindGate:
		return ""
	case in.Head != "" || !read:
		return in.Head
	}

	return standing.Head
}

// queuePlan is what a call reads from the forge before it queues: the head that a gate judges and the place of the gate in the queue.
// A step records it, so a replay queues the child with the same input and the same place.
type queuePlan struct {
	Tip      string `json:"tip,omitempty"`
	Priority uint   `json:"priority"`
}

// planOf reads the pull request of a gate in a recorded step. A merge or a batch gate reads nothing and has the normal place.
func (e *Engine) planOf(run dbos.Context, in ExclusiveInput) (queuePlan, error) {
	if in.Kind != kindGate {
		return queuePlan{Priority: priorityNormal}, nil
	}

	plan, err := runStep(e, run, gateTipName, func(c context.Context) (queuePlan, error) {
		standing, read := e.gateStanding(c, in)

		return queuePlan{Tip: tipOf(in, standing, read), Priority: priorityOf(in, standing, read)}, nil
	})
	if err != nil {
		return queuePlan{}, fmt.Errorf("record the head of the pull request of item %s: %w", in.Item, err)
	}

	return plan, nil
}

// stopOlderGates cancels the old gates of a gate that has a tip, in a recorded step: a replay reads the recorded list and cancels nothing again.
func (e *Engine) stopOlderGates(run dbos.Context, in ExclusiveInput) error {
	if in.Kind != kindGate || in.Tip == "" {
		return nil
	}

	_, err := runStep(e, run, "gate:stop", func(c context.Context) ([]string, error) { return e.stopSuperseded(c, in), nil })
	if err != nil {
		return fmt.Errorf("record the stop of the old gates of item %s: %w", in.Item, err)
	}

	return nil
}

// stopSuperseded cancels each gate of the same pull request that is queued or runs for another head than the tip of in, and returns their ids.
// It reads the head again, because the read at queue time can be old: a gate cancels only while its tip is the head the forge reports now, so it never cancels
// the gate of the head. The queue then starts the gate of the new head at once: a queued gate never starts, and a running gate stops at the next tick of this engine.
// A cancel that fails is logged: the old gate then runs to its end.
func (e *Engine) stopSuperseded(ctx context.Context, in ExclusiveInput) []string {
	standing, read := e.gateStanding(ctx, in)
	if !read || standing.Head != in.Tip {
		return nil
	}

	old, err := e.olderGates(in)
	if err == nil && len(old) > 0 {
		err = dbos.CancelWorkflows(dbos.WithoutCancel(e.ctx), old)
	}

	if err != nil {
		e.logger.WarnContext(ctx, "cannot stop the old gates of the pull request, so they keep their place", "item", in.Item, "error", err)

		return nil
	}

	return old
}

// olderGates lists the ids of the queued and running gates of the pull request of in that judge another head than the tip of in.
func (e *Engine) olderGates(in ExclusiveInput) ([]string, error) {
	rows, err := dbos.ListWorkflows(dbos.WithoutCancel(e.ctx), dbos.WithFilterName(exclusiveName), dbos.WithFilterLoadInput(true),
		dbos.WithFilterStatus(dbos.WorkflowStatusEnqueued, dbos.WorkflowStatusPending))
	if err != nil {
		return nil, err //nolint:wrapcheck // stopSuperseded logs the error with its context
	}

	var old []string

	for _, row := range rows {
		if other, ok := gateInputOf(row.Input); ok && other.Project == in.Project && other.Item == in.Item && other.Tip != "" && other.Tip != in.Tip {
			old = append(old, row.ID)
		}
	}

	return old, nil
}

// gateInputOf reads the input of a queued run when it is the input of a gate.
func gateInputOf(input any) (ExclusiveInput, bool) {
	var in ExclusiveInput

	text, ok := input.(string)

	return in, ok && json.Unmarshal([]byte(text), &in) == nil && in.Kind == kindGate
}

// supersededError names the head that replaced the gate of in when the pull request now has another head. A gate that a person canceled keeps the error of the cancel.
func (e *Engine) supersededError(ctx context.Context, in ExclusiveInput, canceled error) error {
	standing, read := e.gateStanding(ctx, in)
	if !read || standing.Head == "" || standing.Head == in.Tip {
		return canceled
	}

	return fmt.Errorf("%w by %s", errSuperseded, standing.Head)
}

// The queue starts the lowest number first. Both numbers are above zero so that the queue never mixes them with a run that sets no priority.
const (
	priorityPersonWaits uint = 1
	priorityNormal      uint = 2
)

// priorityOf gives a gate the first place in the queue when a person waits on the pull request of its item:
// the pull request is open and has an open change request or an unresolved review thread. The merges and the batch gates keep the normal place.
// The read is the standing read that the lifecycle makes already. A pull request that cannot be read gets the normal place, and a line says so.
func priorityOf(in ExclusiveInput, standing review.Standing, read bool) uint {
	if in.Kind == kindGate && read && personWaits(standing) {
		return priorityPersonWaits
	}

	return priorityNormal
}

// gateStanding reads the pull request of a gate once. The second value is false for a call that is not a gate, a project with no forge and a pull request that cannot be read:
// the gate then keeps the normal place. A project with no forge says nothing, and a failed read of a forge logs a line.
func (e *Engine) gateStanding(ctx context.Context, in ExclusiveInput) (review.Standing, bool) {
	if in.Kind != kindGate || e.deps.Forge == nil {
		return review.Standing{}, false
	}

	standing, err := e.deps.Forge.Pull(ctx, in.Project, in.Item)
	if errors.Is(err, forgepkg.ErrNoForge) {
		return review.Standing{}, false
	}

	if err != nil {
		e.logger.WarnContext(ctx, "cannot read the pull request, so its gate keeps the normal place in the queue", "item", in.Item, "error", err)

		return review.Standing{}, false
	}

	return standing, true
}

// personWaits is true for an open pull request with an open change request or an unresolved review thread.
func personWaits(standing review.Standing) bool {
	return !standing.Merged && !standing.Closed && (len(standing.ChangeRequests) > 0 || standing.UnresolvedThreads > 0)
}

var errNotRunContext = errors.New("a gate or a merge runs only with the context of a workflow run")

// registerExclusiveQueue adds the queue of the gates and the merges: one run for each repository, no limit across repositories.
func (e *Engine) registerExclusiveQueue() error {
	options := []dbos.QueueOption{dbos.WithPartitionConcurrency(partitionLimit)}
	if poll := e.deps.Clock.Poll; poll > 0 {
		options = append(options, dbos.WithQueueBasePollingInterval(poll))
	}

	queue, err := dbos.RegisterQueue(e.ctx, exclusiveQueueName, options...)
	if err != nil {
		return fmt.Errorf("register the exclusive queue: %w", err)
	}

	e.exclusiveQueue = queue

	return nil
}
