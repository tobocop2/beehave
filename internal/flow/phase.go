package flow

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/model"
)

// PhaseInput describes one agent phase of an item. Run is the id of the run that queued the phase.
// Prompt is the rendered prompt, which is the whole brief. Resume is the session to continue.
// Model, Timeout and IdleTimeout go to the agent as they are. Forbid lists the kinds of command that the step forbids.
type PhaseInput struct {
	Run string `json:"run"`
	// Call is the name of the agent call, such as agent:fix#1. The stop at a cap and the error of a look name it.
	Call    string `json:"call,omitempty"`
	Item    string `json:"item"`
	Project string `json:"project"`
	Login   string `json:"login"`
	// Severity is the severity of the item, which sets the place of the phase in its queue. It is left out for an item with none.
	Severity string `json:"severity,omitempty"`
	// Rerun is true for a phase that runs a call again: a new try after a bad output file, or the rerun after a provider limit. It is left out of the first run of a call.
	Rerun bool `json:"rerun,omitempty"`

	Prompt      string        `json:"prompt,omitempty"`
	Resume      string        `json:"resume,omitempty"`
	Model       string        `json:"model,omitempty"`
	Timeout     time.Duration `json:"timeout,omitempty"`
	IdleTimeout time.Duration `json:"idle_timeout,omitempty"`

	Forbid []model.FindingKind `json:"forbid,omitempty"`
	// Protect lists the directories of the kind protected-path. It is empty, and so left out of a recorded phase, for a step that does not use the kind.
	Protect []string `json:"protect,omitempty"`

	// MaxBudget is the max_budget_usd of the step. It is left out of a recorded phase for a step that sets none.
	MaxBudget float64 `json:"max_budget_usd,omitempty"`

	// MaxContextPercent is the max_context_percent of the step. It is left out of a recorded phase for a step that sets none.
	MaxContextPercent int `json:"max_context_percent,omitempty"`

	// NoEdits is true for a step with edits = False. Such a step runs on the review queue. It is left out of a recorded phase for a step that may edit, so a stored phase keeps its form.
	NoEdits bool `json:"no_edits,omitempty"`

	// Sandbox is the sandbox of the step. It is left out of a recorded phase for a step with no sandbox, and a phase without it runs with no sandbox,
	// so a stored phase keeps its form and a replay never reads the current default of the project.
	Sandbox *model.Sandbox `json:"sandbox,omitempty"`

	// Skills are the skills that the prompt of the step declares. It is left out of a recorded phase for a step that declares none.
	Skills []string `json:"skills,omitempty"`

	// OutputFile is the file where the agent writes its declared outputs. Outputs names them. Outputs is left out for a call that declares none.
	OutputFile string   `json:"output_file,omitempty"`
	Outputs    []string `json:"outputs,omitempty"`

	// Data is the JSON text of the data keyword that the render step recorded, which the phase writes to the data file of the item and the call before each start
	// of the agent. It is left out of a recorded phase for a call with no data.
	Data string `json:"data,omitempty"`

	// ResetPassed is true for the rerun after a provider limit whose reset time had passed. A rerun that a limit with a past reset time stops again fails.
	// It is left out of a recorded phase for any other run.
	ResetPassed bool `json:"reset_passed,omitempty"`

	// Limited is the run that a provider limit stopped, for the rerun after the reset, and is left out of a recorded phase for any other run.
	Limited *LostRun `json:"limited,omitempty"`

	// StartHead is the commit of the item folder when the render step of the call ran. The record of a lost start holds the diff summary since that commit.
	// It is empty for a plain folder.
	StartHead string `json:"start_head,omitempty"`

	// TreeStart is the commit that the run made the worktree of a round item at. The diff of a lost start makes a removed worktree again there.
	// It is left out of a recorded phase for any other item.
	TreeStart string `json:"tree_start,omitempty"`

	// Base is the base branch of the round that spawned the run. A worktree that someone removed is cut there again. It is left out for any other run.
	Base string `json:"base,omitempty"`
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
	// Denials lists the tool calls that the permission check of the agent CLI refused. It is left out of a recorded phase that has none.
	Denials []agent.Denial `json:"denials,omitempty"`
	// Stderr is the redacted tail of the error stream of the agent CLI. It is left out of a recorded phase whose CLI wrote none.
	Stderr string `json:"stderr,omitempty"`
	// Spend is the cost of the run: the dollars that the agent CLI reported, or an estimate that beehave made from the tokens, or the mark that the cost is unknown.
	agent.Spend
	// Limit is the provider limit that stopped a run that ended as limited. It is left out of a recorded phase for any other run, so a stored phase keeps its form.
	Limit *agent.Limit `json:"limit,omitempty"`
	// Resumed is true for a run that resumed the session of a lost start or of a run that a provider limit stopped. It is left out of a recorded phase for any other run.
	Resumed bool `json:"resumed,omitempty"`
	// CapStop is the run cap or the tree cap that stopped the look of the phase, so no agent ran. It is left out of a recorded phase for any other run.
	CapStop *CapStop `json:"cap_stop,omitempty"`
	// ResumeOf is the session of the lost start or the limited run that this run was given to resume, and is left out of a recorded phase for any other run.
	ResumeOf string `json:"resume_of,omitempty"`
	// Execution is the id of the agent execution that the phase ran, which is the key of its spend row. It is left out of a recorded phase that ran no agent.
	Execution string `json:"execution,omitempty"`
}

