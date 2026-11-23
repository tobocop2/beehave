package flow

import (
	"cmp"
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"

	"github.com/tobocop2/beehave/internal/batch"
	"github.com/tobocop2/beehave/internal/config"
	gatepkg "github.com/tobocop2/beehave/internal/gate"
	"github.com/tobocop2/beehave/internal/model"
)

const (
	// lostPoll is the time between two looks for the proof that a lost gate job can no longer run.
	lostPoll = time.Minute
	// gateLostStartName is the step that records when the wait for the proof started.
	gateLostStartName = "gate:lost-start"
	// gateLostLookName starts the name of each look for the proof: gate:lost-look.<k>, from 0.
	gateLostLookName = "gate:lost-look"
	// hostLossKey is the field of a gate result that holds the record of a lost job.
	hostLossKey = "host_loss"
	// hostLossesKey is the field of a gate result, and of the status of a run, that counts the host losses of the item tree.
	hostLossesKey = "host_losses"
	// hostLossName is the sub-step of a gate call in which the item run records a lost job that has a proof: <call>/host-loss.
	hostLossName = "host-loss"
	// noHostReader is the note of a proof that a build with no host keeper cannot read.
	noHostReader = "this build cannot read the host"
	// lostJobKey is the event in which an open run shows the lost gate job that it asks the owner about.
	lostJobKey = "lost_job"
	// lostAskName starts the name of the read of the proof after each answer of the owner: <call>/lost-ask.<n>, from 0.
	lostAskName = "lost-ask"
)

// LostJobShown is what status shows while a run asks the owner about a lost gate job: the job, the host, the time the wait for a proof started,
// and the limit host.lost_job_wait that the wait reached with no proof.
type LostJobShown struct {
	Job   string    `json:"job"`
	Host  string    `json:"host"`
	Since time.Time `json:"since"`
	Limit string    `json:"limit"`
}

// LostJob is the host job of a gate whose wait ended as lost: the link to the host stayed down, so nobody can tell where the job stands.
// Kind is "gate" or "batch_gate". Head is the head that a gate judges, or the union commit of a batch gate.
// Host is the host of the HostKey of the call, which is the host the job ran on. Advice is the text of the lost error,
// which ends with the cancel command of the adapter. Required is the list of the gate. Since is the time the wait for a proof started.
// Proof is the proof that the last look read, and At is the time of that look. Limit is true when the wait reached host.lost_job_wait with no proof,
// and Wait is the value of host.lost_job_wait that the last look read.
type LostJob struct {
	Kind     string          `json:"kind"`
	Job      string          `json:"job"`
	Host     string          `json:"host"`
	Head     string          `json:"head,omitempty"`
	Since    time.Time       `json:"since"`
	At       time.Time       `json:"at"`
	Advice   string          `json:"advice"`
	Required []string        `json:"required,omitempty"`
	Proof    model.LostProof `json:"proof"`
	Limit    bool            `json:"limit,omitempty"`
	Wait     time.Duration   `json:"wait,omitempty"`
}

// gateFinish is the record of the step that waits for the host job of a project gate: the outputs of the gate, or the job that the wait lost.
// A loss is a value and not an error, because a step error is replayed as text and a later step could not read it.
type gateFinish struct {
	Outputs map[string]any `json:"outputs,omitempty"`
	Lost    *LostJob       `json:"lost,omitempty"`
}

// lostLook is the record of one look for the proof, and of each read of the proof after an answer of the owner. Wait is host.lost_job_wait as the look read it.
type lostLook struct {
	Proof model.LostProof `json:"proof"`
	At    time.Time       `json:"at"`
	Limit bool            `json:"limit,omitempty"`
	Wait  time.Duration   `json:"wait,omitempty"`
}

// lostJobOf is the lost job of the project gate of in, from the job that its first step recorded and the lost error of the wait.
func lostJobOf(in ExclusiveInput, start gateStart, lost error) *LostJob {
	return &LostJob{
		Kind: kindGate, Job: start.Job.ID, Host: strings.TrimPrefix(in.HostKey, hostKeyPrefix), Head: start.Job.Head, Advice: lost.Error(), Required: start.Required,
	}
}

