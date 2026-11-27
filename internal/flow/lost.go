package flow

import (
	"cmp"
	"context"
	"errors"
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
	// callStartRef is the per-worktree ref that the render step of an agent call writes at the start commit. Git deletes it with the worktree.
	callStartRef = "refs/worktree/beehest/call-start"
	// continueStep ends the prompt of a resumed session of a step.
	continueStep = "Continue the step from where you stopped. Your brief and the output file are earlier in this session."
)

// lostView is what the note of a lost step can say about the changes of the step.
type lostView int

const (
	// viewDiff is a folder that holds the start mark of the call: DiffStat holds its changes since the start commit, empty for none.
	viewDiff lostView = iota
	// viewNoStart is a call with no start commit.
	viewNoStart
	// viewOtherTree is a folder whose start mark is missing or names another commit: it is not the folder that the call started in.
	viewOtherTree
	// viewUnread is a folder that git could not read.
	viewUnread
)

// LostStep records an agent step that started and did not finish, because the engine stopped. It is the record of the step that ran again.
// Lost is the last renewal of the claim lease of the item that the claim store held when the new engine took over, so Ran is how long the step ran before the loss.
// Lost is the zero time when the store held no claim lease for the item: the run time is then unknown.
// Session is the session id that the lost agent named, empty when the loss came before the agent named one.
// DiffStat is the diff summary of the item folder since the start commit of the call (git diff --stat), empty when nothing tracked changed,
// when the call has no start commit, when the folder does not hold the start mark of the call, or when the engine could not read the diff.
type LostStep struct {
	Started  time.Time `json:"started"`
	Lost     time.Time `json:"lost"`
	Cause    string    `json:"cause"`
	Note     string    `json:"note"`
	Session  string    `json:"session,omitempty"`
	DiffStat string    `json:"diff_stat,omitempty"`
}

// diffStatFiles is the most files that the diff summary of a lost step names. Git adds one line for the files left out, and the summary line.
const diffStatFiles = 50

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

// note is the paragraph that the prompt of a fresh run of the step gets. It says what the view lets the engine say about the changes of the step.
// The text is the same for every workflow.
func (l LostStep) note(view lostView) string {
	ran := "ran before for an unknown time and was lost"
	if l.Known() {
		ran = "ran before and was lost after " + minutes(l.Ran())
	}

	return "This step " + ran + ". " + treeNote(view, l.DiffStat)
}

// treeNote is the end of the note of a step that runs again: what the view lets the engine say about the changes of the step, and the request to read them.
func treeNote(view lostView, stat string) string {
	return changes(view, stat) + "Read `git status` and `git diff` before you start, and keep what is right."
}

// changes is the part of the note about the changes of the step, with stat the diff summary. Only a folder that holds the start mark of the call gets a claim about its changes.
func changes(view lostView, stat string) string {
	switch view {
	case viewNoStart:
		return "The work tree may hold its edits. The engine has no start commit of the step, so it cannot say what the step changed. "
	case viewOtherTree:
		return "The work tree is not the one that the step started in: it was made again or removed, or its start mark was removed. " +
			"The edits of the step can be lost, and the engine cannot say what the step changed. "
	case viewUnread:
		return "The work tree may hold its edits. The engine could not read the diff summary of the changes since the step started. "
	case viewDiff:
	}

	if stat == "" {
		return "The work tree may hold its edits. The engine found no change to tracked files since the step started. "
	}

	return "The work tree may hold its edits. The engine read this summary of the changes since the step started:\n\n```\n" + stat + "\n```\n\n"
}

// continued is the prompt of a resumed session of the step. The brief and the output file are in the session already.
func (l LostStep) continued() string {
	ran := "an unknown time"
	if l.Known() {
		ran = minutes(l.Ran())
	}

	return "The engine stopped while you worked on this step, after " + ran + ". " + continueStep
}

