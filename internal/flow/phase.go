package flow

import (
	"context"
	"fmt"

	"github.com/dbos-inc/dbos-transact-golang/dbos"

	"github.com/tobocop2/obee/internal/agent"
	"github.com/tobocop2/obee/internal/model"
)

// PhaseInput describes one agent phase of an item. Findings and Answer feed the brief. Run is the id of the run that queued the phase.
type PhaseInput struct {
	Run      string     `json:"run"`
	Item     string     `json:"item"`
	Project  string     `json:"project"`
	Step     model.Step `json:"step"`
	Round    int        `json:"round"`
	Login    string     `json:"login"`
	Fresh    bool       `json:"fresh"`
	Findings string     `json:"findings"`
	Answer   string     `json:"answer"`
}

// PhaseOutput records how the agent of a phase ended.
type PhaseOutput struct {
	Outcome  agent.Outcome `json:"outcome"`
	ExitCode int           `json:"exit_code"`
	Note     string        `json:"note"`
}

// phase waits for the claim of its run, writes the brief, then runs the agent. The brief and the agent are steps, so a restart skips the work that finished.
// A phase starts no agent unless the run that queued it holds a trusted claim now, whatever context the phase runs under:
// the child workflow of a phase outlives the run that queued it, and a later run of the same item holds another claim.
func (e *Engine) phase(ctx dbos.Context, in PhaseInput) (PhaseOutput, error) {
	claim, err := e.awaitClaim(ctx, in)
	if err != nil {
		return PhaseOutput{}, err
	}

	brief, err := runStep(e, ctx, "brief", func(c context.Context) (string, error) {
		return e.deps.Briefs.Write(c, BriefRequest{Run: in.Run, Item: in.Item, Project: in.Project, Step: in.Step, Round: in.Round, Findings: in.Findings, Answer: in.Answer})
	})
	if err != nil {
		return PhaseOutput{}, fmt.Errorf("write the brief for the %s of item %s: %w", in.Step, in.Item, err)
	}

	return runStep(e, ctx, string(in.Step), func(c context.Context) (PhaseOutput, error) {
		return e.runAgent(c, claim, in, brief)
	})
}

// runAgent starts the agent of one phase under the context of the claim of its run, and waits for it. The agent dies with its process group when
// the claim ends, or when the phase is canceled, times out or ends. A loss of the claim either comes first, and the start finds its context done,
// or comes after, and the end of the context kills the group: no lock and no list of groups to kill on a loss.
func (e *Engine) runAgent(ctx, claim context.Context, in PhaseInput, brief string) (PhaseOutput, error) {
	agentCtx, cancel := context.WithCancelCause(claim)
	defer cancel(nil)

	defer context.AfterFunc(ctx, func() { cancel(context.Cause(ctx)) })()
	defer e.endGroups(in.Run)

	result, err := e.deps.Agents.Run(agentCtx, AgentRequest{
		Item: in.Item, Project: in.Project, Step: in.Step, Round: in.Round, Login: in.Login, Fresh: in.Fresh, Brief: brief, Started: e.recordGroup(ctx, in),
	})

	out := PhaseOutput{Outcome: result.Outcome, ExitCode: result.ExitCode, Note: result.Note}
	if err != nil {
		return out, err
	}

	// The end of the phase is judged inside the step, so a phase that did not exit with code 0 is a failed step that obee resume reruns.
	return out, phaseEnd(in, out)
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

// runPhase runs a phase as a child workflow on the agents queue, keyed by the login, and waits for it.
func (e *Engine) runPhase(ctx dbos.Context, in PhaseInput) error {
	if in.Login == "" {
		return errEmptyLogin
	}

	in.Run = workflowID(ctx)

	if err := e.claimCheck(in.Run, in.Item); err != nil {
		return err
	}

	handle, err := dbos.RunWorkflow(ctx, e.phase, in, dbos.WithQueue(e.queue), dbos.WithQueuePartitionKey(in.Login))
	if err != nil {
		return fmt.Errorf("queue the %s of item %s: %w", in.Step, in.Item, err)
	}

	_, err = awaitPhase(ctx, handle, e.deps.Clock.resultOptions())

	return err
}

// awaitPhase waits for the result of a phase. A cancel of ctx ends the wait at once:
// the polling handle of the workflow library does not watch the context, so a stopped engine would wait for the phase to end.
func awaitPhase(ctx dbos.Context, handle dbos.WorkflowHandle[PhaseOutput], options []dbos.GetResultOption) (PhaseOutput, error) {
	type outcome struct {
		out PhaseOutput
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
		return PhaseOutput{}, context.Cause(ctx)
	}
}

// phaseEnd turns a phase that did not exit with code 0 into an error that names the step.
func phaseEnd(in PhaseInput, out PhaseOutput) error {
	if out.Outcome != agent.OutcomeExited || out.ExitCode != 0 {
		return fmt.Errorf("the %s of item %s ended as %s (exit %d): %s. Fix the cause, then run obee resume %s", in.Step, in.Item, out.Outcome, out.ExitCode, out.Note, in.Item)
	}

	return nil
}
