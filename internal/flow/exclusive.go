package flow

import (
	"context"
	"errors"
	"fmt"

	"github.com/dbos-inc/dbos-transact-golang/dbos"

	"github.com/tobocop2/obee/internal/batch"
	"github.com/tobocop2/obee/internal/merge"
	"github.com/tobocop2/obee/internal/review"
)

const (
	exclusiveName      = "exclusive"
	exclusiveQueueName = "exclusive"

	kindGate      = "gate"
	kindMerge     = "merge"
	kindBatchGate = "batch_gate"
)

// ExclusiveInput is one gate or one merge of a repository. Kind is "gate", "merge" or "batch_gate".
// Request and Head belong to a gate. Head and Settings belong to a merge. Commit and GateRun belong to a batch gate.
type ExclusiveInput struct {
	Kind     string         `json:"kind"`
	Project  string         `json:"project"`
	Item     string         `json:"item"`
	Head     string         `json:"head,omitempty"`
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

	out.Failure = e.failureOf(e.exclusiveCall(ctx, in, &out))

	return out, nil
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

	handle, err := dbos.RunWorkflow(run, e.exclusive, in, dbos.WithQueue(e.exclusiveQueue), dbos.WithQueuePartitionKey(in.Project), dbos.WithPriority(e.exclusivePriority(ctx, in)))
	if err != nil {
		return ExclusiveOutput{}, fmt.Errorf("queue the %s of item %s: %w", in.Kind, in.Item, err)
	}

	out, err := awaitResult(run, handle, e.deps.Clock.resultOptions())
	if err != nil {
		return ExclusiveOutput{}, err
	}

	return out, out.Failure.err(e.knownErrors())
}

// The queue starts the lowest number first. Both numbers are above zero so that the queue never mixes them with a run that sets no priority.
const (
	priorityPersonWaits uint = 1
	priorityNormal      uint = 2
)

// exclusivePriority gives a gate the first place in the queue when a person waits on the pull request of its item:
// the pull request is open and has an open change request or an unresolved review thread. The merges and the batch gates keep the normal place.
// The read is the standing read that the lifecycle makes already. A pull request that cannot be read gets the normal place, and a line says so.
func (e *Engine) exclusivePriority(ctx context.Context, in ExclusiveInput) uint {
	if in.Kind != kindGate || e.deps.Forge == nil {
		return priorityNormal
	}

	standing, err := e.deps.Forge.Pull(ctx, in.Project, in.Item)
	if err != nil {
		e.logger.WarnContext(ctx, "cannot read the pull request, so its gate keeps the normal place in the queue", "item", in.Item, "error", err)

		return priorityNormal
	}

	if personWaits(standing) {
		return priorityPersonWaits
	}

	return priorityNormal
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
