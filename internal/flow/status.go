package flow

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"

	"github.com/tobocop2/beehave/internal/model"
)

// errNoRun is returned when an item has no run.
var errNoRun = errors.New("the item has no run")

// errEmptyAnswer is returned when the answer of the owner has no text.
var errEmptyAnswer = errors.New("the answer has no text")

// errWrongState is returned when a command does not fit the state of the run.
var errWrongState = errors.New("the run is in the wrong state for this command")

// RunState says where a run stands.
type RunState string

const (
	runRunning   RunState = "running"
	runQueued    RunState = "queued"
	runDone      RunState = "done"
	runFailed    RunState = "failed"
	runStopped   RunState = "stopped"
	runCancelled RunState = "cancelled" //nolint:misspell // the same spelling as agent.OutcomeCancelled: one word for one state
)

// RunInfo is one line of the status list. Stranded marks an open run that another version of beehave started.
type RunInfo struct {
	ID       string   `json:"id"`
	State    RunState `json:"state"`
	Version  string   `json:"version,omitempty"`
	Stranded bool     `json:"stranded"`
	// Item and Project are what the run started with. Both are empty when the stored input does not read.
	Item    string `json:"item,omitempty"`
	Project string `json:"project,omitempty"`
	// OlderSteps marks a stranded run that has the build version of this client, but was made by an older step format.
	OlderSteps bool `json:"older_steps"`
	// Question is what an open run asks the owner. An empty text means the run asks nothing.
	Question string `json:"question,omitempty"`
	// Asked names the people and the choices of the open wait, and who answered so far. It is nil for a wait that names neither.
	Asked *Asked `json:"asked,omitempty"`
	// Others names the other orchestrators that held items when an open run started, one line for each: the owner and its items. It is empty when none did.
	Others []string `json:"others,omitempty"`
	// SentBack is why the watch returned the pull request of a finished run to draft. An empty text means it did not.
	SentBack string `json:"sent_back,omitempty"`
	// Closed is the text for the owner when the pull request of a finished run was closed with no merge. An empty text means it was not.
	Closed string `json:"closed,omitempty"`
	// Merge is the state of the merge step: owner_merge while an open run waits for the owner, then merged, sent_back or closed. It is empty for a run with no merge step.
	Merge string `json:"merge,omitempty"`
	// Stopped is the command step that ended a finished run on a code of its `stop_on_rc`. It is nil when no step did. A run with a Stopped has the state stopped.
	Stopped *Stopped `json:"stopped,omitempty"`
	// RunProgress holds the cost against the run cap, the step that runs now, the deadline of a wait, the last gate result and the rounds.
	RunProgress
}

// Waiting reports whether the run is open and shows a question for the owner.
func (i RunInfo) Waiting() bool {
	return (i.State == runRunning || i.State == runQueued) && i.Question != ""
}

// questionReadLimit is how long status waits for an event that the run has not set.
const questionReadLimit = time.Millisecond

// Client reads and steers runs without running any. It never resumes a run on its own.
type Client struct {
	flow    dbos.Client
	version string
	// maxRunBudget is Options.MaxRunBudget, the cap that the status shows the spent sum against.
	maxRunBudget float64
	// reviewPoll is Options.ReviewPoll, so the status can say that the poll is off.
	reviewPoll time.Duration
	dir        string
	give       ReleaseDeps
	logger     *slog.Logger
	log        io.Closer
}

// OpenClient opens the state folder for the status, answer, resume and cancel commands.
// give releases the item of a run that cancel stops while no engine runs.
func OpenClient(ctx context.Context, opts Options, give ReleaseDeps) (*Client, error) {
	state, err := openStore(opts)
	if err != nil {
		return nil, err
	}

	client := &Client{version: applicationVersion(opts.Version), maxRunBudget: opts.MaxRunBudget, reviewPoll: opts.ReviewPoll, dir: state.dir, give: give, log: state.log}
	if err := client.connect(ctx, state); err != nil {
		return nil, errors.Join(fmt.Errorf("open the workflow store: %w", err), state.log.Close())
	}

	return client, nil
}

// connect migrates the store and opens the workflow client on it. The client needs the tables to exist, so a first status on a new folder lists no runs.
func (c *Client) connect(ctx context.Context, state store) error {
	c.logger = newLogger(state.log)

	if err := dbos.Migrate(ctx, state.url, dbos.WithMigrateLogger(c.logger)); err != nil {
		return err
	}

	flow, err := dbos.NewClient(ctx, dbos.ClientConfig{DatabaseURL: state.url, AppName: appName, Logger: c.logger})
	c.flow = flow

	return err
}

