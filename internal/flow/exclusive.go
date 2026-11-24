package flow

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/dbos-inc/dbos-transact-golang/dbos"

	"github.com/tobocop2/beehave/internal/batch"
	"github.com/tobocop2/beehave/internal/config"
	forgepkg "github.com/tobocop2/beehave/internal/forge"
	"github.com/tobocop2/beehave/internal/merge"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/review"
)

const (
	exclusiveName      = "exclusive"
	exclusiveQueueName = "exclusive"
	// gateTipName is the step that reads the standing of the pull request before a gate is queued. The per-run view names a waiting gate from it.
	gateTipName = "gate:tip"
	// gateJobName is the step that records the host job of a gate, or the earlier pass that the gate reuses, before the gate waits.
	gateJobName = "gate:job"
	// gateReuseName is the step that reuses an earlier pass before a gate that runs on a build host waits for the host.
	gateReuseName = "gate:reuse"
	// batchGateJobName is the step that checks out the union commit of a batch gate and records its tree and its host job before the gate waits.
	batchGateJobName = "batch_gate:job"

	kindGate      = "gate"
	kindMerge     = "merge"
	kindBatchGate = "batch_gate"
	// kindCommand is a command call that names a slot class. It runs as a host job on the queue of its class and never on the exclusive queue.
	kindCommand = "command"
)

// ExclusiveInput is one gate or one merge of a repository, or the host job of a command call with a slot class. Kind is "gate", "merge", "batch_gate" or "command".
// Request belongs to a command call. Request and Head belong to a gate. Head and Settings belong to a merge. Commit and GateRun belong to a batch gate.
// Run is the id of the run that queued the merge or the command, and RunItem is the item whose claim that run holds. A call with no Run is refused.
type ExclusiveInput struct {
	Kind    string `json:"kind"`
	Run     string `json:"run,omitempty"`
	RunItem string `json:"run_item,omitempty"`
	Project string `json:"project"`
	Item    string `json:"item"`
	// BuildHost is the build host that the run recorded in host_up, for a batch gate. A gate carries it in Request.
	BuildHost string `json:"build_host,omitempty"`
	Head      string `json:"head,omitempty"`
	// Severity is the severity of the item of a gate, which sets its place in the queue. It is empty for a merge and a batch gate.
	Severity string `json:"severity,omitempty"`
	// Tip is the head of the pull request that a gate judges, set when the gate is queued. A gate of a newer head stops a gate whose Tip is another head.
	Tip string `json:"tip,omitempty"`
	// Priority is the place of the gate in the queue, set when the gate is queued. The build job of the gate takes the same place on the slot queue of the class gate.
	Priority uint `json:"priority,omitempty"`
	// HostKey is the partition of the slot queue that holds the build job of the gate. It is set when the gate is queued, so a replay uses the host that the first
	// run chose even when the project file changed since. It is empty for a gate that runs on this machine. A reopened job goes back to the same partition.
	HostKey string `json:"host_key,omitempty"`
	// Hosts are the declared build hosts that the gate may go to, set when the gate is queued for a project with more than one host and a run that recorded none.
	// The step gate:place picks one when the gate leaves the exclusive queue, and the gate then holds it in HostKey. It is empty for every other call.
	Hosts []string `json:"hosts,omitempty"`
	// SlotCount is the count of the slot class of a command call or a gate, set when the job is queued. A gate reads it to name its queue, and the rewind of a reopened job reads it too.
	// A command replay asks Deps.Slots again, so a changed host block gives the replay the new count.
	SlotCount int `json:"slot_count,omitempty"`
	// Call is the key of the gate call, such as gate:check, which a dry run reads to script the gate. It is empty for a batch gate and a merge.
	Call string `json:"call,omitempty"`
	// PinBase is the base of the merge call, for a batch gate: the pin file is read at that branch. A gate carries it in Request.
	PinBase  string         `json:"pin_base,omitempty"`
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
	// Command is the output of a command call with a slot class.
	Command CommandOutput `json:"command"`
	Failure *Failure      `json:"failure,omitempty"`
	// Key names the gate of a gate run that passed on every step; a later gate with the same key reuses the pass. Empty for every other run.
	Key string `json:"key,omitempty"`
	// Project is the project of a run that holds a Key; the lookup reads a pass only from a run of the same project.
	Project string `json:"project,omitempty"`
	// Lost is the host job of a gate or a batch gate whose wait ended as lost, after the wait for its proof. It is nil for every other run.
	Lost *LostJob `json:"lost,omitempty"`
	// HostStarted is the cluster that this gate started because host_up had left it for the first gate. It is empty for every other run.
	HostStarted string `json:"host_started,omitempty"`
}