// batchLostJobOf is the lost job of the batch gate of in, from the job that its first step recorded and the lost error of the wait.
func batchLostJobOf(in ExclusiveInput, job GateJob, lost error) *LostJob {
	return &LostJob{Kind: kindBatchGate, Job: job.ID, Host: strings.TrimPrefix(in.HostKey, hostKeyPrefix), Head: in.Commit, Advice: lost.Error()}
}

// endLost waits for the proof that the lost job of a gate can no longer run, and stores the job in out.
// A proof gives the void outputs of the gate. A wait that reaches host.lost_job_wait with no proof gives no outputs and a job with Limit set:
// the run ends, so both slots are free, and the item run asks the owner (askLost). A wait that fails stores the job as far as it got.
func (e *Engine) endLost(ctx dbos.Context, in ExclusiveInput, lost LostJob, out *ExclusiveOutput) (map[string]any, error) {
	held, err := e.holdLost(ctx, in, lost)
	out.Lost = &held

	if err != nil || held.Limit {
		return nil, err
	}

	return voidOutputs(held), nil
}

// hostLoss is the record of the step <call>/host-loss: the loss, and the count of the host losses of the item tree, this one included.
// This step is the record of the loss; the gate result carries a copy.
type hostLoss struct {
	Loss   any `json:"host_loss"`
	Losses int `json:"host_losses"`
}

// lostOutputs is the outputs of a gate call from what its exclusive run returned. A lost job with a proof gives the void outputs of the gate,
// with the record and the count of provenLoss.
func (e *Engine) lostOutputs(ctx dbos.Context, in ExclusiveInput, call string, ran ExclusiveOutput) (map[string]any, error) {
	if ran.Lost == nil {
		return ran.Outputs, nil
	}

	lost, record, err := e.provenLoss(ctx, in, call, *ran.Lost)
	if err != nil {
		return nil, err
	}

	outputs := voidOutputs(lost)
	outputs[hostLossKey], outputs[hostLossesKey] = record.Loss, record.Losses

	return outputs, nil
}

// provenLoss is the lost job of a call, which the exclusive run returned, once it has a proof, and the record of that loss in the item run.
// A wait for the proof that reached the limit asks the owner first (askLost). The lost job with its proof is then recorded (recordLoss).
// It runs in the item run, because only the item run receives an answer. The gate and the batch gate both read a loss through it.
func (e *Engine) provenLoss(ctx dbos.Context, in ExclusiveInput, call string, lost LostJob) (LostJob, hostLoss, error) {
	if lost.Limit {
		var err error
		if lost, err = e.askLost(ctx, in, call, lost); err != nil {
			return lost, hostLoss{}, err
		}
	}

	record, err := e.recordLoss(ctx, in.Item, call, lost)

	return lost, record, err
}

// recordLoss writes a lost job that has a proof as the step <call>/host-loss of the item run, with the count of the host losses of the item tree.
// The count walks the item tree inside the step, so a replay reads the recorded count and walks nothing.
func (e *Engine) recordLoss(ctx dbos.Context, item, call string, lost LostJob) (hostLoss, error) {
	record, err := runStep(e, ctx, call+"/"+hostLossName, func(context.Context) (hostLoss, error) {
		earlier, err := e.treeHostLosses(ctx, item)

		return hostLoss{Loss: lossRecord(lost), Losses: earlier + 1}, err
	})
	if err != nil {
		return hostLoss{}, fmt.Errorf("record the lost job of the gate: %w", err)
	}

	return record, nil
}

// treeHostLosses counts the host losses that the item runs of the tree of the run of ctx recorded: their steps <call>/host-loss.
func (e *Engine) treeHostLosses(ctx dbos.Context, item string) (int, error) {
	root, err := e.treeRoot(ctx, item)
	if err != nil {
		return 0, err
	}

	return hostLossesBelow(e.store(), root)
}

// hostLossesBelow counts the steps <call>/host-loss of a run and of every item run below it. The count needs the names of the steps only,
// so the walk does not load their outputs.
func hostLossesBelow(ctx storeView, id string) (int, error) {
	count := 0

	err := walkTree(ctx, id, dbos.WithStepsLoadOutput(false), func(_ string, records []dbos.StepInfo) error {
		for _, record := range records {
			if _, sub, split := strings.Cut(record.StepName, "/"); split && sub == hostLossName {
				count++
			}
		}

		return nil
	})

	return count, err
}