// Close closes the store and the log.
func (c *Client) Close() error {
	return errors.Join(dbos.Shutdown(c.flow, shutdownTimeout), c.log.Close())
}

// Status lists every run of every item, oldest first.
func (c *Client) Status() ([]RunInfo, error) {
	rows, err := dbos.ListWorkflows(c.flow, dbos.WithFilterWorkflowIDPrefix(runPrefix), dbos.WithFilterName(itemName), dbos.WithFilterLoadInput(true), dbos.WithFilterLoadOutput(true))
	if err != nil {
		return nil, fmt.Errorf("list the runs: %w", err)
	}

	infos := make([]RunInfo, 0, len(rows))

	for _, row := range rows {
		info, err := c.info(row)
		if err != nil {
			return nil, err
		}

		infos = append(infos, info)
	}

	return infos, nil
}

// Poll returns the newest review poll that finished, whatever its result: success, error, canceled or too many recovery attempts.
// A poll that did not succeed returns Failure and the time it ended, so an older poll never shows as current.
// The second value is false when no poll has finished.
func (c *Client) Poll() (PollOutput, bool, error) {
	rows, err := dbos.ListWorkflows(c.flow, dbos.WithFilterName(pollName),
		dbos.WithFilterStatus(dbos.WorkflowStatusSuccess, dbos.WorkflowStatusError, dbos.WorkflowStatusCancelled, dbos.WorkflowStatusMaxRecoveryAttemptsExceeded),
		dbos.WithFilterLoadOutput(true), dbos.WithFilterSortDesc(), dbos.WithFilterLimit(1))
	if err != nil {
		return PollOutput{}, false, fmt.Errorf("list the review polls: %w", err)
	}

	if len(rows) == 0 {
		return PollOutput{}, false, nil
	}

	return pollOf(rows[0])
}

// pollOf reads the result of one finished poll run.
func pollOf(row dbos.WorkflowStatus) (PollOutput, bool, error) {
	if row.Status != dbos.WorkflowStatusSuccess {
		return PollOutput{At: endOf(row), Projects: []PollProject{}, Failure: failureOf(row)}, true, nil
	}

	var out PollOutput
	// ListWorkflows returns the stored JSON of the output as a string.
	if err := json.Unmarshal([]byte(fmt.Sprint(row.Output)), &out); err != nil {
		return PollOutput{}, false, fmt.Errorf("read the result of the review poll %s: %w", row.ID, err)
	}

	return out, true, nil
}

// endOf returns the time a poll run ended: the completion time, or the last update when DBOS left no completion time.
func endOf(row dbos.WorkflowStatus) time.Time {
	if row.CompletedAt.IsZero() {
		return row.UpdatedAt.UTC()
	}

	return row.CompletedAt.UTC()
}

// failureOf describes a poll run that did not succeed: the error, or the status when the run was canceled or gave up recovering.
func failureOf(row dbos.WorkflowStatus) string {
	switch {
	case row.Status == dbos.WorkflowStatusError:
		return fmt.Sprint(row.Error)
	case row.Error != nil:
		return fmt.Sprintf("%s: %s", row.Status, row.Error)
	}

	return string(row.Status)
}

// PollNotScheduled returns why the review poll does not run: the interval is 0, or the last launch could not set the schedule. It is empty when the poll is scheduled or no launch ran.
// The configured interval decides the first case, whatever the record holds: a launch with 0 deletes the schedule and records no error.
func (c *Client) PollNotScheduled() (string, error) {
	text, err := os.ReadFile(pollScheduleFile(c.dir))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("read the state of the review poll schedule: %w", err)
	}

	if c.reviewPoll <= 0 {
		return strings.TrimSpace(pollOff + " " + string(text)), nil
	}

	return string(text), nil
}

// info describes one run of the store. An open run also carries the question it asks the owner.
func (c *Client) info(row dbos.WorkflowStatus) (RunInfo, error) {
	info := RunInfo{
		ID: row.ID, State: stateOf(row.Status), Version: buildVersion(row.ApplicationVersion),
		Stranded: !run{Status: row}.terminal() && row.ApplicationVersion != c.version,
	}

	info.OlderSteps = info.Stranded && info.Version == buildVersion(c.version)

	// A row whose input does not read has no item and no project.
	if in, err := (run{Status: row}).input(); err == nil {
		info.Item, info.Project = in.Item, in.Project
	}

	err := c.details(row, &info)

	return info, errors.Join(err, c.addProgress(row, &info))
}