// exclusive runs one gate or one merge. The exclusive queue holds one run for each repository at a time,
// so a merge never starts while a gate of the same repository runs, and the other way round.
// The step inside runs once: a failed step is recorded as the failure of the call, and the call does not run again.
func (e *Engine) exclusive(ctx dbos.Context, in ExclusiveInput) (ExclusiveOutput, error) {
	var out ExclusiveOutput

	run, stop := e.trackGate(ctx, in)
	defer stop()

	return e.endCall(&out, e.exclusiveCall(run, in, &out))
}

// endCall ends an exclusive run or a host job: the error of its call is the Failure of the output, unless the engine stopped.
func (e *Engine) endCall(out *ExclusiveOutput, err error) (ExclusiveOutput, error) {
	if stop := engineStop(e.ctx, err); stop != nil {
		return ExclusiveOutput{}, stop
	}

	out.Failure = e.failureOf(err)

	return *out, nil
}

// trackGate lists a running gate or batch gate with the runs that the tick cancels. DBOS does not interrupt a step that runs, so a gate that a newer head
// or a person canceled stops when the tick cancels its context, and a wait for the proof of a lost job reads the proof once more (lostStopped).
// The returned function ends the gate: it cancels the context and marks the run as ended. A run of another kind is not listed.
func (e *Engine) trackGate(parent dbos.Context, in ExclusiveInput) (dbos.Context, func()) { //nolint:ireturn // the context of a run is the interface of the workflow library
	if in.Kind != kindGate && in.Kind != kindBatchGate {
		return parent, func() {}
	}

	run, cancel := dbos.WithCancel(parent)

	a := &attempt{cancel: cancel, ended: make(chan struct{}), gate: true}

	if id, err := dbos.GetWorkflowID(parent); err == nil {
		a.id = id
		e.cancels.add(a)
	}

	return run, func() {
		cancel()
		close(a.ended)
	}
}

// exclusiveCall runs the call of one kind and stores its result in out. A gate that runs on a build host runs as a job on the slot queue of the class gate,
// unless an earlier pass stands in for it: that check comes first, so a gate that needs no build never waits for a host.
func (e *Engine) exclusiveCall(ctx dbos.Context, in ExclusiveInput, out *ExclusiveOutput) error {
	if in.HostKey == "" {
		return e.runCall(ctx, in, out)
	}

	if reused, err := e.reuseBeforeHost(ctx, in, out); reused || err != nil {
		return err
	}

	return e.onHost(ctx, in, out)
}

// reuseBeforeHost reuses the pass of an earlier gate with the same key, in a recorded step, and says whether it did. Only a project gate has a key.
// The job checks again when it starts, so a pass that ends while the gate waits for the host still stands in for it.
func (e *Engine) reuseBeforeHost(ctx dbos.Context, in ExclusiveInput, out *ExclusiveOutput) (bool, error) {
	if in.Kind != kindGate || len(in.Request.Run) > 0 {
		return false, nil
	}

	start, err := runStep(e, ctx, gateReuseName, func(c context.Context) (gateStart, error) {
		started, err := e.reuseProjectGate(c, in.Request)
		if started.Reused != nil {
			out.setKey(started.Key.ID, in.Request.Project)
		}

		return started, err
	})
	if err != nil || start.Reused == nil {
		return false, err
	}

	out.Outputs = start.Reused

	return true, nil
}

