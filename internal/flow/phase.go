package flow

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"

	"github.com/tobocop2/obee/internal/agent"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/workflow"
)

// PhaseInput describes one agent phase of an item. Run is the id of the run that queued the phase.
// Prompt is the rendered prompt, which is the whole brief. Resume is the session to continue.
// Model, Timeout and IdleTimeout go to the agent as they are. Forbid lists the kinds of command that the step forbids.
type PhaseInput struct {
	Run     string `json:"run"`
	Item    string `json:"item"`
	Project string `json:"project"`
	Login   string `json:"login"`
	// Kind is the agent kind of the step: review or implement. It is empty, and so left out of a recorded phase, for the kind implement.
	Kind string `json:"kind,omitempty"`

	Prompt      string        `json:"prompt,omitempty"`
	Resume      string        `json:"resume,omitempty"`
	Model       string        `json:"model,omitempty"`
	Timeout     time.Duration `json:"timeout,omitempty"`
	IdleTimeout time.Duration `json:"idle_timeout,omitempty"`

	Forbid []model.FindingKind `json:"forbid,omitempty"`
	// Protect lists the directories of the kind protected-path. It is empty, and so left out of a recorded phase, for a step that does not use the kind.
	Protect []string `json:"protect,omitempty"`

	// NoEdits is true for a step with edits = False. It is left out of a recorded phase for a step that may edit, so a stored phase keeps its form.
	NoEdits bool `json:"no_edits,omitempty"`

	// OutputFile is the file where the agent writes its declared outputs. Outputs names them. Outputs is left out for a call that declares none.
	OutputFile string   `json:"output_file,omitempty"`
	Outputs    []string `json:"outputs,omitempty"`
}

// PhaseOutput records how the agent of a phase ended. Session, Transcript, Text, Skills and Violations describe the session that ended.
// The phase is a recorded step, so a replay reads Violations from the record and scans no transcript again.
type PhaseOutput struct {
	Outcome    agent.Outcome `json:"outcome"`
	ExitCode   int           `json:"exit_code"`
	Note       string        `json:"note"`
	Session    string        `json:"session,omitempty"`
	Transcript string        `json:"transcript,omitempty"`
	Text       string        `json:"text,omitempty"`
	Skills     []string      `json:"skills,omitempty"`
	Violations []string      `json:"violations,omitempty"`
}

// phase waits for the claim of its run, then runs the agent as a step, so a restart skips the work that finished.
// A phase starts no agent unless the run that queued it holds a trusted claim now, whatever context the phase runs under:
// the child workflow of a phase outlives the run that queued it, and a later run of the same item holds another claim.
func (e *Engine) phase(ctx dbos.Context, in PhaseInput) (PhaseOutput, error) {
	claim, err := e.awaitClaim(ctx, in)
	if err != nil {
		return PhaseOutput{}, err
	}

	return runStep(e, ctx, "agent", func(c context.Context) (PhaseOutput, error) {
		return e.runAgent(c, claim, in)
	})
}

// runAgent starts the agent of one phase under the context of the claim of its run, and waits for it. The agent dies with its process group when
// the claim ends, or when the phase is canceled, times out or ends. A loss of the claim either comes first, and the start finds its context done,
// or comes after, and the end of the context kills the group: no lock and no list of groups to kill on a loss.
func (e *Engine) runAgent(ctx, claim context.Context, in PhaseInput) (PhaseOutput, error) {
	agentCtx, cancel := context.WithCancelCause(claim)
	defer cancel(nil)

	defer context.AfterFunc(ctx, func() { cancel(context.Cause(ctx)) })()
	defer e.endGroups(in.Run)

	result, err := e.deps.Agents.Run(agentCtx, AgentRequest{
		Item: in.Item, Project: in.Project, Login: in.Login, Started: e.recordGroup(ctx, in),
		Prompt: in.Prompt, Resume: in.Resume, Model: in.Model, Timeout: in.Timeout, IdleTimeout: in.IdleTimeout,
		Forbid: in.Forbid, Protect: in.Protect, NoEdits: in.NoEdits, OutputFile: in.OutputFile, Outputs: in.Outputs,
	})

	out := phaseOutput(result)
	if err != nil {
		return out, err
	}

	// The end of the phase is judged inside the step, so a phase that did not exit with code 0 is a failed step that obee resume reruns.
	return out, phaseEnd(in, out)
}

