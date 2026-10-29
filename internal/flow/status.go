package flow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"

	"github.com/tobocop2/obee/internal/model"
)

// errNoRun is returned when an item has no run.
var errNoRun = errors.New("the item has no run")

// errEmptyAnswer is returned when the answer of the owner has no text.
var errEmptyAnswer = errors.New("the answer has no text")

// errWrongState is returned when a command does not fit the state of the run.
var errWrongState = errors.New("the run is in the wrong state for this command")

// runState says where a run stands.
type runState string

const (
	runRunning   runState = "running"
	runQueued    runState = "queued"
	runDone      runState = "done"
	runFailed    runState = "failed"
	runCancelled runState = "cancelled" //nolint:misspell // the same spelling as agent.OutcomeCancelled: one word for one state
)

// RunInfo is one line of the status list. Stranded marks an open run that another version of obee started.
type RunInfo struct {
	ID       string
	State    runState
	Version  string
	Stranded bool
	// OlderSteps marks a stranded run that has the build version of this client, but was made by an older step format.
	OlderSteps bool
	// Question is what an open run asks the owner. An empty text means the run asks nothing.
	Question string
	// SentBack is why the watch returned the pull request of a finished run to draft. An empty text means it did not.
	SentBack string
	// Merge is the state of the merge step: owner_merge while an open run waits for the owner, then merged or sent_back. It is empty for a run with no merge step.
	Merge string
	// Stopped is the command step that ended a finished run on a code of its `stop_on_rc`. It is nil when no step did.
	Stopped *Stopped
}

// questionReadLimit is how long status waits for an event that the run has not set.
const questionReadLimit = time.Millisecond

// Client reads and steers runs without running any. It never resumes a run on its own.
type Client struct {
	flow    dbos.Client
	version string
	dir     string
	give    ReleaseDeps
	logger  *slog.Logger
	log     io.Closer
}

// OpenClient opens the state folder for the status, answer, resume and cancel commands.
// give releases the item of a run that cancel stops while no engine runs.
func OpenClient(ctx context.Context, opts Options, give ReleaseDeps) (*Client, error) {
	state, err := openStore(opts)
	if err != nil {
		return nil, err
	}

	client := &Client{version: applicationVersion(opts.Version), dir: state.dir, give: give, log: state.log}
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
	rows, err := dbos.ListWorkflows(c.flow, dbos.WithFilterWorkflowIDPrefix(runPrefix), dbos.WithFilterName(itemName), dbos.WithFilterLoadOutput(true))
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

// info describes one run of the store. An open run also carries the question it asks the owner.
func (c *Client) info(row dbos.WorkflowStatus) (RunInfo, error) {
	info := RunInfo{
		ID: row.ID, State: stateOf(row.Status), Version: buildVersion(row.ApplicationVersion),
		Stranded: !run{Status: row}.terminal() && row.ApplicationVersion != c.version,
	}

	info.OlderSteps = info.Stranded && info.Version == buildVersion(c.version)

	if err := c.details(row, &info); err != nil {
		return RunInfo{}, err
	}

	return info, nil
}

// details adds what the run shows besides its state. An open run shows its question and its merge state; a finished run shows its result.
func (c *Client) details(row dbos.WorkflowStatus, info *RunInfo) error {
	var err error

	switch {
	case !run{Status: row}.terminal():
		info.Question, err = c.questionOf(row.ID)
		if err == nil {
			info.Merge, err = c.mergeOf(row.ID)
		}
	case info.State == runDone:
		var result ItemResult

		result, err = resultOf(row)
		info.SentBack, info.Merge, info.Stopped = result.SentBack, result.Merge, result.Stopped
	}

	return err
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

func stateOf(status dbos.WorkflowStatusType) runState {
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
func (c *Client) Answer(item, text string) error {
	if text == "" {
		return fmt.Errorf("%w: write the answer after the item", errEmptyAnswer)
	}

	last, err := c.latest(item)
	if err != nil {
		return err
	}

	if err := c.requireQuestion(item, last); err != nil {
		return err
	}

	return dbos.Send(c.flow, last.Status.ID, text, ownerTopic)
}

// requireQuestion refuses a run that is finished or that asks the owner nothing: a message to such a run would answer the next question at once.
func (c *Client) requireQuestion(item string, last run) error {
	if last.terminal() {
		return fmt.Errorf("%w: %s is %s and waits for no answer. Run obee run %s to start again", errWrongState, last.Status.ID, stateOf(last.Status.Status), item)
	}

	asked, err := c.info(last.Status)
	if err == nil && asked.Question == "" {
		err = fmt.Errorf("%w: %s asks no question. Run obee status to see which run waits for an answer", errWrongState, last.Status.ID)
	}

	return err
}

// Resume reopens the canceled or failed run of an item, from its last finished step. Run obee run for the item to continue it.
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
		return errors.Join(err, c.dropClaim(project, item, owner))
	}

	return nil
}

// dropClaim gives back a claim that Resume took, when the run did not reopen. An empty owner means Resume took none.
func (c *Client) dropClaim(project, item, owner string) error {
	if owner == "" {
		return nil
	}

	ctx, cancel := c.give.limit(context.Background())
	defer cancel()

	return c.give.Lifecycle.Release(ctx, project, item, owner)
}

// requireResumable refuses a run that is not canceled or failed, and a run that another version of obee started.
func (c *Client) requireResumable(item string, last run) error {
	if state := stateOf(last.Status.Status); state != runCancelled && state != runFailed {
		return fmt.Errorf("%w: %s is %s and only a cancelled or failed run resumes. Run obee run %s to start again", errWrongState, last.Status.ID, state, item) //nolint:misspell // user-facing state word, same as runCancelled
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
		return "", fmt.Errorf("%s of item %s: %w. If a canceled run holds the claim, the next obee run frees it. Then run obee resume %s again", model.StepClaim, item, err, item)
	}

	if err := saveOwner(c.dir, id, owner); err != nil {
		return "", errors.Join(err, c.dropClaim(project, item, owner))
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
// The engine lock stays held until the release ends, so no engine starts in between.
func (c *Client) giveBackUnowned(project, item string, last run) error {
	held, err := holdEngine(c.dir)
	if errors.Is(err, errStateBusy) {
		return nil
	}

	if err != nil {
		return err
	}

	return errors.Join(c.giveBack(project, item, last.Status.ID), held.Unlock())
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

// latest returns the newest run of an item, and refuses a run that another version of obee started.
func (c *Client) latest(item string) (run, error) {
	if model.ItemKind(item) == "" {
		return run{}, fmt.Errorf("%w %q: use an item id such as 12 or fix-login", ErrBadItem, item)
	}

	last, found, err := latestRun(c.flow, item)
	if err != nil {
		return run{}, err
	}

	if !found {
		return run{}, fmt.Errorf("%w: start item %s with obee run %s", errNoRun, item, item)
	}

	if last.Status.ApplicationVersion != c.version && !last.terminal() {
		return run{}, strandedError(last, c.version)
	}

	return last, nil
}