// queuedClaim is the claim read of an exclusive call: it waits for the claim of the run that queued the call.
func (e *Engine) queuedClaim(in ExclusiveInput) func(context.Context) (context.Context, error) {
	return func(ctx context.Context) (context.Context, error) {
		claim, _, err := e.awaitClaim(ctx, PhaseInput{Run: in.Run, Item: in.RunItem})

		return claim, err
	}
}

// claimedMerge merges under the claim of the run that queued the merge.
func (e *Engine) claimedMerge(ctx context.Context, in ExclusiveInput) (result merge.Result, err error) {
	err = claimedCall(ctx, e.queuedClaim(in), func(c context.Context) error {
		result, err = e.deps.Merge.Merge(c, in.Project, in.Item, in.Head, in.Settings)

		return err
	})

	return result, err
}

// claimedCommand runs the program of a command job under the claim of the run that queued it.
func (e *Engine) claimedCommand(ctx context.Context, in ExclusiveInput) (out CommandOutput, err error) {
	request := in.Request
	request.SlotCount = in.SlotCount

	err = claimedCall(ctx, e.queuedClaim(in), func(c context.Context) error {
		out, err = e.deps.Commands.Run(c, request)

		return err
	})

	return out, err
}

// runCall runs the call of one kind as recorded steps and stores its result in out.
func (e *Engine) runCall(ctx dbos.Context, in ExclusiveInput, out *ExclusiveOutput) (err error) {
	switch in.Kind {
	case kindGate:
		out.Outputs, err = e.gateCall(ctx, in, out)
	case kindMerge:
		out.Merge, err = runStep(e, ctx, mergeStep, func(c context.Context) (merge.Result, error) { return e.claimedMerge(c, in) })
	case kindBatchGate:
		out.Batch, err = e.batchGateCall(ctx, in, out)
	case kindCommand:
		out.Command, err = runStep(e, ctx, kindCommand, func(c context.Context) (CommandOutput, error) { return e.claimedCommand(c, in) })
	default:
		err = fmt.Errorf("an exclusive run has no kind %q", in.Kind) //nolint:err113 // a fault of the engine, not a state that a caller tests
	}

	return err
}

// gateCall runs the gate of a call as recorded steps. A call that names a program is one step.
// A project gate is two: "gate:job" reuses an earlier pass or submits the host job, and "gate" waits for that job. DBOS cannot record from inside a step,
// so the job id is the output of a step of its own. A crash between the two leaves the id on record: the replay skips the first step and the second
// reads the same job, so the host gets one job. A crash inside the first step, between the host accepting the job and the record, can still submit twice.
// The key is set only when the step runs here: a replay reads the recorded outputs and sets no key, so it can only reuse less.
func (e *Engine) gateCall(ctx dbos.Context, in ExclusiveInput, out *ExclusiveOutput) (map[string]any, error) {
	if len(in.Request.Run) > 0 {
		return runStep(e, ctx, kindGate, func(c context.Context) (map[string]any, error) {
			// A program gate that names a host runs there, so this machine does not refuse it.
			if in.Request.Host == "" {
				if err := e.refuseGate(in.Request.Project, true); err != nil {
					return nil, err
				}
			}

			return e.programGateOutputs(c, in.Request, in.Head, in.gateSlot())
		})
	}

	return e.projectGateCall(ctx, in, out)
}

// refuseGate asks Deps.RefuseGate whether a gate may run on this machine. It runs inside the recorded step that starts the gate, before the gate key,
// an earlier pass or any program, so a replay of a recorded step returns the record and is never refused. A nil RefuseGate refuses nothing.
func (e *Engine) refuseGate(project string, program bool) error {
	if e.deps.RefuseGate == nil {
		return nil
	}

	return e.deps.RefuseGate(project, program)
}