// phase waits for a trusted claim of its run, looks at the caps, then runs the agent as a step; a cap stop ends it with the stop and no agent run.
func (e *Engine) phase(ctx dbos.Context, in PhaseInput) (PhaseOutput, error) {
	claim, holder, err := e.awaitClaim(ctx, in)
	if err != nil {
		return PhaseOutput{}, err
	}

	if check, err := e.lookAtCaps(ctx, in); err != nil || check.Stop != nil {
		return PhaseOutput{CapStop: check.Stop}, err
	}

	in, lost, err := e.markStarts(ctx, in)
	if err != nil {
		return PhaseOutput{}, err
	}

	return e.agentStep(ctx, claim, holder, in, cmp.Or(lost, in.Limited))
}

// agentStep runs the agent of the phase as a step, under the claim of the attempt holder. The record of a failed step holds its output and its error, so the cost of the failed run is in the record.
// lost is the lost or limited run that the start follows, or nil.
func (e *Engine) agentStep(ctx dbos.Context, claim context.Context, holder *attempt, in PhaseInput, lost *LostRun) (PhaseOutput, error) { //nolint:revive // ctx of the workflow first, then the context of the claim
	return runStep(e, ctx, agentStepName, func(c context.Context) (PhaseOutput, error) {
		return e.runAgent(c, claim, holder, in, lost)
	})
}

// runAgent starts the agent of one phase under the context of the claim of its run, and waits for it. The agent dies with its process group when
// the claim ends, or when the phase is canceled, times out or ends. A loss of the claim either comes first, and the start finds its context done,
// or comes after, and the end of the context kills the group: no lock and no list of groups to kill on a loss.
// ctx is the context of the step, which saves each session id that the agent names. holder is the attempt whose claim the agent runs under: its process rows are its own.
func (e *Engine) runAgent(ctx, claim context.Context, holder *attempt, in PhaseInput, lost *LostRun) (PhaseOutput, error) {
	agentCtx, cancel := context.WithCancelCause(claim)
	defer cancel(nil)

	defer context.AfterFunc(ctx, func() { cancel(context.Cause(ctx)) })()
	defer e.endGroups(holder)

	data, err := e.callFiles(in)
	if err != nil {
		return PhaseOutput{}, err
	}

	execution, err := e.startSpend(ctx, in)
	if err != nil {
		return PhaseOutput{}, err
	}

	named, said, finish := e.watch(ctx, in)
	defer finish()

	result, err := e.deps.Agents.Run(agentCtx, e.agentRequest(ctx, holder, in, data, lost, named, said))

	out, err := e.judgePhase(ctx, in, result, err)
	out.ResumeOf, out.Execution = lost.session(), execution
	e.endSpend(ctx, execution, in, out)

	return out, err
}