// askLost asks the owner about a lost gate job and reads the proof once after each answer, in the step <call>/lost-ask.<n>. The read is a new call
// for each answer, never a replay of an earlier look. A proof ends the wait with the proof in the lost job and clears the lost job from the status.
// No proof asks again. It starts no gate job: no rerun of the head or the commit comes before a proof.
func (e *Engine) askLost(ctx dbos.Context, in ExclusiveInput, call string, lost LostJob) (LostJob, error) {
	if err := show(ctx, in.Item, lostJobKey, LostJobShown{Job: lost.Job, Host: lost.Host, Since: lost.Since, Limit: lost.Wait.String()}, "the lost gate job"); err != nil {
		return lost, err
	}

	for answer := 0; ; answer++ {
		seen, err := e.answerThenRead(ctx, in, call, lost, answer)
		if err != nil {
			return lost, err
		}

		if seen.Proof.Kind != "" {
			lost.Proof, lost.At = seen.Proof, seen.At

			return lost, show(ctx, in.Item, lostJobKey, LostJobShown{}, "that the lost gate job has a proof")
		}
	}
}

// answerThenRead waits for the next answer of the owner, then reads the proof once in the recorded step <call>/lost-ask.<answer>.
func (e *Engine) answerThenRead(ctx dbos.Context, in ExclusiveInput, call string, lost LostJob, answer int) (lostLook, error) {
	if _, err := (&script{e: e, ctx: ctx, in: ItemInput{Project: in.Project, Item: in.Item}}).waitOwner(lostQuestion(lost)); err != nil {
		return lostLook{}, err
	}

	return runStep(e, ctx, call+"/"+lostAskName+"."+strconv.Itoa(answer), func(c context.Context) (lostLook, error) {
		proof, err := e.lostProof(c, in.Project, lost)

		return lostLook{Proof: proof, At: e.deps.Clock.reading()}, err
	})
}

// lostQuestion is the question to the owner about a lost gate job whose wait for a proof reached host.lost_job_wait.
func lostQuestion(lost LostJob) string {
	return fmt.Sprintf("the gate job %s on host %s was lost, and no proof that it can no longer run came within host.lost_job_wait (%s): %s. "+
		"Stop the job or bring the host back, then answer CONTINUE: the run reads the proof again, and asks again when there is none",
		lost.Job, lost.Host, lost.Wait.String(), lost.Advice)
}

// holdLost is the timed wait for the proof, in the run that holds the host slot and the exclusive slot of the project, so no other gate starts
// on a host whose job may still run. Each look is a recorded step that calls LostProof once, and a durable sleep comes between two looks,
// so a restart replays the earlier looks. A look reads the limit from the config of the engine that runs it. The wait submits no job.
// A run that is canceled during the wait reads the proof once more, see lostStopped.
func (e *Engine) holdLost(ctx dbos.Context, in ExclusiveInput, lost LostJob) (LostJob, error) {
	since, err := runStep(e, ctx, gateLostStartName, func(context.Context) (time.Time, error) { return e.deps.Clock.reading(), nil })
	if err != nil {
		return lost, fmt.Errorf("record the start of the wait for the lost job %s: %w", lost.Job, err)
	}

	lost.Since = since

	for look := 0; ; look++ {
		seen, err := e.lookThenSleep(ctx, in.Project, lost, look)
		if err != nil {
			return lost, e.lostStopped(ctx, in, lost, err)
		}

		if seen.ends() {
			lost.Proof, lost.At, lost.Limit, lost.Wait = seen.Proof, seen.At, seen.Limit, seen.Wait

			return lost, nil
		}
	}
}

// ends is true for a look that ends the wait: it read a proof, or it came at the limit.
func (l lostLook) ends() bool {
	return l.Proof.Kind != "" || l.Limit
}

// lookThenSleep is look number k of the wait, a recorded step. A look that does not end the wait is followed by the durable sleep before the next look.
func (e *Engine) lookThenSleep(ctx dbos.Context, project string, lost LostJob, look int) (lostLook, error) {
	seen, err := runStep(e, ctx, gateLostLookName+"."+strconv.Itoa(look), func(c context.Context) (lostLook, error) { return e.lookLost(c, project, lost) })
	if err != nil || seen.ends() {
		return seen, err
	}

	_, err = dbos.Sleep(ctx, cmp.Or(e.deps.Clock.LostPoll, lostPoll))

	return seen, err
}

