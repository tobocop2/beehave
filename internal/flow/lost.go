package flow

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
)

const (
	// startStepPrefix and lostStepPrefix name the steps of a phase that record each start of the agent and each loss of a start.
	startStepPrefix = "agent/start"
	lostStepPrefix  = "agent/lost"
	agentStepName   = "agent"
	// noRecord is the cause of a loss. The engine that died took its child processes with it, and nothing it left shows an exit status or a signal.
	noRecord = "the process ended with no record"
)

// LostStep records an agent step that started and did not finish, because the engine stopped. It is the record of the step that ran again.
// Lost is the last renewal of the claim lease of the item that the claim store held when the new engine took over, so Ran is how long the step ran before the loss.
// Lost is the zero time when the store held no claim lease for the item: the run time is then unknown.
type LostStep struct {
	Started time.Time `json:"started"`
	Lost    time.Time `json:"lost"`
	Cause   string    `json:"cause"`
	Note    string    `json:"note"`
}

// Known is true when the loss time was read from the claim store.
func (l LostStep) Known() bool {
	return !l.Lost.IsZero()
}

// Ran is how long the step ran before the loss. A loss before the start counts as no time. It is zero when the loss time is not known.
func (l LostStep) Ran() time.Duration {
	if !l.Known() {
		return 0
	}

	return max(l.Lost.Sub(l.Started), 0)
}

// note is the paragraph that the prompt of the step that runs again gets. The text is the same for every workflow.
func (l LostStep) note() string {
	ran := "ran before for an unknown time and was lost"
	if l.Known() {
		ran = "ran before and was lost after " + minutes(l.Ran())
	}

	return "This step " + ran + ". The work tree may hold its edits. " +
		"Read `git status` and `git diff` before you start, and keep what is right."
}

// minutes writes a run time in whole minutes.
func minutes(d time.Duration) string {
	switch n := int(d / time.Minute); n {
	case 0:
		return "less than a minute"
	case 1:
		return "1 minute"
	default:
		return strconv.Itoa(n) + " minutes"
	}
}

// markStarts records the start of the agent of a phase, in a step, before the agent starts. A step that is recorded was started before:
// the start steps that the store already holds are the earlier starts, and each earlier start that has no end was lost.
// It returns the input for the agent: the prompt gets the note when this start follows a loss.
// The list of steps is read from outside the phase, so the read is not itself a step. The number of calls to steps stays the same on every replay.
func (e *Engine) markStarts(ctx dbos.Context, in PhaseInput) (PhaseInput, error) {
	earlier, err := e.startsOf(workflowID(ctx))
	if err != nil {
		return in, err
	}

	var previous time.Time

	for n := 1; n <= earlier.total; n++ {
		started, lost, err := e.markStart(ctx, in, n, previous, n > 1)
		if err != nil {
			return in, err
		}

		in = in.noted(lost.Note, n == earlier.total && !earlier.ended)
		previous = started
	}

	return in, nil
}

// noted returns the input with the note added to the prompt, when add is true and the note is not empty.
func (in PhaseInput) noted(note string, add bool) PhaseInput {
	if add && note != "" {
		in.Prompt += "\n\n" + note
	}

	return in
}

// markStart records start n, and when lose is true the loss of the start before it, which began at previous.
func (e *Engine) markStart(ctx dbos.Context, in PhaseInput, n int, previous time.Time, lose bool) (time.Time, LostStep, error) {
	started, err := runStep(e, ctx, startStepPrefix+strconv.Itoa(n), func(context.Context) (time.Time, error) { return e.deps.Clock.wall(), nil })
	if err != nil || !lose {
		return started, LostStep{}, err
	}

	lost, err := runStep(e, ctx, lostStepPrefix+strconv.Itoa(n-1), e.loss(in, previous))

	return started, lost, err
}

// loss returns the step that records the loss of the start at the time started, and logs it.
func (e *Engine) loss(in PhaseInput, started time.Time) func(context.Context) (LostStep, error) {
	return func(ctx context.Context) (LostStep, error) {
		lost := LostStep{Started: started, Lost: e.holds.renewedBefore(in.Run), Cause: noRecord}
		lost.Note = lost.note()

		ran := "unknown"
		if lost.Known() {
			ran = lost.Ran().Round(time.Second).String()
		}

		e.logger.WarnContext(ctx, "the agent step was lost", "item", in.Item, "started", started.Format(time.RFC3339), "ran", ran, "cause", lost.Cause,
			"resume", "the step starts the agent again and tells it to read the work tree")

		return lost, nil
	}
}

// earlierStarts is what the store holds about the starts of the agent of a phase.
type earlierStarts struct {
	// total is the number of starts this execution has: the recorded ones, plus the new one when the agent step has not ended.
	total int
	// ended is true when the agent step of this phase ended.
	ended bool
}

// startsOf reads the steps of a phase that an earlier execution recorded.
func (e *Engine) startsOf(id string) (earlierStarts, error) {
	var earlier earlierStarts

	steps, err := dbos.GetWorkflowSteps(e.ctx, id, dbos.WithStepsLoadOutput(false))
	if err != nil {
		return earlier, fmt.Errorf("read the steps of the agent step: %w", err)
	}

	earlier.total, earlier.ended = countStarts(steps)
	if !earlier.ended {
		earlier.total++
	}

	return earlier, err
}

// countStarts returns how many start steps the list holds, and whether it holds the agent step.
func countStarts(steps []dbos.StepInfo) (starts int, ended bool) {
	for _, step := range steps {
		ended = ended || step.StepName == agentStepName
		starts += startNumber(step.StepName)
	}

	return starts, ended
}

// startNumber is 1 for the name of a start step and 0 for any other step.
func startNumber(name string) int {
	rest, found := strings.CutPrefix(name, startStepPrefix)
	if _, err := strconv.Atoi(rest); found && err == nil {
		return 1
	}

	return 0
}