// projectGateCall is the two steps of a project gate, see gateCall. A gate step that recorded a lost job is followed by the wait for its proof (endLost),
// in this run, so the host slot and the exclusive slot of the project stay held while the job may still run.
func (e *Engine) projectGateCall(ctx dbos.Context, in ExclusiveInput, out *ExclusiveOutput) (map[string]any, error) {
	start, err := e.gateJobStep(ctx, in, out)
	if err != nil || start.Reused != nil {
		return start.Reused, err
	}

	finished, err := runStep(e, ctx, kindGate, func(c context.Context) (gateFinish, error) {
		finished, key, err := e.finishProjectGate(c, in, start)
		out.setKey(key, in.Request.Project)

		return finished, err
	})
	if err != nil || finished.Lost == nil {
		return finished.Outputs, err
	}

	return e.endLost(ctx, in, *finished.Lost, out)
}

// gateJobStep is the first step of a project gate (gate:job): it reuses an earlier pass and stores its key in out, or submits the host job.
func (e *Engine) gateJobStep(ctx dbos.Context, in ExclusiveInput, out *ExclusiveOutput) (gateStart, error) {
	return runStep(e, ctx, gateJobName, func(c context.Context) (gateStart, error) {
		started, err := e.startProjectGate(c, in.Request, in.gateSlot())
		if started.Reused != nil {
			out.setKey(started.Key.ID, in.Request.Project)
		}

		return started, err
	})
}

// hostJobName is the child workflow that runs the build job of a gate on a build host.
const hostJobName = "host_job"

// hostKeyPrefix starts the partition key of a host on a slot queue.
const hostKeyPrefix = "host:"

// gateHost is the build host that the gate of in runs on, or empty when the gate runs on this machine or the run is not a gate.
// A run that never called host_up names none, so the project file gives its default host (Deps.Facts).
// The engine reads it once, when the gate is queued, and the gate keeps it in HostKey.
func (e *Engine) gateHost(in ExclusiveInput) string {
	host, runsOnHost := in.recordedHost()
	if !runsOnHost {
		return ""
	}

	if host == "" {
		host = e.facts(in.Project).BuildHost
	}

	if host == config.LocalHost {
		return ""
	}

	return host
}

// gateSlot is the limit that the build job of a gate takes on its host: the class gate with the count that the gate recorded when it was queued.
// A gate that runs on this machine recorded none, and has no limit.
func (in ExclusiveInput) gateSlot() model.Slot {
	if in.SlotCount < 1 {
		return model.Slot{}
	}

	return model.Slot{Class: model.GateClass, Count: in.SlotCount}
}

// recordedHost is the host that the run recorded in host_up, and whether the call runs on a build host at all.
func (in ExclusiveInput) recordedHost() (string, bool) {
	switch {
	case in.Kind == kindGate && (len(in.Request.Run) == 0 || in.Request.Host != ""):
		return in.Request.BuildHost, true
	case in.Kind == kindBatchGate && len(in.GateRun) == 0:
		return in.BuildHost, true
	}

	return "", false
}

// hostJob runs the build job of a gate or a batch gate on the slot queue of the class gate, keyed by the host.
// The exclusive queue keeps one gate or merge for each project, so a merge never starts while a gate of its project runs.
// The slot queue keeps as many build jobs for each host as host.gate_slots says, so the gates of all projects on one host share those slots.
// The two queues cannot deadlock: a gate holds its slot in the exclusive queue while it waits for its job, and no job on the slot queue waits for a slot
// of the exclusive queue, because a job queues nothing.
func (e *Engine) hostJob(ctx dbos.Context, in ExclusiveInput) (ExclusiveOutput, error) {
	var out ExclusiveOutput

	run, stop := e.trackGate(ctx, in)
	defer stop()

	return e.endCall(&out, e.runCall(run, in, &out))
}