// lookLost reads the proof once, then the clock and the limit. A look with no proof at or after Since plus the limit ends the wait.
func (e *Engine) lookLost(ctx context.Context, project string, lost LostJob) (lostLook, error) {
	proof, err := e.lostProof(ctx, project, lost)
	if err != nil {
		return lostLook{}, err
	}

	now, wait := e.deps.Clock.reading(), e.lostJobWait(project)

	return lostLook{Proof: proof, At: now, Limit: proof.Kind == "" && !now.Before(lost.Since.Add(wait)), Wait: wait}, nil
}

// lostProof asks the host keeper for the proof. A build with no host keeper has no proof, and the note says so.
func (e *Engine) lostProof(ctx context.Context, project string, lost LostJob) (model.LostProof, error) {
	if e.deps.Hosts == nil {
		return model.LostProof{Note: noHostReader}, nil
	}

	proof, err := e.deps.Hosts.LostProof(ctx, project, lost.Host, lost.Job)
	if err != nil {
		return proof, fmt.Errorf("read the proof that the lost job %s on host %s can no longer run: %w", lost.Job, lost.Host, err)
	}

	return proof, nil
}

// lostJobWait is host.lost_job_wait of the project as the config of this engine holds it. A project with no host block waits for the default.
func (e *Engine) lostJobWait(project string) time.Duration {
	if block := e.facts(project).Host; block != nil {
		return block.LostJobWait
	}

	return config.DefaultLostJobWait
}

// lostStopped ends a wait that failed. When the run was canceled (a newer head, or a person) and the engine still runs, it reads the proof once more
// under a context that the cancel does not reach, so the stop of the job is tried once more. A read with no proof is logged: the job can still run.
// It never cancels the job: a cancel waits for the job and banks its work.
func (e *Engine) lostStopped(ctx dbos.Context, in ExclusiveInput, lost LostJob, err error) error {
	if ctx.Err() == nil || e.ctx.Err() != nil {
		return err
	}

	proof, readErr := e.lostProof(context.WithoutCancel(ctx), in.Project, lost)
	if readErr != nil || proof.Kind == "" {
		e.logger.WarnContext(ctx, "the gate stopped while its lost job may still run on the host: the last stop of the job gave no proof",
			"item", in.Item, "job", lost.Job, "host", lost.Host, "note", proof.Note, "error", readErr)
	}

	return err
}

// voidOutputs is the result of a gate whose lost job has a proof: it did not pass, and every step of its list is unknown, so the workflow reads a void gate.
// host_loss holds the record of the loss. A gate with no list gives the one step of its command.
func voidOutputs(lost LostJob) map[string]any {
	names := lost.Required
	if len(names) == 0 {
		names = []string{gatepkg.StepCommand}
	}

	steps := make([]GateStep, len(names))
	for i, name := range names {
		steps[i] = GateStep{Name: name, Status: unknownStatus}
	}

	outputs := gateOutputs(false, lost.Head, "", lossReport(lost), steps, lost.Required, nil)
	outputs[hostLossKey] = lossRecord(lost)

	return outputs
}

// lossReport is the report of a gate whose lost job has a proof: the host and the kind of the proof.
func lossReport(lost LostJob) string {
	return fmt.Sprintf("the gate job was lost on %s and the proof is %s", lost.Host, lost.Proof.Kind)
}

// lossRecord is the record host_loss of a lost job that has a proof. It has one shape for the gate and the batch gate: head_or_commit is the head
// of a gate and the union commit of a batch gate.
func lossRecord(lost LostJob) map[string]any {
	return map[string]any{
		"kind": lost.Kind, "job": lost.Job, "host": lost.Host, "head_or_commit": lost.Head, "proof": lost.Proof.Kind, "note": lost.Proof.Note, "at": lost.At.UTC().Format(time.RFC3339),
	}
}

// lostOutcome is the outcome of a batch gate whose lost job has a proof: a void gate on the tree of the checkout, whose reason names the host and the proof.
// It is the record of the host job run. The item run reads the lost job next to it and gates the commit again (batchGate.Run).
func lostOutcome(tree string, lost LostJob) batch.GateOutcome {
	return batch.GateOutcome{Tree: tree, Report: lossReport(lost), Void: lossReport(lost)}
}