// addProgress reads the progress of a run from its step records. A run that asks a question waits on it, and the question is what it shows in place of a step.
// A caller that gets an error ignores the info.
func (c *Client) addProgress(row dbos.WorkflowStatus, info *RunInfo) error {
	records, readErr := c.records(row.ID)
	lines := stepLines(records, info.State, !run{Status: row}.terminal(), time.Now())

	info.RunProgress = progressOf(lines, c.maxRunBudget, !run{Status: row}.terminal())
	info.Deadline = deadlineFor(row, records, info.Question)

	if info.Question != "" {
		info.Now = ""
	}

	return readErr
}

// details adds what the run shows besides its state. An open run shows its question and its merge state; a finished run shows its result.
func (c *Client) details(row dbos.WorkflowStatus, info *RunInfo) error {
	var err error

	switch {
	case !run{Status: row}.terminal():
		err = c.openDetails(row.ID, info)
	case info.State == runDone:
		var result ItemResult

		result, err = resultOf(row)
		info.SentBack, info.Closed, info.Merge, info.Stopped = result.SentBack, result.Closed, result.Merge, result.Stopped

		// The workflow returns a stop as a result, so the store records success. The result tells the two apart.
		if info.Stopped != nil {
			info.State = runStopped
		}
	}

	return err
}

// openDetails reads what an open run shows: its question, the other orchestrators it found and its merge state.
func (c *Client) openDetails(id string, info *RunInfo) error {
	var err error

	if info.Question, info.Asked, err = c.waitOf(id); err != nil {
		return err
	}

	if info.Others, err = c.othersOf(id); err != nil {
		return err
	}

	info.Merge, err = c.mergeOf(id)

	return err
}

// waitOf reads the question and the people of the open wait of a run. A wait that names people or choices sets the ask event and leaves the question event unset,
// so the question is the question event, or else the ask event. This is the one place that holds the rule.
func (c *Client) waitOf(id string) (string, *Asked, error) {
	question, err := c.questionOf(id)
	if err != nil {
		return "", nil, err
	}

	asked, err := c.askedOf(id)
	if err != nil {
		return "", nil, err
	}

	return cmp.Or(question, asked.questionText()), asked, nil
}

// questionOf reads the question an open run asks the owner.
func (c *Client) questionOf(id string) (string, error) {
	question, err := dbos.GetEvent[string](c.flow, id, questionKey, questionReadLimit)
	// A run that sets no question yet is not an error: the read times out.
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return "", fmt.Errorf("read the question of %s: %w", id, err)
	}

	return question, nil
}

// othersOf reads the other orchestrators that the run found at its start. A run that found none sets nothing.
func (c *Client) othersOf(id string) ([]string, error) {
	lines, err := dbos.GetEvent[[]string](c.flow, id, othersKey, questionReadLimit)
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return nil, fmt.Errorf("read the other orchestrators of %s: %w", id, err)
	}

	return lines, nil
}

// mergeOf reads the state that an open run sets while its merge step waits for the owner.
func (c *Client) mergeOf(id string) (string, error) {
	state, err := dbos.GetEvent[string](c.flow, id, mergeKey, questionReadLimit)
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return "", fmt.Errorf("read the merge state of %s: %w", id, err)
	}

	return state, nil
}

// resultOf reads the result of a finished run. A run with no result has an empty one.
func resultOf(row dbos.WorkflowStatus) (ItemResult, error) {
	if row.Output == nil {
		return ItemResult{}, nil
	}

	var result ItemResult
	// ListWorkflows returns the stored JSON of the output as a string.
	if err := json.Unmarshal([]byte(fmt.Sprint(row.Output)), &result); err != nil {
		return ItemResult{}, fmt.Errorf("read the result of %s: %w", row.ID, err)
	}

	return result, nil
}

func stateOf(status dbos.WorkflowStatusType) RunState {
	switch status {
	case dbos.WorkflowStatusEnqueued, dbos.WorkflowStatusDelayed:
		return runQueued
	case dbos.WorkflowStatusSuccess:
		return runDone
	case dbos.WorkflowStatusError, dbos.WorkflowStatusMaxRecoveryAttemptsExceeded:
		return runFailed
	case dbos.WorkflowStatusCancelled:
		return runCancelled
	case dbos.WorkflowStatusPending:
		// A pending run is running, as is a status this version does not know.
	}

	return runRunning
}

