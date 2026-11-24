package flow

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
)

const runPrefix = "item-"

// passPrefix starts the id of a pass run of a round, round-<base>-r<k>-p<n>, and passName is the workflow name of a pass run.
const (
	passPrefix = "round-"
	passName   = "round"
)

const attemptMarker = "-r"

// runIDForm matches the two forms of run id that the engine makes: item-<item>-r<n> and round-<base>-r<k>-p<n>.
var runIDForm = regexp.MustCompile(`^(item-.+-r[1-9][0-9]*|round-.+-r[1-9][0-9]*-p[1-9][0-9]*)$`)

// errBadRunID is returned when --run names an id that no run of beehave has the form of.
var errBadRunID = errors.New("not a run id")

// ErrBadItem is returned when an item id is not an item id: digits, or a prefix, a dash and a name.
var ErrBadItem = errors.New("invalid item")

// errNoProject is returned when run names no project.
var errNoProject = errors.New("no project for the item")

// errNoSandboxMode is returned when a run starts with no sandbox mode: the caller must give on or off.
var errNoSandboxMode = errors.New("the run has no sandbox mode")

// errProjectConflict is returned when an open run of the item belongs to another project.
var errProjectConflict = errors.New("the item has an open run under another project")

// errStranded is returned when a run belongs to another build version.
var errStranded = errors.New("the run belongs to another version of beehave")

// errRunFailed is returned when run finds that the last attempt of the item failed.
var errRunFailed = errors.New("the last run of the item failed")

// run is one attempt at an item, as the store holds it.
type run struct {
	Status  dbos.WorkflowStatus
	Attempt int
}

func (r run) terminal() bool {
	switch r.Status.Status {
	case dbos.WorkflowStatusSuccess, dbos.WorkflowStatusError, dbos.WorkflowStatusCancelled, dbos.WorkflowStatusMaxRecoveryAttemptsExceeded:
		return true
	case dbos.WorkflowStatusPending, dbos.WorkflowStatusEnqueued, dbos.WorkflowStatusDelayed, dbos.WorkflowStatusSuspended:
		// The run is still open.
	}

	return false
}

// delayed reports whether a debounced start holds the run back: it has not started, and the window is open.
func (r run) delayed() bool { return r.Status.Status == dbos.WorkflowStatusDelayed }

// cancelable reports whether a cancel reaches the run: an open run, a failed one, or a canceled one, whose children a cancel that stopped between two levels of the tree left open.
func (r run) cancelable() bool {
	return !r.terminal() || r.Status.Status == dbos.WorkflowStatusCancelled || r.failed()
}

// failed reports whether the attempt ended with an error. Only beehave resume continues it.
func (r run) failed() bool {
	return stateOf(r.Status.Status) == runFailed
}

// failedError tells the owner why a new attempt does not start after a failed one.
// The error text of the run names the step that failed.
func failedError(failed run, item string) error {
	return fmt.Errorf("%w: %s failed: %s. Run beehave resume %s to continue it from the last finished step. To start a new attempt instead, run beehave cancel %s",
		errRunFailed, failed.Status.ID, fmt.Sprint(failed.Status.Error), item, item)
}

// endFailedFirst gives the sentence that sends the owner to beehave cancel when the last run of the item failed, and an empty text for any other run.
func endFailedFirst(last run, item string) string {
	if !last.failed() {
		return ""
	}

	return fmt.Sprintf("End the failed run first: beehave cancel %s. ", item)
}

// runID names attempt k of an item. A second start of the same attempt joins the first.
func runID(item string, attempt int) string {
	return runPrefix + item + attemptMarker + strconv.Itoa(attempt)
}

// latestRun returns the attempt of the item with the highest number. found is false when the item never ran.
func latestRun(client storeView, item string) (run, bool, error) {
	attempts, err := attemptsOf(client, item, dbos.WithFilterLoadInput(true), dbos.WithFilterLoadOutput(true))
	if err != nil {
		return run{}, false, fmt.Errorf("list the runs of item %s: %w", item, err)
	}

	var last run

	found := false

	for _, attempt := range attempts {
		if attempt.Attempt > last.Attempt {
			last, found = attempt, true
		}
	}

	return last, found, nil
}

// attemptsOf lists the attempts of the item that the store holds, in no order. It is the one read of the runs of an item: latestRun and earlierRuns take it, and so do run_read and earlier_reads.
// The prefix of item 12 also matches a run of item 12-r2, whose tail is not a number: only a numbered id is an attempt of this item. A caller names in opts what the store loads of each run.
func attemptsOf(client storeView, item string, opts ...dbos.ListWorkflowsOption) ([]run, error) {
	prefix := runPrefix + item + attemptMarker

	rows, err := client.List(append([]dbos.ListWorkflowsOption{dbos.WithFilterWorkflowIDPrefix(prefix), dbos.WithFilterName(itemName)}, opts...)...)
	if err != nil {
		return nil, err //nolint:wrapcheck // the caller names the read
	}

	var attempts []run

	for _, row := range rows {
		if attempt, numbered := attemptOf(row.ID, prefix); numbered {
			attempts = append(attempts, run{Status: row, Attempt: attempt})
		}
	}

	return attempts, nil
}

// withBaseOf gives a run that a user started the base of the last run of the same item, which holds the base that its spawn gave it.
// An item with no earlier run keeps an empty base, so its worktree is cut at the base branch of the project.
func (in ItemInput) withBaseOf(last run, found bool) (ItemInput, error) {
	if !found {
		return in, nil
	}

	before, err := last.input()
	in.Base = before.Base

	return in, err
}

// project returns the project stored with the run. The store keeps the input of the workflow as JSON.
func (r run) project() (string, error) {
	in, err := r.input()

	return in.Project, err
}

// input returns the input the run started with.
func (r run) input() (ItemInput, error) {
	var in ItemInput
	if err := json.Unmarshal([]byte(fmt.Sprint(r.Status.Input)), &in); err != nil { // WithFilterLoadInput returns the stored JSON as a string
		return ItemInput{}, fmt.Errorf("read the input of run %s: %w", r.Status.ID, err)
	}

	return in, nil
}

// conflictError names both projects when run asks for a project the open run of the item does not use.
func conflictError(item, held, asked string) error {
	return fmt.Errorf("%w: item %s has an open run under project %s and you asked for %s. Run beehave cancel %s to end it, then run beehave run %s %s",
		errProjectConflict, item, held, asked, item, item, asked)
}

// strandedError names the version that a run was stored under. When only the version of the steps differs, no build of this version can finish the run.
func strandedError(stranded run, version string) error {
	stored := stranded.Status.ApplicationVersion

	if buildVersion(stored) == buildVersion(version) {
		return fmt.Errorf("%w: %s was made by an older step format, stored as version %s, and this is %s. Start the build that made it to finish it",
			errStranded, stranded.Status.ID, stored, version)
	}

	moveWith := ""
	if in, err := stranded.input(); err == nil {
		moveWith = fmt.Sprintf(", or run beehave resume %s --on-this-build to move the run to this build from its last finished step", in.Item)
	}

	return fmt.Errorf("%w: %s started under version %s and this is %s. Start version %s to finish it%s",
		errStranded, stranded.Status.ID, stored, version, buildVersion(stored), moveWith)
}