// judgePhase returns the output of a phase from the result of its agent, and the error that ends the phase.
// A limit with no reset time gets the one that the usage read gives. The end of the phase is judged inside the step, so a phase that did not exit with code 0,
// and that no cap and no provider limit with an end to its wait stopped, is a failed step that beehave resume reruns.
func (e *Engine) judgePhase(ctx context.Context, in PhaseInput, result agent.Result, runErr error) (PhaseOutput, error) {
	out := phaseOutput(result)

	if runErr != nil {
		return out, runErr
	}

	if err := e.dateLimit(ctx, in, &out); err != nil {
		return out, err
	}

	e.signalLimit(out)

	return out, phaseEnd(in, out, e.deps.Clock.wall())
}

// agentRequest is the request for the agent of a phase. Its process groups are rows of the attempt holder. data is the data file of the phase,
// and named receives each new session id that the agent names. said receives each message text and tool call.
func (e *Engine) agentRequest(ctx context.Context, holder *attempt, in PhaseInput, data string, lost *LostRun, named func(session string), said func(agent.Said)) AgentRequest {
	return AgentRequest{
		Run: in.Run, Call: in.Call, Item: in.Item, Project: in.Project, Login: in.Login, Started: e.recordGroup(ctx, holder, in), Named: named, Said: said, Redact: e.redact, Lost: lost,
		Prompt: in.Prompt, Resume: in.Resume, Model: in.Model, Timeout: in.Timeout, IdleTimeout: in.IdleTimeout,
		Forbid: in.Forbid, Protect: in.Protect, MaxBudget: in.MaxBudget, MaxContextPercent: in.MaxContextPercent, NoEdits: in.NoEdits, Sandbox: in.Sandbox, Skills: in.Skills, OutputFile: in.OutputFile, Outputs: in.Outputs,
		DataFile: data,
	}
}

// callFiles checks the output file of the phase and writes its data file, which it returns.
func (e *Engine) callFiles(in PhaseInput) (string, error) {
	if err := e.checkOutputFile(in); err != nil {
		return "", err
	}

	return e.writeData(in)
}

// checkOutputFile stops a phase that declares outputs and names an output file other than the one file of the folder of its call,
// because the sandbox opens the folder of the output file to the agent.
func (e *Engine) checkOutputFile(in PhaseInput) error {
	if derived := outputFile(e.dir, in.Item, in.Call); len(in.Outputs) > 0 && in.OutputFile != derived {
		return fmt.Errorf("the agent phase of item %s names the output file %s, and the output file of this item and step is %s. The agent did not start. Run beehave cancel %s, then start a new run",
			in.Item, in.OutputFile, derived, in.Item)
	}

	return nil
}

// writeData writes the recorded data of the phase, mode 0600, to the data file that the engine derives for the item and the call, and returns that file.
// A phase with no data writes nothing and returns "". A phase with data and no call name stops before the agent starts.
func (e *Engine) writeData(in PhaseInput) (string, error) {
	if in.Data == "" {
		return "", nil
	}

	if in.Call == "" {
		return "", fmt.Errorf("the agent phase of item %s has data and no step name, so beehave cannot find its data file. The agent did not start. Start a new run", in.Item)
	}

	file := dataFile(e.dir, in.Item, in.Call)

	err := errors.Join(os.MkdirAll(filepath.Dir(file), outputsMode), os.WriteFile(file, []byte(in.Data), dataMode), os.Chmod(file, dataMode))
	if err != nil {
		return "", fmt.Errorf("write the data file %s of the step: %w", file, err)
	}

	return file, nil
}

// phaseOutput records how the agent of a phase ended.
func phaseOutput(result agent.Result) PhaseOutput {
	return PhaseOutput{
		Outcome: result.Outcome, ExitCode: result.ExitCode, Note: result.Note, Session: result.Session, Transcript: result.Transcript,
		Text: result.Text, Skills: result.Skills, Violations: result.Violations, Denials: result.Denials, Stderr: result.Stderr, Spend: result.Spend, Limit: result.Limit, Resumed: result.Resumed,
	}
}

