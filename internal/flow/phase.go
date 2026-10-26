package flow

import (
	"context"
	"fmt"

	"github.com/dbos-inc/dbos-transact-golang/dbos"

	"github.com/tobocop2/obee/internal/agent"
	"github.com/tobocop2/obee/internal/model"
)

// PhaseInput describes one agent phase of an item. Findings and Answer feed the brief.
type PhaseInput struct {
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

// phase writes the brief, then runs the agent. Both are steps, so a restart skips the work that finished.
func (e *Engine) phase(ctx dbos.Context, in PhaseInput) (PhaseOutput, error) {
	brief, err := runStep(e, ctx, "brief", func(c context.Context) (string, error) {
		return e.deps.Briefs.Write(c, BriefRequest{Item: in.Item, Project: in.Project, Step: in.Step, Round: in.Round, Findings: in.Findings, Answer: in.Answer})
	})
	if err != nil {
		return PhaseOutput{}, fmt.Errorf("write the brief for the %s of item %s: %w", in.Step, in.Item, err)
	}

	return runStep(e, ctx, string(in.Step), func(c context.Context) (PhaseOutput, error) {
		return e.runAgent(c, in, brief)
	})
}

// runAgent starts the agent of one phase and waits for it. The group of the agent dies when the phase is canceled, times out or ends.
func (e *Engine) runAgent(ctx context.Context, in PhaseInput, brief string) (PhaseOutput, error) {
	owner := fmt.Sprintf("%s/%s/%d", in.Item, in.Step, in.Round)
	stop := context.AfterFunc(ctx, func() { e.endGroups(owner) })

	defer e.endGroups(owner)
	defer stop()

	result, err := e.deps.Agents.Run(ctx, AgentRequest{
		Item: in.Item, Project: in.Project, Step: in.Step, Round: in.Round, Login: in.Login, Fresh: in.Fresh, Brief: brief, Started: e.recordGroup(ctx, in.Item, owner),
	})

	out := PhaseOutput{Outcome: result.Outcome, ExitCode: result.ExitCode, Note: result.Note}
	if err != nil {
		return out, err
	}

	// The end of the phase is judged inside the step, so a phase that did not exit with code 0 is a failed step that obee resume reruns.
	return out, phaseEnd(in, out)
}

// recordGroup returns the function that writes the process group of an agent to the process file. A failed write is logged.
func (e *Engine) recordGroup(ctx context.Context, item, owner string) func(pid int) {
	return func(pid int) {
		if err := e.procs.add(ctx, owner, pid); err != nil {
			e.logger.WarnContext(ctx, "cannot record the agent process", "item", item, "error", err)
		}
	}
}

// endGroups kills the agent groups of one phase and forgets them.
func (e *Engine) endGroups(owner string) {
	if err := e.procs.release(owner); err != nil {
		e.logger.Warn("cannot update the agent process file", "error", err)
	}
}

// runPhase runs a phase as a child workflow on the agents queue, keyed by the login, and waits for it.
// It refuses to start when the last renewal of the claim failed.
func (e *Engine) runPhase(ctx dbos.Context, in PhaseInput) error {
	if in.Login == "" {
		return errEmptyLogin
	}

	if err := e.claimCheck(ctx, in.Item); err != nil {
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