// onHost runs the call of a gate as a job on the slot queue of the class gate, under the HostKey of the gate, and waits for it. The job is a child run of the gate,
// so every cancel of the gate reaches it: WithCancelChildren of DBOS cancels each level before it lists the children of that level, so a job that starts
// during the cancel is listed and canceled, or refused by the owner check of its start. A queued job never starts, and a running one stops at the next tick. The wait itself never cancels the job: a cancel by id from a wait that stopped could land after a resume
// reopened the job, and fail the replay of the gate. A stop of the engine leaves the job for the next start.
func (e *Engine) onHost(ctx dbos.Context, in ExclusiveInput, out *ExclusiveOutput) error {
	handle, started, err := e.placedJob(ctx, in)
	out.HostStarted = started

	if err != nil {
		return err
	}

	err = e.awaitJob(ctx, handle, out)
	out.HostStarted = started

	return err
}

// placedJob places the gate of in when it has hosts to choose from, and queues its host job. The placement lock is held from the read of the free slots
// until the job is queued. One engine owns one state folder, so the lock covers every gate that can read the same host slots.
func (e *Engine) placedJob(ctx dbos.Context, in ExclusiveInput) (handle dbos.WorkflowHandle[ExclusiveOutput], started string, err error) {
	held := len(in.Hosts) > 0
	if held {
		e.placement.Lock()
		defer e.placement.Unlock()
	}

	in, err = e.place(ctx, in)
	if err != nil {
		return nil, "", err
	}

	if started, err = e.startSizedHost(ctx, in, held); err != nil {
		return nil, "", err
	}

	handle, err = e.slottedJob(ctx, in)

	return handle, started, err
}

// slottedJob queues the host job of a gate in the slot queue of its host.
func (e *Engine) slottedJob(ctx dbos.Context, in ExclusiveInput) (dbos.WorkflowHandle[ExclusiveOutput], error) {
	queue, err := e.slotQueue(model.GateClass, cmp.Or(in.SlotCount, 1))
	if err != nil {
		return nil, err
	}

	return e.queueJob(ctx, in, queue)
}

// runJob queues the host job of in on queue, under the HostKey of in, and waits for it. The error and the stop of the wait are those of onHost.
func (e *Engine) runJob(ctx dbos.Context, in ExclusiveInput, queue dbos.Queue, out *ExclusiveOutput) error {
	handle, err := e.queueJob(ctx, in, queue)
	if err != nil {
		return err
	}

	return e.awaitJob(ctx, handle, out)
}

// queueJob queues the host job of in on queue, under the HostKey of in.
func (e *Engine) queueJob(ctx dbos.Context, in ExclusiveInput, queue dbos.Queue) (dbos.WorkflowHandle[ExclusiveOutput], error) {
	handle, err := dbos.RunWorkflow(ctx, e.hostJob, in, dbos.WithQueue(queue), dbos.WithQueuePartitionKey(in.HostKey), dbos.WithPriority(e.currentPlace(ctx, in.Priority)))
	if err != nil {
		return nil, fmt.Errorf("queue the build job of item %s on host %s: %w", in.Item, strings.TrimPrefix(in.HostKey, hostKeyPrefix), err)
	}

	return handle, nil
}

// awaitJob waits for the host job of handle and stores its output in out. A job that a person holds keeps the gate waiting (see awaitHeld).
func (e *Engine) awaitJob(ctx dbos.Context, handle dbos.WorkflowHandle[ExclusiveOutput], out *ExclusiveOutput) error {
	done, err := e.awaitHeld(ctx, handle)
	if err != nil {
		return err
	}

	*out = done
	out.Failure = nil

	return done.Failure.err(e.knownErrors())
}

func (o *ExclusiveOutput) setKey(key, project string) {
	o.Key = key

	if key != "" {
		o.Project = project
	}
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

	in.Tip, in.Priority = plan.Tip, plan.Priority

	if in, err = e.placeOnHost(in); err != nil {
		return nil, in, err
	}

	handle, err := dbos.RunWorkflow(run, e.exclusive, in, dbos.WithQueue(e.exclusiveQueue), dbos.WithQueuePartitionKey(in.Project), dbos.WithPriority(plan.Priority))
	if err != nil {
		return nil, in, fmt.Errorf("queue the %s of item %s: %w", in.Kind, in.Item, err)
	}

	return handle, in, e.stopOlderGates(run, in)
}