// phaseOutput records how the agent of a phase ended.
func phaseOutput(result agent.Result) PhaseOutput {
	return PhaseOutput{
		Outcome: result.Outcome, ExitCode: result.ExitCode, Note: result.Note, Session: result.Session, Transcript: result.Transcript,
		Text: result.Text, Skills: result.Skills, Violations: result.Violations,
	}
}

// recordGroup returns the function that writes the process group of an agent to the process file, under the id of the run. A failed write is logged.
// The file serves the next start of obee run: the next worker kills the groups that a crash left.
func (e *Engine) recordGroup(ctx context.Context, in PhaseInput) func(pid int) {
	return func(pid int) {
		if err := e.procs.add(ctx, in.Run, pid); err != nil {
			e.logger.WarnContext(ctx, "cannot record the agent process", "item", in.Item, "error", err)
		}
	}
}

// endGroups forgets the agent groups of one run in the process file.
func (e *Engine) endGroups(run string) {
	if err := e.procs.release(run); err != nil {
		e.logger.Warn("cannot update the agent process file", "error", err)
	}
}

// agentQueueName is the queue of an agent kind: a review runs on the reviews queue, every other kind on the agents queue.
func agentQueueName(kind string) string {
	if kind == workflow.AgentKindReview {
		return reviewsQueueName
	}

	return queueName
}

// agentQueue is the registered queue that agentQueueName names for a kind.
func (e *Engine) agentQueue(kind string) dbos.Queue { //nolint:ireturn // the queue type of the workflow library
	if agentQueueName(kind) == reviewsQueueName {
		return e.reviewQueue
	}

	return e.queue
}

// queuePhase runs a phase as a child workflow on the queue of its kind, keyed by the login, and waits for it. It returns how the agent ended.
func (e *Engine) queuePhase(ctx dbos.Context, in PhaseInput) (PhaseOutput, error) {
	in.Run = workflowID(ctx)

	if err := e.claimCheck(in.Run, in.Item); err != nil {
		return PhaseOutput{}, err
	}

	handle, err := dbos.RunWorkflow(ctx, e.phase, in, dbos.WithQueue(e.agentQueue(in.Kind)), dbos.WithQueuePartitionKey(in.Login))
	if err != nil {
		return PhaseOutput{}, fmt.Errorf("queue the agent step of item %s: %w", in.Item, err)
	}

	return awaitResult(ctx, handle, e.deps.Clock.resultOptions())
}

// awaitResult waits for the result of a child workflow. A cancel of ctx ends the wait at once:
// the polling handle of the workflow library does not watch the context, so a stopped engine would wait for the phase to end.
func awaitResult[T any](ctx dbos.Context, handle dbos.WorkflowHandle[T], options []dbos.GetResultOption) (T, error) { //nolint:ireturn // the result is the type of the child workflow
	type outcome struct {
		out T
		err error
	}

	done := make(chan outcome, 1)

	go func() {
		out, err := handle.GetResult(options...)
		done <- outcome{out: out, err: err}
	}()

	select {
	case finished := <-done:
		return finished.out, finished.err
	case <-ctx.Done():
		var none T

		return none, stopError(ctx)
	}
}

// stopError is the error of a step that stops because ctx is done. It wraps context.Canceled as well as the cause. The library records a run as failed
// when a step returns a plain cause in the instant between the close of the context and its own cancel hook, and as stopped only when the error is a cancel.
// Every wait inside a step returns this error when its context ends.
func stopError(ctx context.Context) error {
	return errors.Join(ctx.Err(), context.Cause(ctx))
}

// phaseEnd turns a phase that did not exit with code 0 into an error that names the step.
// A run that a cap stopped (turns or dollars) is not a failure: the output `stop` names the cap and the workflow decides what follows.
func phaseEnd(in PhaseInput, out PhaseOutput) error {
	if out.Outcome == agent.OutcomeTurnCap || out.Outcome == agent.OutcomeBudgetCap {
		return nil
	}

	if out.Outcome != agent.OutcomeExited || out.ExitCode != 0 {
		return fmt.Errorf("the agent step of item %s ended as %s (exit %d): %s. Fix the cause, then run obee resume %s", in.Item, out.Outcome, out.ExitCode, out.Note, in.Item)
	}

	return nil
}