// sessionEvent is the key of the event in which a phase saves the session id of its agent while the agent runs. A lost step records no output,
// and the event outlives the loss. A rewind of the phase drops the event with the agent step that wrote it.
const sessionEvent = "session"

// watch starts what keeps the output of the agent while it runs: the saver of its session ids and the writer of its stream. named and said give them
// the ids and the texts, and finish writes what waits and ends both. The caller must call finish once.
func (e *Engine) watch(ctx context.Context, in PhaseInput) (named func(session string), said func(agent.Said), finish func()) {
	saver, stream := e.saveSession(ctx, in), e.openStream(ctx, in)

	return saver.name, stream.say, func() {
		stream.finish()
		saver.finish()
	}
}

// saveSession starts the saver that writes the session ids of the agent as the event of the phase, from the context of the agent step.
// The library writes the event of a step at once, with no step of its own. Each write is logged. A refused write lets the run go on: a replay of a lost step then starts fresh.
// While the store is down, the library retries the write until the step ends. Only the saver waits then, so the agent and the reading of its output go on.
func (e *Engine) saveSession(ctx context.Context, in PhaseInput) *sessionSaver {
	// The library runs every step body with a context of its own. Any other context leaves step nil, and the write then fails and is logged.
	var step dbos.Context
	if c, isStep := ctx.(dbos.Context); isStep {
		step = c
	}

	return startSessionSaver(func(session string) {
		if err := dbos.SetEvent(step, sessionEvent, session); err != nil {
			e.logger.WarnContext(ctx, "cannot save the session of the agent", "item", in.Item, "session", session, "error", err)

			return
		}

		e.logger.InfoContext(ctx, "saved the session of the agent", "item", in.Item, "session", session)
	})
}

// sessionSaver writes the session ids of an agent on a goroutine of its own. The tap gives it each new id on the goroutine that reads the output of the agent,
// so a write that waits for the store never stops that reading. Only the newest id waits for a write: the event keeps only the last value,
// so an id that a newer one replaces before its write is not written.
type sessionSaver struct {
	write   func(session string)
	mu      sync.Mutex
	pending string
	// wake holds one signal that an id waits. stop ends the goroutine after it writes the id that waits, and done closes when it ended.
	wake, stop, done chan struct{}
}

// startSessionSaver starts the goroutine of a saver that writes each id with write. The caller must call finish once.
func startSessionSaver(write func(session string)) *sessionSaver {
	s := &sessionSaver{write: write, wake: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{})}

	go s.run()

	return s
}

// name gives a new session id to the saver. It never waits for the write.
func (s *sessionSaver) name(session string) {
	s.mu.Lock()
	s.pending = session
	s.mu.Unlock()

	select {
	case s.wake <- struct{}{}:
	default: // a signal waits already, and the write that it starts reads the newest id
	}
}

// run writes the id that waits after each signal. Each turn checks the stop first: once finish closed it, the next turn writes the id that waits
// and ends, even when a signal waits too.
func (s *sessionSaver) run() {
	defer close(s.done)

	for {
		select {
		case <-s.stop:
			s.flush()

			return
		default:
		}

		select {
		case <-s.wake:
			s.flush()
		case <-s.stop: // the next turn writes the id that waits
		}
	}
}

// flush writes the id that waits, if one waits.
func (s *sessionSaver) flush() {
	s.mu.Lock()
	session := s.pending
	s.pending = ""
	s.mu.Unlock()

	if session != "" {
		s.write(session)
	}
}

// finish waits until the saver wrote the newest id and its goroutine ended, so the event is written before the step records its output.
func (s *sessionSaver) finish() {
	close(s.stop)
	<-s.done
}

// recordGroup returns the function that writes the process group of an agent to the process file, under the attempt whose claim the agent runs under.
// A failed write is logged. The file serves the next start of beehave run: the next worker kills the groups that a crash left.
func (e *Engine) recordGroup(ctx context.Context, a *attempt, in PhaseInput) func(pid int) {
	return func(pid int) {
		if err := e.procs.add(ctx, a, pid); err != nil {
			e.logger.WarnContext(ctx, "cannot record the agent process", "item", in.Item, "error", err)
		}
	}
}

