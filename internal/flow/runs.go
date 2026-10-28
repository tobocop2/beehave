package flow

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
)

const runPrefix = "item-"

const attemptMarker = "-r"

// ErrBadItem is returned when an item id is not an item id: digits, or a prefix, a dash and a name.
var ErrBadItem = errors.New("invalid item")

// errNoProject is returned when run names no project.
var errNoProject = errors.New("no project for the item")

// errProjectConflict is returned when an open run of the item belongs to another project.
var errProjectConflict = errors.New("the item has an open run under another project")

// errStranded is returned when a run belongs to another build version.
var errStranded = errors.New("the run belongs to another version of obee")

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
	case dbos.WorkflowStatusPending, dbos.WorkflowStatusEnqueued, dbos.WorkflowStatusDelayed:
		// The run is still open.
	}

	return false
}

// failed reports whether the attempt ended with an error. Only obee resume continues it.
func (r run) failed() bool {
	return stateOf(r.Status.Status) == runFailed
}

// failedError tells the owner why a new attempt does not start after a failed one.
// The error text of the run names the step that failed.
func failedError(failed run, item string) error {
	return fmt.Errorf("%w: %s failed: %s. Run obee resume %s to continue it from the last finished step",
		errRunFailed, failed.Status.ID, fmt.Sprint(failed.Status.Error), item)
}

// runID names attempt k of an item. A second start of the same attempt joins the first.
func runID(item string, attempt int) string {
	return runPrefix + item + attemptMarker + strconv.Itoa(attempt)
}

// latestRun returns the attempt of the item with the highest number. found is false when the item never ran.
func latestRun(client dbos.Client, item string) (run, bool, error) {
	prefix := runPrefix + item + attemptMarker

	rows, err := dbos.ListWorkflows(client, dbos.WithFilterWorkflowIDPrefix(prefix), dbos.WithFilterName(itemName), dbos.WithFilterLoadInput(true))
	if err != nil {
		return run{}, false, fmt.Errorf("list the runs of item %s: %w", item, err)
	}

	var last run

	found := false

	for _, row := range rows {
		attempt, err := strconv.Atoi(strings.TrimPrefix(row.ID, prefix))
		if err == nil && attempt > last.Attempt {
			last, found = run{Status: row, Attempt: attempt}, true
		}
	}

	return last, found, nil
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
	return fmt.Errorf("%w: item %s has an open run under project %s and you asked for %s. Run obee cancel %s to end it, then run obee run %s %s",
		errProjectConflict, item, held, asked, item, item, asked)
}

func strandedError(stranded run, version string) error {
	return fmt.Errorf("%w: %s started under version %s and this is %s. Start version %s to finish it",
		errStranded, stranded.Status.ID, stranded.Status.ApplicationVersion, version, stranded.Status.ApplicationVersion)
}