// Answer sends the answer of the owner to the open run of an item.
// A wait that names people refuses an answer with no name; AnswerAs gives one.
func (c *Client) Answer(item, text string) error { return c.AnswerAs(item, "", text) }

// requireQuestion refuses a run that is finished or that asks the owner nothing: a message to such a run would answer the next question at once.
// It returns the info of the run, which holds the question and who it asks.
func (c *Client) requireQuestion(item string, last run) (RunInfo, error) {
	if last.terminal() {
		return RunInfo{}, fmt.Errorf("%w: %s is %s and waits for no answer. Run beehave run %s to start again", errWrongState, last.Status.ID, stateOf(last.Status.Status), item)
	}

	info, err := c.info(last.Status)
	if err == nil && info.Question == "" {
		err = fmt.Errorf("%w: %s asks no question. Run beehave status to see which run waits for an answer", errWrongState, last.Status.ID)
	}

	return info, err
}

// Resume reopens the canceled or failed run of an item, from its last finished step. Run beehave run for the item to continue it.
func (c *Client) Resume(item string) error {
	last, err := c.latest(item)
	if err != nil {
		return err
	}

	if err := c.requireResumable(item, last); err != nil {
		return err
	}

	// The run replays its recorded claim step, whose claim was released. A fresh claim comes first, and the run refuses to reopen without it.
	project, owner, err := c.takeClaim(item, last)
	if err != nil {
		return err
	}

	if err := c.reopenRun(last); err != nil {
		return errors.Join(err, c.dropClaim(project, item, last.Status.ID, owner))
	}

	return nil
}

// dropClaim gives back a claim that Resume took for run id, when the run did not reopen, and removes the token it saved: a phase waits for the reopen of a run that has one.
// An empty owner means Resume took none.
func (c *Client) dropClaim(project, item, id, owner string) error {
	if owner == "" {
		return nil
	}

	ctx, cancel := c.give.limit(context.Background())
	defer cancel()

	return errors.Join(c.give.Lifecycle.Release(ctx, project, item, owner), removeFile(ownerPath(c.dir, id)))
}

// requireResumable refuses a run that is not canceled or failed, and a run that another version of beehave started.
func (c *Client) requireResumable(item string, last run) error {
	if state := stateOf(last.Status.Status); state != runCancelled && state != runFailed {
		return fmt.Errorf("%w: %s is %s and only a cancelled or failed run resumes. Run beehave run %s to start again", errWrongState, last.Status.ID, state, item) //nolint:misspell // user-facing state word, same as runCancelled
	}

	if last.Status.ApplicationVersion != c.version {
		return strandedError(last, c.version)
	}

	return nil
}

// reopenRun puts a canceled or failed run back to work. DBOS resumes a canceled run but ignores one that ended with an error: that one needs a rewind.
func (c *Client) reopenRun(last run) error {
	if last.Status.Status == dbos.WorkflowStatusError {
		return c.reopen(last.Status.ID)
	}

	// The cancel reached the gates and the merges that the run waited for. The run replays its steps, and a child that stays canceled would fail it again.
	if err := c.reopenCanceledPhases(last.Status.ID); err != nil {
		return err
	}

	_, err := dbos.ResumeWorkflow[ItemResult](c.flow, last.Status.ID)

	return err
}

// takeClaim takes a fresh claim on the item of a run and saves its token for the run. The run reads that token in place of the recorded one.
// A run whose claim step has no result takes no claim here: the step runs again and claims, so the owner is empty.
func (c *Client) takeClaim(item string, last run) (project, owner string, err error) {
	project, err = last.project()
	if err != nil {
		return "", "", err
	}

	recorded, err := recordedClaim(c.flow, last.Status.ID)
	if err != nil || recorded == "" {
		return project, "", err
	}

	owner, err = c.claimFresh(project, item, last.Status.ID)

	return project, owner, err
}

// claimFresh takes the claim under a timeout and saves its token for the run. A token that cannot be saved is given back.
func (c *Client) claimFresh(project, item, id string) (string, error) {
	ctx, cancel := c.give.limit(context.Background())
	defer cancel()

	owner, err := c.give.Lifecycle.Claim(ctx, project, item)
	if err != nil {
		return "", fmt.Errorf("%s of item %s: %w. If a canceled run holds the claim, the next beehave run frees it. Then run beehave resume %s again", model.StepClaim, item, err, item)
	}

	if err := saveOwner(c.dir, id, owner); err != nil {
		return "", errors.Join(err, c.dropClaim(project, item, id, owner))
	}

	return owner, nil
}