// lostRun returns the lost run for the start that follows the loss, or nil when the lost agent named no session: the note then goes into the prompt.
func (l LostStep) lostRun() *LostRun {
	if l.Session == "" {
		return nil
	}

	return &LostRun{Session: l.Session, Note: l.Note, Continue: l.continued()}
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
// It returns the input for the agent and the lost run when this start follows a loss: a loss that named a session gives the lost run,
// and a loss that named none gives the note in the prompt.
// The list of steps is read from outside the phase, so the read is not itself a step. The number of calls to steps stays the same on every replay.
func (e *Engine) markStarts(ctx dbos.Context, in PhaseInput) (PhaseInput, *LostRun, error) {
	earlier, err := e.startsOf(workflowID(ctx))
	if err != nil {
		return in, nil, err
	}

	previous, last := time.Time{}, LostStep{}

	for n := 1; n <= earlier.total; n++ {
		started, lost, err := e.markStart(ctx, in, n, previous, n > 1)
		if err != nil {
			return in, nil, err
		}

		last, previous = lost, started
	}

	in, run := in.afterLoss(last, earlier.ended)

	return in, run, nil
}

// afterLoss returns the input and the lost run of the start that follows the loss last. A loss that named a session gives the lost run.
// A loss that named none gives the note in the prompt. A start that follows no loss, or an agent step that ended, keeps the input as it is.
func (in PhaseInput) afterLoss(last LostStep, ended bool) (PhaseInput, *LostRun) {
	if ended || last.Note == "" {
		return in, nil
	}

	if run := last.lostRun(); run != nil {
		return in, run
	}

	in.Prompt += "\n\n" + last.Note

	return in, nil
}

// markStart records start n, and when lose is true the loss of the start before it, which began at previous.
func (e *Engine) markStart(ctx dbos.Context, in PhaseInput, n int, previous time.Time, lose bool) (time.Time, LostStep, error) {
	started, err := runStep(e, ctx, startStepPrefix+strconv.Itoa(n), func(context.Context) (time.Time, error) { return e.deps.Clock.wall(), nil })
	if err != nil || !lose {
		return started, LostStep{}, err
	}

	lost, err := runStep(e, ctx, lostStepPrefix+strconv.Itoa(n-1), e.loss(in, workflowID(ctx), previous))

	return started, lost, err
}

// loss returns the step that records the loss of the start at the time started, and logs it. The record holds the session that the lost agent
// saved in the event of the phase, and the diff summary of the item folder. The event is read with the context of the engine,
// because the library refuses a read of an event inside a step. A replay reads the record, so a second replay resumes the same session.
func (e *Engine) loss(in PhaseInput, phaseID string, started time.Time) func(context.Context) (LostStep, error) {
	return func(ctx context.Context) (LostStep, error) {
		lost, err := e.lostRecord(ctx, in, phaseID, started)
		if err != nil {
			return lost, err
		}

		ran := "unknown"
		if lost.Known() {
			ran = lost.Ran().Round(time.Second).String()
		}

		e.logger.WarnContext(ctx, "the agent step was lost", "item", in.Item, "started", started.Format(time.RFC3339), "ran", ran, "cause", lost.Cause,
			"session", cmp.Or(lost.Session, "none"), "resume", resumeWord(lost.Session))

		return lost, nil
	}
}

// lostRecord returns the record of the loss of the start at the time started: the run time, the saved session, the diff summary and the note.
func (e *Engine) lostRecord(ctx context.Context, in PhaseInput, phaseID string, started time.Time) (LostStep, error) {
	lost := LostStep{Started: started, Lost: e.holds.renewedBefore(in.Run), Cause: noRecord}

	session, err := e.savedSession(phaseID)
	if err != nil {
		return lost, err
	}

	// Only the note uses the diff summary, so a diff that cannot be read changes the note and keeps the session.
	stat, view, unread := e.lostDiff(ctx, in)
	if unread != nil {
		e.logger.WarnContext(ctx, "cannot read the diff summary of the lost agent step", "item", in.Item, "error", unread)
	}

	lost.Session, lost.DiffStat = session, stat
	lost.Note = lost.note(view)

	return lost, nil
}

// savedSession returns the session id that the agent of a phase saved, or "" when it saved none. The library answers a read of an event
// that does not exist with a timeout, so the read waits for no time.
func (e *Engine) savedSession(phaseID string) (string, error) {
	session, err := dbos.GetEvent[string](e.ctx, phaseID, sessionEvent, 0)

	var engineErr *dbos.Error
	if errors.As(err, &engineErr) && engineErr.Code == dbos.ErrorCodeTimeout {
		return "", nil
	}

	if err != nil {
		return "", fmt.Errorf("read the session of the lost agent step: %w", err)
	}

	return session, nil
}

// lostDiff returns the diff summary of the item folder since the start commit of the call, and what the note can say about it.
// An error comes with viewUnread.
func (e *Engine) lostDiff(ctx context.Context, in PhaseInput) (string, lostView, error) {
	if in.StartHead == "" {
		return "", viewNoStart, nil
	}

	dir, view, err := e.lostFolder(ctx, in)
	if view != viewDiff {
		return "", view, err
	}

	out, err := git(ctx, e.deps.Trees.Git(dir), "diff", "--stat", "--stat-count="+strconv.Itoa(diffStatFiles), in.StartHead)
	if err != nil {
		return "", viewUnread, fmt.Errorf("read the diff summary of the item folder: %w", err)
	}

	return strings.TrimRight(out, "\n"), viewDiff, nil
}

// lostFolder returns the folder of the lost call with viewDiff when it holds the start mark of the call at the start commit. The folder comes from
// the same call as the folder of the agent call, so a removed worktree of a round item is made again at its tree start here, without the mark.
func (e *Engine) lostFolder(ctx context.Context, in PhaseInput) (string, lostView, error) {
	dir, err := e.deps.Trees.Ensure(ctx, in.Project, in.Item, in.TreeStart, in.Base)
	if err != nil {
		return "", viewUnread, fmt.Errorf("find the folder of item %s: %w", in.Item, err)
	}

	if dir == "" {
		return "", viewOtherTree, nil
	}

	mark, err := git(ctx, e.deps.Trees.Git(dir), "for-each-ref", "--format=%(objectname)", callStartRef)
	if err != nil {
		return "", viewUnread, fmt.Errorf("read the start mark of the item folder: %w", err)
	}

	if strings.TrimSpace(mark) != in.StartHead {
		return "", viewOtherTree, nil
	}

	return dir, viewDiff, nil
}

// resumeWord says in the log how the step starts again.
func resumeWord(session string) string {
	if session == "" {
		return "the step starts the agent again and tells it to read the work tree"
	}

	return "the step resumes the session of the agent, or starts it again with the note when the session cannot resume"
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