// endGroups forgets the agent groups of one attempt in the process file.
func (e *Engine) endGroups(a *attempt) {
	if err := e.procs.release(a); err != nil {
		e.logger.Warn("cannot update the agent process file", "error", err)
	}
}

// agentQueueName is the queue of an agent step: a read-only step (edits = False) runs on the reviews queue, a step that edits on the agents queue.
func agentQueueName(noEdits bool) string {
	if noEdits {
		return reviewsQueueName
	}

	return queueName
}

// agentQueue is the registered queue that agentQueueName names for a step.
func (e *Engine) agentQueue(noEdits bool) dbos.Queue { //nolint:ireturn // the queue type of the workflow library
	if agentQueueName(noEdits) == reviewsQueueName {
		return e.reviewQueue
	}

	return e.queue
}

// queuePhase runs a phase as a child workflow on the queue of its step, keyed by the login, and waits for it. It returns how the agent ended.
func (e *Engine) queuePhase(ctx dbos.Context, in PhaseInput) (PhaseOutput, error) {
	in.Run = workflowID(ctx)

	if _, err := e.claimed(in.Run, in.Item); err != nil {
		return PhaseOutput{}, err
	}

	handle, err := dbos.RunWorkflow(ctx, e.phase, in, dbos.WithQueue(e.agentQueue(in.NoEdits)), dbos.WithQueuePartitionKey(in.Login), dbos.WithPriority(placeOf(model.Severity(in.Severity), false)))
	if err != nil {
		return PhaseOutput{}, fmt.Errorf("queue the agent step of item %s: %w", in.Item, err)
	}

	return awaitResult(ctx, e.ctx, handle, e.deps.Clock.resultOptions())
}

// childResult is how a child workflow ended.
type childResult[T any] struct {
	out T
	err error
}

// settle returns the result of a child. The child run sees the stop of the engine before the context of the waiting run is canceled, and its error can arrive first.
// An error that arrives after the engine stopped is the stop, not a failure of the phase: the run stays open for the next start.
func (r childResult[T]) settle(engine dbos.Context) (T, error) { //nolint:ireturn // the result is the type of the child workflow
	if stop := engineStop(engine, r.err); stop != nil {
		var none T

		return none, stop
	}

	return r.out, r.err
}

// engineStop is the stop error when err is set and the engine has stopped, and nil otherwise. It reads the context of the engine, which the library cancels before the context of any run.
func engineStop(engine context.Context, err error) error {
	if err != nil && engine.Err() != nil {
		return stopError(engine)
	}

	return nil
}

// awaitResult waits for the result of a child workflow. A stop of the engine (its context) ends the wait without a failure, even when the error of the child comes first. A cancel of ctx ends the wait at once:
// the polling handle of the workflow library does not watch the context, so a stopped engine would wait for the phase to end.
func awaitResult[T any](ctx, engine dbos.Context, handle dbos.WorkflowHandle[T], options []dbos.GetResultOption) (T, error) { //nolint:ireturn // the result is the type of the child workflow
	done := make(chan childResult[T], 1)

	go func() {
		out, err := handle.GetResult(options...)
		done <- childResult[T]{out: out, err: err}
	}()

	select {
	case finished := <-done:
		return finished.settle(engine)
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
// A run that a provider limit stopped is not a failure either: the item run waits for the reset and runs the call again, so the wait holds no slot of the queue.
// Only a limit that no wait ends fails the phase (see limitEnd). now is the wall clock at the end of the run.
func phaseEnd(in PhaseInput, out PhaseOutput, now time.Time) error {
	if out.Outcome == agent.OutcomeLimited && out.Limit != nil {
		return limitEnd(in, *out.Limit, now)
	}

	if out.Outcome == agent.OutcomeTurnCap || out.Outcome == agent.OutcomeBudgetCap || out.Outcome == agent.OutcomeContextCap {
		return nil
	}

	if out.Outcome != agent.OutcomeExited || out.ExitCode != 0 {
		return fmt.Errorf("the agent step of item %s ended as %s (exit %d): %s. Fix the cause, then run beehave resume %s", in.Item, out.Outcome, out.ExitCode, out.Note, in.Item)
	}

	return nil
}