// Cancel stops the open run of an item and the phases it started.
func (c *Client) Cancel(item string) error {
	last, err := c.latest(item)
	if err != nil {
		return err
	}

	if last.terminal() {
		return fmt.Errorf("%w: %s is %s and has nothing to cancel", errWrongState, last.Status.ID, stateOf(last.Status.Status))
	}

	return c.cancelRun(item, last)
}

// cancelRun cancels an open run and its children, and gives back what the run held when no engine runs.
func (c *Client) cancelRun(item string, last run) error {
	// The marker comes first. An engine that stops before its release ends leaves it, and the next start finishes the release.
	// A marker for a run that stays open is harmless: the start drops it.
	project, err := c.markCancel(item, last)
	if err != nil {
		return err
	}

	err = dbos.CancelWorkflow(c.flow, last.Status.ID, dbos.WithCancelChildren())
	if err == nil {
		err = c.giveBackUnowned(project, item, last)
	}

	return err
}

// markCancel writes the cancel marker of a run and returns the project of the run.
func (c *Client) markCancel(item string, last run) (string, error) {
	project, err := last.project()
	if err != nil {
		return "", err
	}

	return project, markCancel(c.dir, last.Status.ID, ItemInput{Item: item, Project: project})
}

// giveBackUnowned releases the worktree and the claim of a canceled run when no engine runs. A running engine does it on its next tick,
// and an engine that stops first leaves the cancel marker, so the next start does it.
// It holds probe.lock and the engine lock until the release ends, so no engine starts in between and no check meets a holder that is not an engine.
func (c *Client) giveBackUnowned(project, item string, last run) error {
	probe, err := holdProbe(c.dir)
	if err != nil {
		return err
	}

	return errors.Join(c.releaseUnowned(project, item, last), probe.Unlock())
}

// releaseUnowned does the release of giveBackUnowned, under probe.lock, when it takes the engine lock.
func (c *Client) releaseUnowned(project, item string, last run) error {
	held, err := holdEngine(c.dir)
	if errors.Is(err, ErrStateBusy) {
		return nil
	}

	if err != nil {
		return err
	}

	// No engine runs the run, so no live wait can turn off its queued merge.
	list := func(opts ...dbos.ListWorkflowsOption) ([]dbos.WorkflowStatus, error) {
		return dbos.ListWorkflows(c.flow, opts...)
	}
	off := disarmCanceled(context.Background(), list, c.give.Merge, last.Status.ID)

	return errors.Join(off, c.giveBack(project, item, last.Status.ID), held.Unlock())
}

// giveBack releases the worktree and the claim of a run with the token the run holds, then removes the files of the run.
// A run that never claimed has nothing to give back.
func (c *Client) giveBack(project, item, id string) error {
	owner, err := currentOwner(c.flow, c.dir, id)
	if err == nil {
		err = c.give.giveBack(context.Background(), c.logger, project, item, owner)
	}

	if err != nil && !errors.Is(err, errNoOwner) {
		return err
	}

	return forgetRun(c.dir, id)
}

// PinnedFile returns the workflow file of the open run of the item. It is empty when the item has no open run, or when the open run uses no file.
// It reads the store and launches nothing.
func (c *Client) PinnedFile(item string) (string, error) {
	last, found, err := latestRun(c.flow, item)
	if err != nil || !found || last.terminal() {
		return "", err
	}

	in, err := last.input()

	return in.File, err
}

// latest returns the newest run of an item, and refuses a run that another version of beehave started.
func (c *Client) latest(item string) (run, error) {
	last, err := c.newest(item)
	if err != nil {
		return run{}, err
	}

	if last.Status.ApplicationVersion != c.version && !last.terminal() {
		return run{}, strandedError(last, c.version)
	}

	return last, nil
}

// newest returns the newest run of an item, whatever build started it.
func (c *Client) newest(item string) (run, error) {
	if model.ItemKind(item) == "" {
		return run{}, fmt.Errorf("%w %q: use an item id such as 12 or fix-login", ErrBadItem, item)
	}

	last, found, err := latestRun(c.flow, item)
	if err != nil {
		return run{}, err
	}

	if !found {
		return run{}, fmt.Errorf("%w: start item %s with beehave run %s", errNoRun, item, item)
	}

	return last, nil
}