// placeOnHost records the host and the gate slot count in a gate that runs on a build host. A gate on this machine, and a merge, stay as they are.
func (e *Engine) placeOnHost(in ExclusiveInput) (ExclusiveInput, error) {
	host := e.gateHost(in)
	if host == "" {
		return in, nil
	}

	count, err := e.gateSlots(in.Project, host)
	if err != nil {
		return in, fmt.Errorf("queue the %s of item %s: %w", in.Kind, in.Item, err)
	}

	recorded, _ := in.recordedHost()
	in.HostKey, in.SlotCount, in.Hosts = hostKeyPrefix+host, count, e.placeCandidates(in, recorded)

	return in, nil
}

// gateSlots asks the build how many gates host builds at once for project. A build with no Slots gives one, the default of `host.gate_slots`.
func (e *Engine) gateSlots(project, host string) (int, error) {
	if e.deps.Slots == nil {
		return 1, nil
	}

	grant, err := e.deps.Slots(project, host, model.GateClass)
	if err != nil {
		return 0, err
	}

	return grant.Count, nil
}

// awaitExclusive waits for the queued run. The error of a gate that was canceled names the head that replaced it.
func (e *Engine) awaitExclusive(run dbos.Context, handle dbos.WorkflowHandle[ExclusiveOutput], in ExclusiveInput) (ExclusiveOutput, error) {
	out, err := awaitResult(run, e.ctx, handle, e.deps.Clock.resultOptions())
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

// planOf reads the pull request of a gate in a recorded step. A merge or a batch gate reads nothing and has the last place (6).
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
// The cancel reaches the build job of each old gate, a job that starts during the cancel included, so the host frees its slot too.
// A cancel that fails is logged, and the gate of a later head cancels again (see docs/design/gate.md).
func (e *Engine) stopSuperseded(ctx context.Context, in ExclusiveInput) []string {
	// The list comes before the head read: a gate is queued after its head moves, so every listed gate was queued before the read and none belongs to a newer head.
	old, err := e.olderGates(in)

	standing, read := e.gateStanding(ctx, in)
	if !read || standing.Head != in.Tip {
		return nil
	}

	if err == nil && len(old) > 0 {
		err = dbos.CancelWorkflows(dbos.WithoutCancel(e.ctx), old, dbos.WithCancelChildren())
	}

	if err != nil {
		e.logger.WarnContext(ctx, "cannot stop the old gates of the pull request; the gate of the next head tries again", "item", in.Item, "error", err)

		return nil
	}

	return old
}

// olderGates lists the ids of the gates of the pull request of in that judge another head than the tip of in: the queued and running ones,
// and a canceled one whose build job is still queued or running.
func (e *Engine) olderGates(in ExclusiveInput) ([]string, error) {
	view := viewOf(dbos.WithoutCancel(e.ctx), e.dir)

	rows, err := view.Open("", dbos.WithFilterName(exclusiveName), dbos.WithFilterLoadInput(true))
	if err == nil {
		var stranded []dbos.WorkflowStatus

		stranded, err = canceledGatesWithAJob(view)
		rows = append(rows, stranded...)
	}

	if err != nil {
		return nil, err //nolint:wrapcheck // stopSuperseded logs the error with its context
	}

	return gatesOfOtherHeads(rows, in), nil
}

// canceledGatesWithAJob lists the canceled gates whose build job is still queued or running.
func canceledGatesWithAJob(view storeView) ([]dbos.WorkflowStatus, error) {
	jobs, err := view.Open("", dbos.WithFilterName(hostJobName))

	parents := parentsOf(jobs)
	if err != nil || len(parents) == 0 {
		return nil, err //nolint:wrapcheck // stopSuperseded logs the error with its context
	}

	return view.List(dbos.WithFilterWorkflowIDs(parents...), dbos.WithFilterName(exclusiveName), dbos.WithFilterLoadInput(true), //nolint:wrapcheck // stopSuperseded logs the error with its context
		dbos.WithFilterStatus(dbos.WorkflowStatusCancelled))
}

// parentsOf returns the parent ids of the rows.
func parentsOf(rows []dbos.WorkflowStatus) []string {
	parents := make([]string, len(rows))

	for i, row := range rows {
		parents[i] = row.ParentWorkflowID
	}

	return parents
}

// gatesOfOtherHeads returns the ids of the gate rows of the pull request of in that judge another head than the tip of in.
func gatesOfOtherHeads(rows []dbos.WorkflowStatus, in ExclusiveInput) []string {
	var old []string

	for _, row := range rows {
		if other, ok := gateInputOf(row.Input); ok && other.Project == in.Project && other.Item == in.Item && other.Tip != "" && other.Tip != in.Tip {
			old = append(old, row.ID)
		}
	}

	return old
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

// The queue starts the lowest number first. Every number is above zero so that the queue never mixes them with a run that sets no priority.
// One rule gives the place of a gate, an agent step and a run of the items queue (see placeOf): P0 first, then a person who waits, then P1, P2, P3 and no severity.
const (
	priorityP0          uint = 1
	priorityPersonWaits uint = 2
	priorityNormal      uint = 6 // no severity: the place after P3, which has 5
)

// placeOf is the one rule of the place in a queue. An item of severity P0 comes first. A person who waits on the pull request of the item comes next
// (an open pull request with an open change request or an unresolved review thread). The rest follow in the order of their severity, P1 to P3, and an item with none comes last.
func placeOf(severity model.Severity, personWaits bool) uint {
	switch {
	case severity == model.SeverityP0:
		return priorityP0
	case personWaits:
		return priorityPersonWaits
	}

	index := slices.Index(model.Severities(), severity)
	if index < 0 {
		return priorityNormal
	}

	return priorityPersonWaits + uint(index)
}

// severityOfItem reads the severity that the tracker gave the item in its fields. A field that is absent or not a severity is P?.
func severityOfItem(fields map[string]string) model.Severity {
	severity, err := model.ParseSeverity(fields[severityField])
	if err != nil {
		return model.SeverityUnknown
	}

	return severity
}

// priorityOf gives a gate its place from the severity of its item and the standing of its pull request. The merges and the batch gates keep the normal place.
// The standing is the read that the lifecycle makes already. A pull request that cannot be read counts as one that nobody waits on, and a line says so.
// A P0 with nobody waiting comes before a person who waits.
func priorityOf(in ExclusiveInput, standing review.Standing, read bool) uint {
	if in.Kind != kindGate {
		return priorityNormal
	}

	return placeOf(model.Severity(in.Severity), read && personWaits(standing))
}

// gateStanding reads the pull request of a gate once. The second value is false for a call that is not a gate, a project with no forge and a pull request that cannot be read:
// nobody then waits, and the gate takes the place of its severity alone. A project with no forge and an item with no pull request yet say nothing, and a failed read of a forge logs a line.
func (e *Engine) gateStanding(ctx context.Context, in ExclusiveInput) (review.Standing, bool) {
	if in.Kind != kindGate || e.deps.Forge == nil {
		return review.Standing{}, false
	}

	standing, err := e.deps.Forge.Pull(ctx, in.Project, in.Item)
	if errors.Is(err, forgepkg.ErrNoForge) || errors.Is(err, forgepkg.ErrNoPull) {
		return review.Standing{}, false
	}

	if err != nil {
		e.logger.WarnContext(ctx, "cannot read the pull request, so nobody counts as waiting and its gate takes the place of its severity", "item", in.Item, "error", err)

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
